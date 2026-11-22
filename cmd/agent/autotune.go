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
//	fd     → 引擎返回的 LocalResErrs（EMFILE 计数），非零说明已越过本机能力，立刻大幅回退；
//	CPU    → /proc/stat 的整机 idle 差分（**整机**占用，不是只看自己的进程）；
//	探针数 → 每个单元扫完回报，用来判断这一窗口到底有没有在干活（空闲时不该乱调）。
//
// v0.0.50 修正（实测事故）：原来只看 /proc/self/stat 的**本进程** CPU，而平台中心（web +
// AI 识别 + chromium 渲染 + PostgreSQL）和节点是混部在同一台机器上的——识别那部分 CPU
// 全在别的进程里，调速器一点都看不见，于是一路把在途探针推高，直到整机被吃满、
// web 页面打不开。现在改成读**整机**占用，并预留核数预算（SCAN_CPU_RESERVE_CORES，
// 4 核留 1 核、8 核留 2 核），超过预算立刻回退；同时进程自己的 GOMAXPROCS 也按
// 预算收窄（见 cpubudget.go），保证运行时调度器不跟 web 抢最后一个核。
//
// 人工优先：服务端下发过 limit/pause 的任务，scanner.AutoManualHold 内不自动干预，
// 避免"我调成 60，十秒后又被机器自己推回去"这种打架行为。

import (
	"fmt"
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

	cpuN        int
	reserveCores int // 留给同机 web/DB/chromium 的核数
	memMB       int  // 0 = 读不到（非 Linux 或无 cgroup）
	fdBudget    int  // nofile 安全余量，来自 checkNoFileLimit
	base        int  // 天花板
	cur         int  // 当前推荐值
	reason      string // 最近一次调整的原因（日志与排查用）

	winProbes    int64
	lastLocalRes int64 // 上次读到的累计 EMFILE 数（用来算本窗口增量）
	winStart     time.Time

	lastTicks uint64
	lastAt    time.Time
	// 整机 CPU 采样（/proc/stat，容器内读到的是宿主机视角，正好包含 chromium 等兄弟进程）
	lastHostIdle  uint64
	lastHostTotal uint64
	highCPU       int // 连续超预算窗口数
}

var autoPilot *autoTuner
var autoOnce sync.Once

