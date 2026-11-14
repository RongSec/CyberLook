package main

import "testing"

// 引擎进度行有两代文案，节点解析必须两代都认 —— 只认一代会让单元进度、
// ETA、开放端口数全部归零（界面上就是"进度一直 0%、剩余时间估算中"）。
func TestProgLineParsing(t *testing.T) {
	cases := []struct {
		name              string
		line              string
		done, total       int
		pps               float64
		open, ident       int
		pct             int
		expectPairParsed  bool
		expectOpenParsed  bool
	}{
		{
			name: "新文案",
			line: "[自研引擎] 扫描中 45%（4500/10000 探针，已用 3s，剩余 6s）｜613 探针/秒｜开放端口 2 个，已识别 2 个",
			done: 4500, total: 10000, pps: 613, open: 2, ident: 2, pct: 45,
			expectPairParsed: true, expectOpenParsed: true,
		},
		{
			name: "老文案",
			line: "[自研引擎] 进度 45% (4500/10000) [3s:6s] 613探针/秒 开放 2 已识别 2",
			done: 4500, total: 10000, pps: 613, open: 2, ident: 2, pct: 45,
			expectPairParsed: true, expectOpenParsed: true,
		},
		{
			name: "完成行",
			line: "[自研引擎] 扫描完成 100/100 探针，耗时 2s，平均 67 探针/秒，开放端口 2 个，识别服务 2 个",
			done: 100, total: 100, pps: 67, open: 2, ident: 2, pct: 100,
			expectPairParsed: true, expectOpenParsed: true,
		},
		{
			name: "日志行不应被当成进度",
			line: "[审计库] 落库路径 /app/config/config.db",
			expectPairParsed: false, expectOpenParsed: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, b, ok := progPair(c.line)
			if ok != c.expectPairParsed {
				t.Fatalf("progPair ok=%v 期望 %v", ok, c.expectPairParsed)
			}
			if ok && (a != c.done || b != c.total) {
				t.Errorf("progPair = %d/%d，期望 %d/%d", a, b, c.done, c.total)
			}
			oa, ob, ook := openPair(c.line)
			if ook != c.expectOpenParsed {
				t.Fatalf("openPair ok=%v 期望 %v", ook, c.expectOpenParsed)
			}
			if ook && (oa != c.open || ob != c.ident) {
				t.Errorf("openPair = %d/%d，期望 %d/%d", oa, ob, c.open, c.ident)
			}
			if !ok {
				return
			}
			if v, vok := ppsOf(c.line); !vok || v != c.pps {
				t.Errorf("ppsOf = %v(%v)，期望 %v", v, vok, c.pps)
			}
			if p, pok := pctOf(c.line); !pok || p != c.pct {
				t.Errorf("pctOf = %d(%v)，期望 %d", p, pok, c.pct)
			}
		})
	}
}
