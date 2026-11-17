package service

import (
	"testing"
	"time"
)

// TestSampleAliveETA_DropsICMPBurst 存活探测的 ETA 采样必须丢掉 ICMP 爆发窗口。
//
// 这是回归测试：老实现用 "probe_offset ÷ 已用时间" 算速率，
// 而探测进度是 ICMP（两秒推完一半）与 TCP（200 IP/秒）两段归一化后的值，
// 于是 /16 这种目标一开探就显示"剩余 2 秒"，真实耗时 7 分 36 秒。
func TestSampleAliveETA_DropsICMPBurst(t *testing.T) {
	now := time.Now()
	st := &etaState{}

	// 第 1 个 tick：建立基线（probe_offset = 0）
	sampleAliveETA(st, 0, now)

	// 第 2 个 tick：ICMP 爆发，10 秒内推进 47,000 个 IP（4,700 IP/秒）→ 必须被丢弃
	now = now.Add(10 * time.Second)
	sampleAliveETA(st, 47000, now)
	if st.pSamples != 0 {
		t.Fatalf("ICMP 爆发窗口不应计入样本，实际 samples=%d", st.pSamples)
	}
	if st.pEwma != 0 {
		t.Fatalf("ICMP 爆发窗口不应产生速率，实际 ewma=%.1f", st.pEwma)
	}

	// 随后进入 TCP 阶段：每 10 秒推进约 2,000 台 → 速率 ~200 IP/秒
	for i := 1; i <= 3; i++ {
		now = now.Add(10 * time.Second)
		sampleAliveETA(st, 47000+i*2000, now)
	}
	if st.pSamples < 2 {
		t.Fatalf("TCP 阶段应累计样本，实际 samples=%d", st.pSamples)
	}
	if st.pEwma < 100 || st.pEwma > 400 {
		t.Fatalf("稳态速率应接近 200 IP/秒，实际 %.1f", st.pEwma)
	}
}

// TestSampleAliveETA_StalledProgress 进度不动时除以"距上次变化的时间"，
// 速率只会变小、不会凭空变大——不能靠它给出一个越来越乐观的剩余时间。
func TestSampleAliveETA_StalledProgress(t *testing.T) {
	now := time.Now()
	st := &etaState{}
	sampleAliveETA(st, 0, now)
	now = now.Add(10 * time.Second)
	sampleAliveETA(st, 2000, now) // 200 IP/秒
	first := st.pEwma

	// 冻结 40 秒（抽样复核阶段），偏移不动（delta=0，不产生新样本）
	now = now.Add(40 * time.Second)
	sampleAliveETA(st, 2000, now)
	if st.pEwma > first+1 {
		t.Fatalf("冻结期间速率不应上升：before=%.1f after=%.1f", first, st.pEwma)
	}

	// 恢复推进：delta=2000 / 40s = 50 IP/秒（一批被卡住后的真实均速）
	now = now.Add(40 * time.Second)
	sampleAliveETA(st, 4000, now)
	if st.pEwma >= first {
		t.Fatalf("卡顿后的平均速率应低于卡顿前，before=%.1f after=%.1f", first, st.pEwma)
	}
}
