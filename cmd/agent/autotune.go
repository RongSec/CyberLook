package main

// 智能调速（auto pilot）—— 节点自己决定「同时在途多少个探针」，不靠人配。
//
// 为什么需要它：limit（在途探针数）是吞吐的唯一决定因素（pps ≈ limit ÷ 单探针延迟），
// 但它有上界，而且上界随机器不同：
//
//	① fd 墙：connect 扫描每个在途探针占一个 socket，超过 nofile 就是 EMFILE（探针发不出去）；
//	② CPU 墙：并发超过某点后，每个探针自己的延迟被拉长，吞吐不升反降（纯亏）；
//	③ 内存墙：每个在途探针有栈和缓冲，8GB 小机器跑 2000 在途会被 OOM kill。
//
// 这三堵墙没法靠一个固定值覆盖（4 核 8G 与 8 核 16G 差三倍），所以做成闭环：
// 启动按机器规格算一个「天花板 base」，然后每 10s 看一次运行反馈，在 [base/4, base] 内调。
//
// 三条反馈的来源：
//
//	fd    → 引擎返回的 LocalResErrs（EMFILE 计数），非零说明已越过本机能力，立刻大幅回退；
//	CPU   → /proc/self/stat 的 utime+stime 差分（进程自身占用，不是整机 load，避免被别的容器干扰）；
//	探针数 → 每个单元扫完回报，用来判断"这一窗口到底有没有在干活"（空闲时不该乱调）。
//
// 人工优先：服务端下发过 limit/pause 的任务，scanner.AutoManualHold 内不自动干预，
// 避免"我调成 60，十秒后又被机器自己推回去"这种打架行为。

import (
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"asset-platform/internal/scanner"
)

const (
	// autoTick 采样/调整周期。太短会被单个单元的粒度噪声带着抖，太长跟不上环境变化。
	autoTick = 10 * time.Second
	// autoMinInflight 再省也不能低于它，否则一个 /24 要跑十几分钟。
	autoMinInflight = 100
	// autoMaxCap 自动挡绝对上限（防止算错把机器打爆）。
	autoMaxCap = 4000
)

type autoTuner struct {
	mu sync.Mutex

	cpuN     int
	memMB    int    // 0 = 读不到（非 Linux 或无 cgroup）
	fdBudget int    // nofile 安全余量，来自 checkNoFileLimit
	base     int    // 天花板
	cur      int    // 当前推荐值
	reason   string // 最近一次调整的原因（日志与排查用）

	winProbes    int64
	lastLocalRes int64 // 上次读到的累计 EMFILE 数（用来算本窗口增量）
	winStart     time.Time

	lastTicks uint64
	lastAt    time.Time
	highCPU   int // 连续高 CPU 窗口数
}

var autoPilot *autoTuner
var autoOnce sync.Once

// startAutoTuner 启动闭环。env SCAN_INFLIGHT_AUTO=off 可关闭（回到固定并发）。
func startAutoTuner(fdBudget int) {
	autoOnce.Do(func() {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("SCAN_INFLIGHT_AUTO")), "off") {
			log.Printf("[auto] 智能调速已关闭（SCAN_INFLIGHT_AUTO=off），使用固定并发")
			return
		}
		autoPilot = newAutoTuner(fdBudget)
		log.Printf("[auto] 智能调速启用：本机 %d 核 / %s，fd 预算 %d → 在途天花板 %d，起步 %d",
			autoPilot.cpuN, memDesc(autoPilot.memMB), fdBudget, autoPilot.base, autoPilot.cur)
		go autoPilot.loop()
	})
}

// autoInflight 供 queueLimiter 取当前推荐值；未启用时返回 0（调用方回落到节点配置）。
func autoInflight() int {
	if autoPilot == nil {
		return 0
	}
	return autoPilot.recommended()
}

func newAutoTuner(fdBudget int) *autoTuner {
	cpuN := runtime.NumCPU()
	mem := memLimitMB()

	base := fdBudget
	if v := cpuN * 350; v < base {
		base = v
	}
	if mem > 0 {
		if v := mem * 3; v < base {
			base = v
		}
	}
	if v := envInt("SCAN_INFLIGHT_MAX", 0); v > 0 && v < base {
		base = v
	}
	if base > autoMaxCap {
		base = autoMaxCap
	}
	if base < autoMinInflight*2 {
		base = autoMinInflight * 2
	}

	// 起步给 3/4：先保守跑，让观测把它推到该到的位置；不直接顶格是为了避免
	// 开头几秒就撞 fd/CPU 墙、把第一批单元打成失败重试。
	cur := base * 3 / 4
	return &autoTuner{
		cpuN: cpuN, memMB: mem, fdBudget: fdBudget, base: base, cur: cur,
		reason: "启动初值", winStart: time.Now(), lastAt: time.Now(),
	}
}

func (t *autoTuner) recommended() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cur
}

// note 汇报一个单元实际扫了多少探针，作为"这一窗口有没有在干活"的判断依据。
// 本地资源错误不走这里：它是进程级累计计数，由 tune() 自己读差分（见下）。
func (t *autoTuner) note(probes int) {
	if t == nil || probes <= 0 {
		return
	}
	t.mu.Lock()
	t.winProbes += int64(probes)
	t.mu.Unlock()
}

func (t *autoTuner) loop() {
	tk := time.NewTicker(autoTick)
	defer tk.Stop()
	for range tk.C {
		t.tune()
		// 把推荐值推到所有在跑的任务（人工调控过的任务会被 SetTaskAutoLimit 挡回）
		for _, id := range activeTaskIDs() {
			applyAutoLimit(id, t.recommended())
		}
	}
}

