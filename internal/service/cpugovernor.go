package service

// 服务端 CPU 预算（v0.0.50）
//
// 问题（实测事故）：平台中心自己也干重活——存活探测对 9 万多 IP × 16 端口做
// ICMP + TCP connect，容器 CPU 直接打到 160%~220%（us 38% + sy 38% + softirq 15%），
// 4 核机器上整机立刻顶满，web 请求排队、页面打不开。节点侧的智能调速管不到这里，
// 因为这段逻辑跑在 server 进程里。
//
// 处理办法：
//  1. 探测并发按核数定（不再写死 1200）：probeConcurrency() = clamp(cpuN*220, 256, 1200)。
//  2. 探测分块（probeChunkHosts 个 IP 一块），块间看整机 CPU：
//     超过预算就按超出幅度停一会儿（probePaceDelay），并逐块下调并发；
//     远低于预算则小幅回补。这是唯一能在"没有引擎内部限速钩子"的前提下
//     把 server 自身占用压回预算内的办法。
//
// 预算口径与节点一致：预留核数默认 4 核留 1、8 核留 2（env CPU_RESERVE_CORES 可覆盖），
// 即 4 核机器上平台整体最多用 75%，剩下的留给 web/DB/系统。

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cpuReserveCoresServer 与节点侧 cpuReserveCores 同口径（这里单独一份，
// 避免 internal/service 反向依赖 cmd/agent）。
func cpuReserveCoresServer(cpuN int) int {
	if cpuN < 1 {
		cpuN = 1
	}
	res := 0
	switch {
	case cpuN >= 8:
		res = 2
	case cpuN >= 4:
		res = 1
	}
	if v := strings.TrimSpace(os.Getenv("CPU_RESERVE_CORES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			res = n
		}
	}
	if res > cpuN-1 {
		res = cpuN - 1
	}
	if res < 0 {
		res = 0
	}
	return res
}

// cpuBudgetPct 整机 CPU 预算（100 = 全部核）。
func cpuBudgetPct() float64 {
	cpuN := runtime.NumCPU()
	if cpuN < 1 {
		cpuN = 1
	}
	cores := cpuN - cpuReserveCoresServer(cpuN)
	if cores < 1 {
		cores = 1
	}
	b := float64(cores) / float64(cpuN) * 100
	if b > 92 {
		b = 92
	}
	return b
}

// hostBusyPct 读 /proc/stat 汇总行，算两次采样之间的整机占用。
// 首次调用只打点，返回 ok=false（拿不到差值就不做判断，绝不瞎猜）。
func hostBusyPct() (float64, bool) {
	idle, total, ok := readStatCPUTimes()
	if !ok {
		return 0, false
	}
	cpuStatMu.Lock()
	prevIdle, prevTotal, primed := cpuStatIdle, cpuStatTotal, cpuStatPrimed
	cpuStatIdle, cpuStatTotal, cpuStatPrimed = idle, total, true
	cpuStatMu.Unlock()
	if !primed || total <= prevTotal {
		return 0, false
	}
	dTotal := total - prevTotal
	dIdle := idle - prevIdle
	if dTotal == 0 || dIdle > dTotal {
		return 0, false
	}
	return float64(dTotal-dIdle) / float64(dTotal) * 100, true
}

var (
	cpuStatMu     sync.Mutex
	cpuStatIdle   uint64
	cpuStatTotal  uint64
	cpuStatPrimed bool
)

// readStatCPUTimes 读 /proc/stat 第一行，返回 (idle+iowait, total)。
func readStatCPUTimes() (uint64, uint64, bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		f := strings.Fields(line)[1:]
		if len(f) < 4 {
			return 0, 0, false
		}
		var total, idle uint64
		for i, x := range f {
			v, err := strconv.ParseUint(x, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			total += v
			if i == 3 || i == 4 {
				idle += v
			}
		}
		if total == 0 {
			return 0, 0, false
		}
		return idle, total, true
	}
	return 0, 0, false
}

// probeConcurrency 探测并发：按核数给，不写死。
// 每核 220 路 ≈ 单核 connect 吞吐的安全值（4 核 → 880）。上限仍是 1200，
// 与历史实测的准确性平衡点一致；下限 256 保证小机器进度条也在动。
func probeConcurrency() int {
	cpuN := runtime.NumCPU()
	if cpuN < 1 {
		cpuN = 1
	}
	n := cpuN * 220
	if n < 256 {
		n = 256
	}
	if n > 1200 {
		n = 1200
	}
	if v := strings.TrimSpace(os.Getenv("ALIVE_PROBE_CONCURRENCY")); v != "" {
		if c, err := strconv.Atoi(v); err == nil && c >= 32 && c <= 4096 {
			n = c
		}
	}
	return n
}

// probePaceDelay 块间的让路时长：
//
//	超出预算 20 点以上 → 停 1.2s
//	超出预算           → 停 400ms
//	预算下 20 点以内    → 不停
//
// 为什么是"停一会儿"而不是"降低并发"：connect 探测的 CPU 是 syscall 级的，
// 降并发对 CPU 的影响是线性的、但会让大网段耗时翻倍；块间短停只损失极少的
// 总时长（每 2048 个 IP 最多停 1.2s），却能把整机占用压回去。
func probePaceDelay(busy, budget float64) time.Duration {
	if busy > budget+20 {
		return 1200 * time.Millisecond
	}
	if busy > budget {
		return 400 * time.Millisecond
	}
	return 0
}

// ===== 识别闸门 =====
//
// AI 识别每条都可能拉起一个 chromium 页面渲染，单个渲染进程就能吃掉一个核。
// 扫描期间识别队列一路跑，容器 CPU 峰值可以到 445%（实测）——web 又被打回去。
// 所以在每个识别作业前加一道闸：整机超预算就等一下再干，让网页请求先过。

var (
	busyCachedMu sync.Mutex
	busyCached   float64
	busyCachedAt time.Time
)

// hostBusyCached 1 秒内复用上次读数：/proc/stat 是累计值，
// 采样间隔太短（毫秒级）差值噪声大，反而会误判。
func hostBusyCached() (float64, bool) {
	busyCachedMu.Lock()
	defer busyCachedMu.Unlock()
	if !busyCachedAt.IsZero() && time.Since(busyCachedAt) < time.Second {
		return busyCached, true
	}
	v, ok := hostBusyPct()
	if ok {
		busyCached, busyCachedAt = v, time.Now()
	}
	return busyCached, ok
}

// identCPUGate 识别作业前的 CPU 闸门。最多等 2 秒，避免极端情况下把队列完全卡死。
func identCPUGate() time.Duration {
	busy, ok := hostBusyCached()
	if !ok {
		return 0
	}
	d := probePaceDelay(busy, cpuBudgetPct())
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	if d > 0 {
		time.Sleep(d)
	}
	return d
}