// startAutoTuner 启动闭环。env SCAN_INFLIGHT_AUTO=off 可关闭（回到固定并发）。
func startAutoTuner(fdBudget int) {
	autoOnce.Do(func() {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("SCAN_INFLIGHT_AUTO")), "off") {
			log.Printf("[调速] 智能调速已关闭（SCAN_INFLIGHT_AUTO=off），使用固定并发")
			return
		}
		autoPilot = newAutoTuner(fdBudget)
		log.Printf("[调速] 智能调速启用：本机 %d 核 / %s，fd 预算 %d → 在途天花板 %d，起步 %d；"+
			"整机 CPU 预算 %.0f%%（预留 %d 核给平台）",
			autoPilot.cpuN, memDesc(autoPilot.memMB), fdBudget, autoPilot.base, autoPilot.cur,
			autoPilot.busyBudget(), autoPilot.reserveCores)
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

// cpuReserveCores 给同机其它组件留的核数。
//
// 4 核留 1 核（25%）、8 核及以上留 2 核（25%）、2 核及以下不留——
// 小机器上再留一核就只剩半台机器干活，吞吐会跌到不可用。
// SCAN_CPU_RESERVE_CORES 可显式指定（0 = 不留），但永远不允许留到一核不剩。
func cpuReserveCores(cpuN int) int {
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
	if v := strings.TrimSpace(os.Getenv("SCAN_CPU_RESERVE_CORES")); v != "" {
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

func newAutoTuner(fdBudget int) *autoTuner {
	cpuN := runtime.NumCPU()
	res := cpuReserveCores(cpuN)
	mem := memLimitMB()

	base := fdBudget
	// CPU 天花板按"可用核"算，把留给平台的那部分先扣掉。
	cores := cpuN - res
	if cores < 1 {
		cores = 1
	}
	if v := cores * 350; v < base {
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
		cpuN: cpuN, reserveCores: res, memMB: mem, fdBudget: fdBudget, base: base, cur: cur,
		// lastAt 故意留零值：它同时充当"是否已经采过样"的标记。
		// 不能用 lastTicks==0 当标记 —— 新进程的 utime+stime 合法地就是 0。
		reason: "启动初值", winStart: time.Now(),
	}
}

func (t *autoTuner) recommended() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cur
}

// busyBudget 整机 CPU 预算（百分比，100 = 全部核）。超过它就该退并发——这是
// "留一点余量"的唯一口径，所有阈值都由它派生，避免又多一套写死的数字。
func (t *autoTuner) busyBudget() float64 {
	cpuN := t.cpuN
	if cpuN < 1 {
		cpuN = 1
	}
	cores := cpuN - t.reserveCores
	if cores < 1 {
		cores = 1
	}
	b := float64(cores) / float64(cpuN) * 100
	// 任何机器都别顶到满：最后 8% 留给内核、日志落盘和突发。
	if b > 92 {
		b = 92
	}
	return b
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
	hostBusy, idle, total, hostOK := hostCPUPercent(t.lastHostIdle, t.lastHostTotal)
	// 无条件记录本次读数。两个坑都在这：
	//   ① 上一版只在 cpuOK 时存 → 首次采样只有打点没有差值，lastTicks 永远停在 0，
	//      之后每次都被判成"无 CPU 信号"，CPU 一路从来没生效过（实测发现）；
	//   ② 用 "ticks > 0" 当"打过点"的判据也不行 —— 刚启动的进程 utime/stime 就是 0，
	//      合法的 0 会被误判成"还没打点"。
	// 所以直接存：读失败时 ticks 返回的是旧值，等价于没更新，无害。
	t.lastTicks, t.lastAt = ticks, now
	if hostOK {
		t.lastHostIdle, t.lastHostTotal = idle, total
	}

	probes := t.winProbes
	t.winProbes = 0
	t.winStart = now

	// EMFILE 等本地资源错误是进程级累计计数，这里读差分：
	// 只加不清零，天然不会因为"某个单元提前退出"丢计数。
	totalErr := scanner.LocalResErrsTotal()
	localRes := totalErr - t.lastLocalRes
	if localRes < 0 {
		localRes = 0
	}
	t.lastLocalRes = totalErr

	prev := t.cur
	why := t.decide(probes, localRes, cpu, cpuOK, hostBusy, hostOK)
	if t.cur != prev {
		t.reason = why
		log.Printf("[调速] 在途上限 %d → %d（%s；整机 CPU %s / 预算 %.0f%%，本进程 CPU %s，本机 %d 核 / %s，fd 预算 %d，本窗口 %d 探针）",
			prev, t.cur, why, fmtPct(hostBusy, hostOK), t.busyBudget(), fmtPct(cpu, cpuOK),
			t.cpuN, memDesc(t.memMB), t.fdBudget, probes)
		return true
	}
	t.reason = why
	return false
}

// decide 是纯决策函数（不读文件、不看时间），便于单测：
//
//	输入：本窗口探针数、本窗口本地资源错误数、本进程 CPU 使用率、整机 CPU 使用率
//	输出：调整原因（"维持"表示没动）
//
// 规则（按优先级）：
//
//	1. 出现 EMFILE 等本地资源错误        → 乘 0.6（硬错误，退得很）
//	2. 整机 CPU 超过预算 +10 个点        → 立刻乘 0.7（web 已经在卡，不能等第二个窗口）
//	3. 整机 CPU 超过预算                 → 连续两个窗口乘 0.85（小幅退，避免抖动）
//	4. 整机 CPU 低于预算 -20 个百分点    → 乘 1.10 试探（慢一点爬，撞墙代价小）
//	5. 读不到整机 CPU（Windows/无 /proc）→ 退回本进程 CPU：>90% 连续两窗口退、<70% 涨
//	6. 其它                              → 维持
//	7. 本窗口零探针（暂停/无任务）        → 不动，避免凭"CPU 低"空推
//
// 为什么要整机口径：平台中心（web/AI 识别/chromium/DB）与节点混部。只盯本进程时，
// 识别把整机吃满也看不出来，实测表现为"扫描一开，页面打不开"。
func (t *autoTuner) decide(probes, localRes int64, cpu float64, cpuOK bool, hostBusy float64, hostOK bool) string {
	if probes == 0 {
		return "空闲，不调整"
	}
	budget := t.busyBudget()
	why := ""
	switch {
	case localRes > 0:
		t.cur = t.cur * 6 / 10
		t.highCPU = 0
		why = "本地资源错误 " + strconv.FormatInt(localRes, 10) + " 次"
	case hostOK && hostBusy > budget+10:
		// 已经越界：整机在超载，web 请求会被饿死。这一步不等第二个窗口。
		t.highCPU++
		t.cur = t.cur * 7 / 10
		why = fmt.Sprintf("整机 CPU %.0f%% 已超预算 %.0f%%，立即回退", hostBusy, budget)
	case hostOK && hostBusy > budget:
		t.highCPU++
		if t.highCPU >= 2 {
			t.cur = t.cur * 85 / 100
			why = fmt.Sprintf("整机 CPU %.0f%% 持续高于预算 %.0f%%", hostBusy, budget)
		} else {
			why = fmt.Sprintf("整机 CPU %.0f%% 接近预算 %.0f%%，观察", hostBusy, budget)
		}
	case hostOK:
		t.highCPU = 0
		if hostBusy > budget-20 {
			why = fmt.Sprintf("整机 CPU %.0f%% 在预算内，维持", hostBusy)
		} else if cpuOK && cpu > 90 {
			// 整机有余量但自己这一个进程已经很忙：再加并发只会让每个探针更慢。
			why = "本进程 CPU 偏高，维持"
		} else {
			t.cur = t.cur * 110 / 100
			why = fmt.Sprintf("整机 CPU %.0f%% 有余量", hostBusy)
		}
	case cpuOK && cpu > 90:
		t.highCPU++
		if t.highCPU >= 2 {
			t.cur = t.cur * 8 / 10
			why = "CPU 持续高位"
		} else {
			why = "CPU 首次冲高，观察"
		}
	case cpuOK && cpu < 70:
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

// procCPUPercent 用 /proc/self/stat 的 utime+stime 差分算**进程自身** CPU 占用（100 = 一个核）。
// 只作为整机读数不可用时的兜底，以及"自己是不是已经很忙"的辅助判据。
// 非 Linux（Windows）读不到 /proc → 返回 ok=false。
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
	// 只用"上次采样时间"判断是否已经打过点。
	// 原来还判断 prevTicks==0，但刚启动的进程 utime+stime 就是 0，会被误判成"没采过"，
	// 导致 CPU 使用率永远算不出来。
	if prevAt.IsZero() {
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

// hostCPUPercent 用 /proc/stat 第一行（"cpu " 汇总行）算**整机** CPU 占用（100 = 全部核）。
//
// 为什么必须用它：扫描时真正的 CPU 消耗大头常常不在本进程——同机的 AI 识别会拉起
// chromium 渲染页面，PostgreSQL 在写结果，web 在渲染列表。只看本进程时这些全看不见，
// 调速器就会一路加并发，直到 web 打不开（v0.0.50 实测事故）。
// 在容器里 /proc/stat 默认就是宿主机视角（除非挂了 lxcfs），正好覆盖兄弟容器；
// 读不到（Windows / 受限容器）时返回 ok=false，闭环退化为本进程 CPU + fd 两路。
func hostCPUPercent(prevIdle, prevTotal uint64) (busy float64, idle, total uint64, ok bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, prevIdle, prevTotal, false
	}
	line := ""
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "cpu ") {
			line = l
			break
		}
	}
	if line == "" {
		return 0, prevIdle, prevTotal, false
	}
	f := strings.Fields(line)[1:]
	if len(f) < 4 {
		return 0, prevIdle, prevTotal, false
	}
	var sum uint64
	var idleTicks uint64
	for i, s := range f {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0, prevIdle, prevTotal, false
		}
		sum += v
		if i == 3 || i == 4 { // idle + iowait：等待 IO 的 CPU 也算空闲
			idleTicks += v
		}
	}
	if sum == 0 {
		return 0, prevIdle, prevTotal, false
	}
	if prevTotal == 0 || sum <= prevTotal {
		// 首次采样：只打点
		return 0, idleTicks, sum, false
	}
	dTotal := sum - prevTotal
	dIdle := idleTicks - prevIdle
	if dTotal == 0 || dIdle > dTotal {
		return 0, idleTicks, sum, false
	}
	return float64(dTotal-dIdle) / float64(dTotal) * 100, idleTicks, sum, true
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

// autoUnitConcurrency 由"想要的在途探针数"和"单个单元的端口数"反推该并行几个单元。
//
// 为什么需要：limit 决定流量，K 决定有多少单元同时在跑，两者是乘起来的关系
// （总在途 ≈ min(K × 单元端口数, limit)）。top100 的单元只有 100 个探针，
// K=6 就把在途钉死在 600 —— 此时把 limit 调到 1400 也毫无意义。
// 反过来，全端口扫描（65535/单元）不能给大 K，否则一个单元就 spawn 6 万个探针 goroutine。
//
// 返回 0 表示"不改"（未启用调速或读数不可用）。
func autoUnitConcurrency(probesPerUnit int64) int {
	if autoPilot == nil || probesPerUnit <= 0 {
		return 0
	}
	inflight := autoPilot.recommended()
	k := (inflight + int(probesPerUnit) - 1) / int(probesPerUnit)
	hard := queueUnitConcurrency() // 节点配置的 K 作为下限：不要比人工配的更保守
	if k < hard {
		k = hard
	}
	if k > 32 { // 服务端批量领取上限
		k = 32
	}
	if k < 1 {
		k = 1
	}
	return k
}
