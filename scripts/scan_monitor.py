#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""扫描期间的主机 / 平台采样器（在 Kali 上跑，纯标准库）。

每 10s 采一次，写 JSONL：
  host_cpu  —— 整机 CPU 占用（/proc/stat 差分，100=全部核）
  load1     —— 1 分钟负载
  mem_avail —— MemAvailable(MB)
  api_*     —— 平台接口往返耗时（ms），400/500 也照记，用于判断"页面是否可用"
  task      —— 任务进度（units done/total、已发现服务数）
  agent_cpu —— 扫描节点容器 CPU%（docker stats --no-stream，30s 一次）
"""
import json
import os
import subprocess
import time
import urllib.request

OUT = "/root/mon_scan.jsonl"
TASK_FILE = "/root/mon_task.txt"
TOKEN_FILE = "/root/mon_token.txt"
BASE = "http://127.0.0.1:18888"


def token():
    try:
        return open(TOKEN_FILE).read().strip()
    except Exception:
        return ""


def cpu_times():
    with open("/proc/stat") as f:
        line = f.readline()
    f = line.split()[1:]
    vals = [int(x) for x in f[:8]]
    idle = vals[3] + vals[4]
    return idle, sum(vals)


def mem_avail():
    for l in open("/proc/meminfo"):
        if l.startswith("MemAvailable:"):
            return int(l.split()[1]) // 1024
    return -1


def api(path, ms=True):
    t0 = time.time()
    try:
        tok = token()
        req = urllib.request.Request(BASE + path)
        if tok:
            req.add_header("Authorization", "Bearer " + tok)
        with urllib.request.urlopen(req, timeout=30) as r:
            body = r.read()
            code = r.status
    except Exception as e:
        return round((time.time() - t0) * 1000), 0, str(e)[:60]
    return round((time.time() - t0) * 1000), code, len(body)


def task_progress():
    try:
        tid = open(TASK_FILE).read().strip()
    except Exception:
        return None
    if not tid:
        return None
    ms, code, n = api("/api/tasks/%s/overview" % tid)
    if code != 200:
        return {"id": tid, "err": code, "ms": ms}
    tok = token()
    req = urllib.request.Request(BASE + "/api/tasks/%s/overview" % tid)
    req.add_header("Authorization", "Bearer " + tok)
    with urllib.request.urlopen(req, timeout=30) as r:
        d = json.loads(r.read())
    return {"id": tid, "ms": ms, "data": d}


def agent_cpu():
    try:
        p = subprocess.run(["docker", "stats", "--no-stream", "--format",
                            "{{.Name}} {{.CPUPerc}} {{.MemUsage}}"],
                           capture_output=True, text=True, timeout=25)
        out = {}
        for line in p.stdout.strip().split("\n"):
            parts = line.split()
            if len(parts) >= 2 and parts[0].startswith("asset-"):
                out[parts[0]] = {"cpu": parts[1], "mem": " ".join(parts[2:])}
        return out
    except Exception as e:
        return {"err": str(e)[:60]}


def main():
    prev = cpu_times()
    prev_t = time.time()
    n = 0
    while True:
        time.sleep(10)
        now = cpu_times()
        dt = max(time.time() - prev_t, 0.001)
        dtotal = now[1] - prev[1]
        didle = now[0] - prev[0]
        busy = round((dtotal - didle) / dtotal * 100, 1) if dtotal > 0 else -1
        prev, prev_t = now, time.time()
        try:
            load1 = round(os.getloadavg()[0], 2)
        except Exception:
            load1 = -1
        rec = {
            "ts": time.strftime("%H:%M:%S"),
            "host_cpu": busy,
            "load1": load1,
            "mem_avail": mem_avail(),
            "health_ms": api("/api/health")[0],
            "tasks_ms": api("/api/tasks?page=1&size=20")[0],
            "assets_ms": api("/api/assets?page=1&size=20")[0],
        }
        tp = task_progress()
        if tp:
            rec["task"] = tp
        n += 1
        if n % 3 == 0:
            rec["containers"] = agent_cpu()
        with open(OUT, "a") as f:
            f.write(json.dumps(rec, ensure_ascii=False) + "\n")


main()
