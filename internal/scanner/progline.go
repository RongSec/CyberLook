package scanner

// 扫描进度行解析（唯一实现）
//
// 为什么要集中：这条行的消费者有四个 —— 节点把进度写进心跳、服务端按单元记 probes_done、
// 任务日志高亮、AI 诊断读日志。以前正则在 cmd/agent 与 internal/service 各写一套，
// 改一次文案要同步改两处，漏一处就是"进度永远 0%"这种静默故障（真踩过）。
//
// 文案来源是引擎的 portscan/report.go，两个入口（CLI / 平台队列单元）共用同一份格式：
//
//	[扫描] 进行中 45%（29490/65535 探针，已用 12s，剩余 14s）｜2450 探针/秒｜开放 6 个，识别 6 个
//	[扫描] 完成 65535/65535 探针，耗时 26s，平均 2520 探针/秒，开放 6 个，识别 6 个
//
// 同时兼容 v0.0.35 及更早的两代旧文案（节点自升级窗口里会有旧版节点还在打旧行）：
//
//	[自研引擎] 扫描中 45%（4500/10000 探针，已用 3s，剩余 6s）｜613 探针/秒｜开放端口 2 个，已识别 2 个
//	[自研引擎] 进度 45% (4500/10000) [3s:6s] 613探针/秒 开放 2 已识别 2

import (
	"regexp"
	"strconv"
)

// ProgInfo 一行进度里能提取到的全部信息。Has* 表示该字段在本行是否出现，
// 调用方按需取用（不要用零值判断"没出现"——百分比 0 是合法值）。
type ProgInfo struct {
	Done, Total int64
	Percent     int
	PPS         float64
	Open, Ident int64
	EtaSec      int
	IsDone      bool

	HasPair, HasPct, HasPPS, HasOpen, HasEta bool
}

var (
	progPairRe = regexp.MustCompile(`(\d+)\s*/\s*(\d+)\s*探针|\((\d+)\s*/\s*(\d+)\)`)
	progPctRe  = regexp.MustCompile(`(?:进行中|扫描中|进度)\s*(\d{1,3})\s*%`)
	progPPSRe  = regexp.MustCompile(`([\d.]+)\s*探针/秒`)
	progOpenRe = regexp.MustCompile(`开放(?:端口)?\s*(\d+)\s*个?[，,]?\s*(?:已识别|识别服务|识别)\s*(\d+)\s*个?`)
	// ETA 两种写法：新「剩余 14s」、旧「[3s:6s]」的冒号后半段
	progEtaNewRe = regexp.MustCompile(`剩余\s*([0-9]+h)?([0-9]+m)?([0-9.]+s)?`)
	progEtaOldRe = regexp.MustCompile(`\[[0-9hms]+:([0-9hms]+)\]`)
	progDurRe    = regexp.MustCompile(`(\d+)([hms])`)
	progDoneRe   = regexp.MustCompile(`\[扫描\]\s*完成|扫描完成`)
)

// ParseProgress 解析一行扫描日志。ok=false 表示这行不是进度/完成行。
func ParseProgress(line string) (ProgInfo, bool) {
	var p ProgInfo

	if m := progPairRe.FindStringSubmatch(line); len(m) > 0 {
		pick := func(i int) int64 {
			if i < len(m) && m[i] != "" {
				v, _ := strconv.ParseInt(m[i], 10, 64)
				return v
			}
			return 0
		}
		if m[1] != "" {
			p.Done, p.Total = pick(1), pick(2)
		} else {
			p.Done, p.Total = pick(3), pick(4)
		}
		p.HasPair = p.Total > 0
	}
	if m := progPctRe.FindStringSubmatch(line); len(m) == 2 {
		if v, err := strconv.Atoi(m[1]); err == nil && v <= 100 {
			p.Percent, p.HasPct = v, true
		}
	}
	if m := progPPSRe.FindStringSubmatch(line); len(m) == 2 {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			p.PPS, p.HasPPS = v, true
		}
	}
	if m := progOpenRe.FindStringSubmatch(line); len(m) == 3 {
		a, _ := strconv.ParseInt(m[1], 10, 64)
		b, _ := strconv.ParseInt(m[2], 10, 64)
		p.Open, p.Ident, p.HasOpen = a, b, true
	}
	if progDoneRe.MatchString(line) {
		p.IsDone = true
		p.Percent, p.HasPct = 100, true
	}
	// ETA：先试新格式，再试旧格式
	if m := progEtaNewRe.FindStringSubmatch(line); len(m) == 4 && (m[1] != "" || m[2] != "" || m[3] != "") {
		sec := 0
		for _, part := range []struct {
			s string
			f int
		}{{m[1], 3600}, {m[2], 60}, {m[3], 1}} {
			if part.s == "" {
				continue
			}
			num := part.s[:len(part.s)-1]
			if v, err := strconv.ParseFloat(num, 64); err == nil {
				sec += int(v) * part.f
			}
		}
		if sec > 0 {
			p.EtaSec, p.HasEta = sec, true
		}
	}
	if !p.HasEta {
		if m := progEtaOldRe.FindStringSubmatch(line); len(m) == 2 {
			if sec := parseDurSec(m[1]); sec > 0 {
				p.EtaSec, p.HasEta = sec, true
			}
		}
	}

	ok := p.HasPair || p.HasPct || p.HasOpen || p.IsDone
	return p, ok
}

// parseDurSec 把 "2m36s" / "45s" / "1h2m" 解析成秒。
func parseDurSec(s string) int {
	total := 0
	for _, m := range progDurRe.FindAllStringSubmatch(s, -1) {
		v, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "h":
			total += v * 3600
		case "m":
			total += v * 60
		case "s":
			total += v
		}
	}
	return total
}
