package scanner

import "testing"

// TestParseProgressNewFormat 新文案（v0.0.36 起 CLI 与平台共用）
func TestParseProgressNewFormat(t *testing.T) {
	cases := []struct {
		line string
		done, total int64
		pct  int
		open, ident int64
		eta  int
		done_ bool
	}{
		{
			line: "[扫描] 进行中 45%（30000/65535 探针，已用 12s，剩余 14s）｜2458 探针/秒｜开放 6 个，识别 6 个",
			done: 30000, total: 65535, pct: 45, open: 6, ident: 6, eta: 14,
		},
		{
			line: "[扫描] 完成 65535/65535 探针，耗时 26s，平均 2520 探针/秒，开放 6 个，识别 6 个",
			done: 65535, total: 65535, pct: 100, open: 6, ident: 6, done_: true,
		},
	}
	for _, c := range cases {
		p, ok := ParseProgress(c.line)
		if !ok {
			t.Fatalf("应识别为进度行: %s", c.line)
		}
		if p.Done != c.done || p.Total != c.total {
			t.Errorf("探针 %d/%d 期望 %d/%d", p.Done, p.Total, c.done, c.total)
		}
		if p.Percent != c.pct {
			t.Errorf("百分比 %d 期望 %d", p.Percent, c.pct)
		}
		if p.Open != c.open || p.Ident != c.ident {
			t.Errorf("命中 %d/%d 期望 %d/%d", p.Open, p.Ident, c.open, c.ident)
		}
		if p.EtaSec != c.eta {
			t.Errorf("ETA %d 期望 %d", p.EtaSec, c.eta)
		}
		if p.IsDone != c.done_ {
			t.Errorf("IsDone %v 期望 %v", p.IsDone, c.done_)
		}
	}
}

// TestParseProgressLegacyFormat 旧文案（v0.0.35 及更早的节点还在打）
func TestParseProgressLegacyFormat(t *testing.T) {
	line := "[自研引擎] 扫描中 45%（4500/10000 探针，已用 3s，剩余 6s）｜613 探针/秒｜开放端口 2 个，已识别 2 个"
	p, ok := ParseProgress(line)
	if !ok {
		t.Fatal("旧格式应能解析")
	}
	if p.Done != 4500 || p.Total != 10000 || p.Percent != 45 || p.Open != 2 || p.Ident != 2 || p.EtaSec != 6 {
		t.Fatalf("解析结果不对: %+v", p)
	}

	// 更老的一代：括号 + [已用:剩余]
	old := "[自研引擎] 进度 21% (283200/1279104) [3m4s:10m23s] 613探针/秒 开放 2 已识别 2"
	p2, ok := ParseProgress(old)
	if !ok {
		t.Fatal("更老格式应能解析")
	}
	if p2.Done != 283200 || p2.Total != 1279104 || p2.Percent != 21 || p2.Open != 2 || p2.Ident != 2 {
		t.Fatalf("解析结果不对: %+v", p2)
	}
	if p2.EtaSec != 10*60+23 {
		t.Errorf("ETA %d 期望 %d", p2.EtaSec, 10*60+23)
	}
}

// TestParseProgressNonProgress 普通日志不能被误判为进度行
func TestParseProgressNonProgress(t *testing.T) {
	for _, line := range []string{
		"[扫描] 参数：并发 600，超时 1000ms，识别=关闭（识别并发 0，全量指纹 关闭）；本次 1 目标 × 65535 端口 = 65535 探针",
		"[队列] 198.18.188.85 开放 2 个端口（22,443）",
		"[存活探测] 完成：8/16384 存活（0.0%）；入队 8 个 IP × 100 端口",
	} {
		if p, ok := ParseProgress(line); ok {
			t.Errorf("误判为进度行: %s -> %+v", line, p)
		}
	}
}