// tune 一次闭环调整。返回是否发生变化。
func (t *autoTuner) tune() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	cpu, ticks, cpuOK := procCPUPercent(t.lastTicks, t.lastAt)
	if cpuOK {
		t.lastTicks, t.lastAt = ticks, now
	}

	probes := t.winProbes
	t.winProbes = 0
	t.winStart = now

	// EMFILE 等本地资源错误是进程级累计计数，这里读差分：
	// 只加不清零，天然不会因为"某个单元提前退出"丢计数。
	total := scanner.LocalResErrsTotal()
	localRes := total - t.lastLocalRes
	if localRes < 0 {
		localRes = 0
	}
	t.lastLocalRes = total

	prev := t.cur
	why := t.decide(probes, localRes, cpu, cpuOK)
	if t.cur != prev {
		t.reason = why
		log.Printf("[auto] 在途上限 %d → %d（%s；CPU %s，本机 %d 核 / %s，fd 预算 %d，本窗口 %d 探针）",
			prev, t.cur, why, fmtPct(cpu, cpuOK), t.cpuN, memDesc(t.memMB), t.fdBudget, probes)
		return true
	}
	t.reason = why
	return false
}

// decide 是纯决策函数（不读文件、不看时间），便于单测：
//
//	输入：本窗口探针数、本窗口本地资源错误数、进程 CPU 使用率
//	输出：调整原因（"维持"表示没动）
//
// 规则（按优先级）：
//
//	1. 出现 EMFILE 等本地资源错误 → 乘 0.6（硬错误，退得很）
//	2. CPU > 85% 连续两个窗口   → 乘 0.8（再加并发只会拉长每个探针延迟，纯亏）
//	3. CPU < 55%               → 乘 1.15 试探（撞墙下一轮退回来，代价很小）
//	4. 其它                    → 维持
//	5. 本窗口零探针（暂停/无任务）→ 不动，避免凭"CPU 低"空推
func (t *autoTuner) decide(probes, localRes int64, cpu float64, cpuOK bool) string {
	if probes == 0 {
		return "空闲，不调整"
	}
	why := ""
	switch {
	case localRes > 0:
		t.cur = t.cur * 6 / 10
		t.highCPU = 0
		why = "本地资源错误 " + strconv.FormatInt(localRes, 10) + " 次"
	case cpuOK && cpu > 85:
		t.highCPU++
		if t.highCPU >= 2 {
			t.cur = t.cur * 8 / 10
			why = "CPU 持续高位"
		} else {
			why = "CPU 首次冲高，观察"
		}
	case cpuOK && cpu < 55:
		t.highCPU = 0
		t.cur = t.cur * 115 / 100
		why = "CPU 有余量"
	default:
		t.highCPU = 0
		why = "维持"
	}

	// 下限 base/4：退到这里说明机器真的吃力；再低吞吐就没意义。
	lo := t.base / 4
	if lo < autoMinInflight {
		lo = autoMinInflight
	}
	if t.cur < lo {
		t.cur = lo
	}
	if t.cur > t.base {
		t.cur = t.base
	}
	return why
}

// ===== 平台相关的机器指标 =====

// procCPUPercent 用 /proc/self/stat 的 utime+stime 差分算**进程自身** CPU 占用。
// 不用整机 loadavg：节点可能和别的服务混部，整机负载高不代表我们该降并发。
// 非 Linux（Windows）读不到 /proc → 返回 ok=false，此时闭环退化为"只看 fd"。
func procCPUPercent(prevTicks uint64, prevAt time.Time) (float64, uint64, bool) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, prevTicks, false
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return 0, prevTicks, false
	}
	f := strings.Fields(s[i+1:])
	// f[0] 是 state（field 3）；utime=field14 → f[11]，stime=field15 → f[12]
	if len(f) < 13 {
		return 0, prevTicks, false
	}
	ut, e1 := strconv.ParseUint(f[11], 10, 64)
	st, e2 := strconv.ParseUint(f[12], 10, 64)
	if e1 != nil || e2 != nil {
		return 0, prevTicks, false
	}
	ticks := ut + st
	if prevTicks == 0 || prevAt.IsZero() {
		return 0, ticks, false
	}
	elapsed := time.Since(prevAt).Seconds()
	if elapsed <= 0 {
		return 0, ticks, false
	}
	// 绝大多数 Linux 的 USER_HZ = 100
	pct := float64(ticks-prevTicks) / 100.0 / elapsed * 100.0
	if pct < 0 {
		pct = 0
	}
	return pct, ticks, true
}

// memLimitMB 读容器/整机内存上限。cgroup v2 → v1 → /proc/meminfo，都读不到返回 0。
func memLimitMB() int {
	tryBytes := func(p string) int {
		b, err := os.ReadFile(p)
		if err != nil {
			return 0
		}
		s := strings.TrimSpace(string(b))
		if s == "" || s == "max" {
			return 0
		}
		n, err := strconv.ParseInt(s, 10, 64)
		// v1 在无限制时会写一个接近 2^63 的巨大值，视为未知
		if err != nil || n <= 0 || n > 1<<50 {
			return 0
		}
		return int(n / 1024 / 1024)
	}
	if v := tryBytes("/sys/fs/cgroup/memory.max"); v > 0 {
		return v
	}
	if v := tryBytes("/sys/fs/cgroup/memory/memory.limit_in_bytes"); v > 0 {
		return v
	}
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				if kb, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					return int(kb / 1024)
				}
			}
		}
	}
	return 0
}

func memDesc(mb int) string {
	if mb <= 0 {
		return "内存未知"
	}
	return strconv.Itoa(mb) + "MB"
}

func fmtPct(v float64, ok bool) string {
	if !ok {
		return "未知"
	}
	return strconv.Itoa(int(v)) + "%"
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
