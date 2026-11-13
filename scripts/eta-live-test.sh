#!/usr/bin/env bash
# 端到端验证脚本：建任务 → 轮询进度/ETA → 打印每个采样点的阶段、百分比、速率、剩余时间。
# 用法：scripts/eta-live-test.sh <CIDR> [每轮间隔秒]
set -u
API=${API:-http://localhost:18888/api}
CIDR=${1:-10.193.0.0/16}
INTERVAL=${2:-10}

TOKEN=$(curl -s -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | python -c 'import sys,json;print(json.load(sys.stdin).get("token",""))')
[ -z "$TOKEN" ] && { echo "LOGIN_FAILED"; exit 1; }
AUTH="Authorization: Bearer $TOKEN"

TASK=$(curl -s -X POST "$API/tasks" -H "$AUTH" -H 'Content-Type: application/json' -d "{
  \"name\":\"ETA验证-$CIDR-$(date +%H%M%S)\",
  \"ipRanges\":\"$CIDR\",
  \"portMode\":\"top100\",
  \"aliveProbe\":true,
  \"nodeIds\":\"all\",
  \"thread\":600,
  \"timeout\":3,
  \"run\":true
}")
TID=$(echo "$TASK" | python -c 'import sys,json;d=json.load(sys.stdin);print(d.get("id") or (d.get("task") or {}).get("id") or "")')
[ -z "$TID" ] && { echo "CREATE_FAILED: $TASK"; exit 1; }
echo "TASK_ID=$TID"

printf '%-8s %-9s %6s %7s %9s %12s %-11s %-8s\n' TIME PHASE PCT PPS UNITS ETA CONF PROBE
while :; do
  J=$(curl -s "$API/tasks" -H "$AUTH")
  echo "$J" | python - "$TID" <<'PY'
import sys, json
tid = int(sys.argv[1])
d = json.load(sys.stdin)
rows = d if isinstance(d, list) else d.get("tasks") or d.get("items") or d.get("data") or []
for r in rows:
    if int(r.get("id", 0)) != tid:
        continue
    q = r.get("queue") or {}
    eta = r.get("etaSeconds", -1)
    if eta is None or eta < 0:
        etas = "估算中"
    elif eta >= 86400:
        etas = f"{eta//86400}天{eta%86400//3600}时"
    elif eta >= 3600:
        etas = f"{eta//3600}时{eta%3600//60}分"
    elif eta >= 60:
        etas = f"{eta//60}分{eta%60}秒"
    else:
        etas = f"{eta}秒"
    units = f'{q.get("done",0)}/{q.get("total",0)}'
    probe = f'{r.get("probeOffset",0)}/{r.get("totalIps",0)}'
    import time as _t
    print(f'{_t.strftime("%H:%M:%S"):<8} {str(q.get("phase") or r.get("phase") or "-"):<9}'
          f' {r.get("percent",-1):>6} {round(r.get("pps") or 0):>7} {units:>9} {etas:>12}'
          f' {str(r.get("etaConfidence") or "-"):<11} {probe:<8}', flush=True)
    if r.get("status") in ("done", "failed", "interrupted"):
        print("STATUS=" + str(r.get("status")), flush=True)
        sys.exit(7)
    sys.exit(0)
print("(task not in list yet)", flush=True)
PY
  [ $? -eq 7 ] && break
  sleep "$INTERVAL"
done
echo "DONE"
