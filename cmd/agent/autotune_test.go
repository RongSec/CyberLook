package main

import (
	"testing"
	"time"

	"asset-platform/internal/scanner"
)

// 智能调速的决策逻辑必须是确定的、可测的 —— 它直接决定节点打多快，
// 一个方向写反就会把 8GB 机器推到 OOM，或者把吞吐压回谷底。
func newTestTuner(base int) *autoTuner {
	return &autoTuner{cpuN: 4, memMB: 8192, fdBudget: base, base: base, cur: base}
}

// ① 出现本地资源错误（EMFILE）必须立刻大幅回退：这是硬墙，不是慢。
func TestAutoTuneShrinkOnLocalResourceErr(t *testing.T) {
	tt := newTestTuner(1000)
	why := tt.decide(20000, 3, 10, true)
	if tt.cur != 600 {
		t.Fatalf("期望 1000→600（×0.6），实际 %d（%s）", tt.cur, why)
	}
	if why == "" {
		t.Fatal("必须给出调整原因")
	}
}

// ② CPU 首次冲高只观察，连续两次才降 —— 避免被单个窗口的尖峰带着抖。
func TestAutoTuneCPUHighNeedsTwoWindows(t *testing.T) {
	tt := newTestTuner(1000)
	tt.decide(20000, 0, 95, true) // 首窗口只观察
	if tt.cur != 1000 {
		t.Fatalf("首个高 CPU 窗口不应调整，实际 %d", tt.cur)
	}
	tt.decide(20000, 0, 95, true) // 第二窗口才降
	if tt.cur != 800 {
		t.Fatalf("连续两个高 CPU 窗口应降到 800，实际 %d", tt.cur)
	}
}

// ③ CPU 有余量时小幅上调，但绝不越过天花板。
func TestAutoTuneGrowCappedByBase(t *testing.T) {
	tt := newTestTuner(1000)
	tt.cur = 950
	tt.decide(20000, 0, 20, true)
	if tt.cur != 1000 {
		t.Fatalf("应上调但被 base 截断到 1000，实际 %d", tt.cur)
	}
	// 连续上调也不能超过 base
	for i := 0; i < 10; i++ {
		tt.decide(20000, 0, 10, true)
	}
	if tt.cur != 1000 {
		t.Fatalf("天花板失效，cur=%d", tt.cur)
	}
}

// ④ 空闲窗口（暂停/没任务）不许凭"CPU 低"空推。
func TestAutoTuneIdleDoesNotGrow(t *testing.T) {
	tt := newTestTuner(1000)
	tt.cur = 500
	for i := 0; i < 5; i++ {
		tt.decide(0, 0, 1, true)
	}
	if tt.cur != 500 {
		t.Fatalf("空闲窗口不应调整，实际 %d", tt.cur)
	}
}

// ⑤ 退到底也不能低于下限（base/4，且不低于 autoMinInflight）。
func TestAutoTuneFloor(t *testing.T) {
	tt := newTestTuner(1000)
	for i := 0; i < 20; i++ {
		tt.decide(20000, 5, 99, true)
	}
	if tt.cur != 250 {
		t.Fatalf("期望兜底在 250（base/4），实际 %d", tt.cur)
	}

	small := newTestTuner(200) // base/4=50 < autoMinInflight(100) → 下限取 100
	for i := 0; i < 20; i++ {
		small.decide(20000, 5, 99, true)
	}
	if small.cur != autoMinInflight {
		t.Fatalf("小机器下限应为 %d，实际 %d", autoMinInflight, small.cur)
	}
}

// ⑥ 读不到 CPU（Windows / 非 Linux）时闭环退化为"只看 fd"，不能乱涨。
func TestAutoTuneNoCPUSignal(t *testing.T) {
	tt := newTestTuner(1000)
	tt.cur = 700
	tt.decide(20000, 0, 0, false)
	if tt.cur != 700 {
		t.Fatalf("无 CPU 信号时应维持，实际 %d", tt.cur)
	}
}

// ⑦ 天花板计算：fd / CPU / 内存 三者取最小，且钳在 [200, 4000]。
func TestBaseComputation(t *testing.T) {
	t.Setenv("SCAN_INFLIGHT_MAX", "")
	cases := []struct {
		name    string
		fd      int
		wantMin int
	}{
		{"fd 是瓶颈", 900, 675}, // cpuN=4 → 1400，mem=8192→24576，取 900 的 3/4
	}
	for _, c := range cases {
		tt := newAutoTuner(c.fd)
		if tt.base > c.fd {
			t.Fatalf("%s: base=%d 超过 fd 预算 %d", c.name, tt.base, c.fd)
		}
		if tt.cur != tt.base*3/4 {
			t.Fatalf("%s: 起步应为 base*3/4，实际 cur=%d base=%d", c.name, tt.cur, tt.base)
		}
		if tt.cur < c.wantMin {
			t.Fatalf("%s: 起步 %d 低于预期 %d", c.name, tt.cur, c.wantMin)
		}
	}
}

// ⑧ 人工调控让位期的口径只有一处定义（scanner 包），agent 侧不许自带副本。
func TestAutoLimitManualHoldDefinedOnce(t *testing.T) {
	if scanner.AutoManualHold <= 0 {
		t.Fatalf("scanner.AutoManualHold 未定义，自动挡会抢人工调控的方向盘")
	}
	if scanner.AutoManualHold < time.Minute {
		t.Fatalf("让位期 %v 太短，人调完十秒就被推回去等于没有", scanner.AutoManualHold)
	}
}

// ⑨ K 必须随"在途目标 ÷ 单元端口数"变化，但不能把全端口扫描推出天文数字。
func TestAutoUnitConcurrency(t *testing.T) {
	old := autoPilot
	defer func() { autoPilot = old }()
	autoPilot = &autoTuner{cpuN: 4, memMB: 8000, fdBudget: 1500, base: 1400, cur: 1050}

	// top100：1050 在途 ÷ 100 端口 → 11 个单元并行（旧实现固定 6，在途被钉死在 600）
	if k := autoUnitConcurrency(100); k != 11 {
		t.Fatalf("100 端口/单元期望 K=11，实际 %d", k)
	}
	// 全端口：1050 ÷ 65535 → 向上取整为 1，且不低于节点配置的 K（默认 6）
	if k := autoUnitConcurrency(65535); k < 1 || k > 32 {
		t.Fatalf("全端口 K 越界: %d", k)
	}
	// 读数不可用时不改
	if k := autoUnitConcurrency(0); k != 0 {
		t.Fatalf("端口数未知时应返回 0，实际 %d", k)
	}
	// 上限 32
	autoPilot.cur = 999999
	if k := autoUnitConcurrency(100); k != 32 {
		t.Fatalf("K 应被钳到 32，实际 %d", k)
	}
}
