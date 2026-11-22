package service

// 服务端 CPU 预算的确定性单测：这些数字直接决定"扫描时网页还能不能开"，
// 方向写反就会把探测压到爬不动、或者根本没压住。全部是纯函数，不依赖环境。

import (
	"runtime"
	"testing"
	"time"
)

// ① 预算口径：4 核留 1 核 → 75%；显式 0 → 92%（上限）；不许留到一核不剩。
func TestCPUBudgetPct(t *testing.T) {
	t.Setenv("CPU_RESERVE_CORES", "")
	if runtime.NumCPU() >= 4 {
		if b := cpuBudgetPct(); b != 75 && b != 92 {
			t.Fatalf("4 核预算应为 75%%（8 核为 75%%、上限 92%%），实际 %.0f", b)
		}
	}
	t.Setenv("CPU_RESERVE_CORES", "0")
	if b := cpuBudgetPct(); b != 92 {
		t.Fatalf("不留余量时预算应为上限 92%%，实际 %.0f", b)
	}
	t.Setenv("CPU_RESERVE_CORES", "99")
	cpuN := runtime.NumCPU()
	res := cpuReserveCoresServer(cpuN)
	if res != cpuN-1 {
		t.Fatalf("预留核数应被钳到 cpuN-1=%d，实际 %d", cpuN-1, res)
	}
}

// ② 让路时长：超预算 20 点停 1.2s，超预算停 400ms，预算内不停。
func TestProbePaceDelay(t *testing.T) {
	cases := []struct {
		busy, budget float64
		want         time.Duration
	}{
		{96, 75, 1200 * time.Millisecond},
		{78, 75, 400 * time.Millisecond},
		{75, 75, 0},
		{40, 75, 0},
	}
	for _, c := range cases {
		if got := probePaceDelay(c.busy, c.budget); got != c.want {
			t.Fatalf("busy=%.0f budget=%.0f 期望 %v，实际 %v", c.busy, c.budget, c.want, got)
		}
	}
}

// ③ 探测并发的边界：核数×220 钳在 [256,1200]，env 覆盖只接受合理值。
func TestProbeConcurrency(t *testing.T) {
	t.Setenv("ALIVE_PROBE_CONCURRENCY", "")
	n := probeConcurrency()
	if n < 256 || n > 1200 {
		t.Fatalf("默认并发越界: %d", n)
	}
	t.Setenv("ALIVE_PROBE_CONCURRENCY", "300")
	if got := probeConcurrency(); got != 300 {
		t.Fatalf("env 覆盖失败: %d", got)
	}
	t.Setenv("ALIVE_PROBE_CONCURRENCY", "5") // 太小，不接受
	if got := probeConcurrency(); got == 5 {
		t.Fatalf("非法并发应被忽略，实际 %d", got)
	}
}

// ④ 整机 CPU 采样：首次只打点，第二次起必须能算出占用（Linux）。
func TestHostBusyPctPriming(t *testing.T) {
	if _, _, ok := readStatCPUTimes(); !ok {
		t.Skip("非 Linux（读不到 /proc/stat），跳过")
	}
	if _, ok := hostBusyPct(); ok {
		t.Fatal("首次采样不应给出占用值（只有打点，没有差值）")
	}
	time.Sleep(120 * time.Millisecond)
	busy, ok := hostBusyPct()
	if !ok {
		t.Fatal("第二次采样必须能算出整机占用")
	}
	if busy < 0 || busy > 100 {
		t.Fatalf("整机占用越界: %.1f", busy)
	}
}
