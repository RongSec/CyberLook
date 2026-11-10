package scanner

// 自研扫描引擎接入点：SCAN_ENGINE=builtin 时完全不使用 TscanPlus。
//
// 这里只做「适配」：把 internal/portscan 的开放端口 + internal/svcid 的识别结果
// 翻译成现有 ScanResult，后面的 ingestService / 合规 / 快照链路一行都不用改。

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"asset-platform/internal/portscan"
	"asset-platform/internal/svcid"
	"asset-platform/internal/target"
)

// BuiltinRulesDir 规则目录（可用 SCAN_RULES_DIR 覆盖）
func BuiltinRulesDir() string {
	if p := os.Getenv("SCAN_RULES_DIR"); p != "" {
		return p
	}
	return "data/fingerprint"
}

// RunBuiltinPortScan 用自研引擎跑一轮扫描，返回与 TscanPlus 同结构的结果
func RunBuiltinPortScan(targets, excludes []string, opts ScanOptions, logFn LogFunc, onResult func([]ScanResult)) ([]ScanResult, error) {
	ips := target.Resolve(targets, excludes)
	if len(ips) == 0 {
		return nil, fmt.Errorf("目标展开后为空")
	}

	portSpec := opts.Ports
	if portSpec == "" {
		portSpec = "top100" // 与 TscanClient 默认 Top100 对齐
	}
	ports, err := portscan.ParsePorts(portSpec)
	if err != nil {
		return nil, err
	}

	conc := opts.Thread
	if conc <= 0 || conc > 8192 {
		conc = 1500
	}
	// 超时单位兼容：agent 侧传毫秒（task.Timeout*1000），本地任务路径历史上按秒传。
	// <100 视为秒，自动换算；这样两条路径都不用改调用方。
	timeoutMs := opts.Timeout
	if timeoutMs > 0 && timeoutMs < 100 {
		timeoutMs *= 1000
	}
	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}

	// ===== ScanOptions 语义映射（对齐 TscanPlus 用法）=====
	// Modules: "port" 只扫端口；"port,url" 或空 → 额外做 HTTP/服务识别
	// Finger:  "tiny" 只做规则匹配；"all"/空 → 全量（含主动探针）
	// NoPing:  自研是 connect 扫描，天然不做 ICMP 预探测（等价 -np）
	// NoSmart: 自研不做端口启发式裁剪，按给定端口全扫（等价 -nosmart）
	modules := strings.ToLower(strings.TrimSpace(opts.Modules))
	wantIdent := modules == "" || strings.Contains(modules, "url") || strings.Contains(modules, "finger")
	fullFinger := opts.Finger == "" || strings.Contains(strings.ToLower(opts.Finger), "all")
	if opts.NoPing {
		logf(logFn, "[自研引擎] NoPing=true：connect 扫描不做 ICMP 预探测（语义等价 -np）")
	}
	if opts.NoSmart {
		logf(logFn, "[自研引擎] NoSmart=true：按给定端口全扫，不做启发式裁剪（语义等价 -nosmart）")
	}
	logf(logFn, fmt.Sprintf("[自研引擎] 目标 %d 个 × 端口 %d 个 = %d 探针，并发 %d，超时 %s，识别=%v，全量指纹=%v",
		len(ips), len(ports), len(ips)*len(ports), conc, timeout, wantIdent, fullFinger))

	ctx := context.Background()
	t0 := time.Now()
	opens, st, err := portscan.Scan(ctx, ips, ports, portscan.Options{
		Concurrency: conc,
		Timeout:     timeout,
		OnProgress: func(done, total, open int64) {
			logf(logFn, fmt.Sprintf("[自研引擎] 进度 %d/%d 开放 %d（%.0f 探针/秒）",
				done, total, open, float64(done)/max1(time.Since(t0).Seconds())))
		},
	})
	if err != nil {
		return nil, err
	}
	logf(logFn, fmt.Sprintf("[自研引擎] 扫描完成：%d 探针 / %s / %.0f 探针每秒，开放 %d",
		st.Probes, st.Elapsed.Round(time.Second), st.PPS(), st.Open))

	// 规则：全量载入 FingerprintHub（web 3450 + service 11938）
	var rs *svcid.RuleSet
	var probes *svcid.ProbeIndex
	if r, e := svcid.Load(BuiltinRulesDir()); e == nil {
		rs = r
		probes = r.BuildServiceProbes()
		logf(logFn, fmt.Sprintf("[自研引擎] 指纹规则载入：web %d / service %d / 主动探针端口组 %d",
			len(rs.Web), len(rs.Service), probes.PortCount()))
	} else {
		logf(logFn, "[自研引擎] 规则载入失败，仅做端口扫描："+e.Error())
	}

	// 识别参数：Finger=tiny 时不做主动探针（减少发包）；Modules=port 时不做 HTTP 抓取
	identOpt := svcid.IdentOptions{Rules: rs, Probes: probes}
	if !fullFinger {
		identOpt.Probes = nil // tiny：只用规则/被动探针，不发主动探针
	}
	if !wantIdent {
		identOpt.Rules = nil
		identOpt.Probes = nil
	}

	results := make([]ScanResult, 0, len(opens))
	for _, r := range opens {
		sv := svcid.Identify(ctx, r.IP, r.Port, identOpt)
		proto := strings.ToUpper(sv.Protocol)
		if proto == "" {
			proto = "TCP"
		}
		// ===== 字段映射到 TscanPlus 的 ScanResult 语义 =====
		// Title        ← 页面 <title>（无页面时用产品名，供 MergeIdentification 合并）
		// Fingerprint  ← 技术栈（产品+版本），供 MergeFingerprint 合并
		// Banner       ← 协议 banner 原文
		// Response     ← HTTP 响应体（ingestService 用它算页面 SimHash / content_changed）
		// StatusCode   ← HTTP 状态码（变更检测）
		// ContentLength← HTTP 响应体长度（变更检测）
		// OS           ← SMB/RDP 握手拿到的系统信息
		// Mid          ← 中间件/产品（tscan 的 Mid 语义）
		title := sv.Title
		if title == "" {
			title = sv.Product
		}
		fp := sv.Fingerprint
		if fp == "" {
			fp = strings.Join(sv.Evidence, ",")
		}
		results = append(results, ScanResult{
			IP:            r.IP,
			Port:          r.Port,
			Protocol:      proto,
			Service:       sv.Protocol,
			Banner:        sv.Banner,
			Title:         title,
			Fingerprint:   fp,
			StatusCode:    sv.StatusCode,
			ContentLength: sv.BodyLen,
			Response:      sv.Body,
			OS:            sv.OS,
			Mid:           sv.Product,
		})
	}
	if onResult != nil && len(results) > 0 {
		onResult(results)
	}
	return results, nil
}

// useEngine 按任务级 engine 字段路由；空则用全局默认
func useEngine(engine string) bool {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "tscan", "legacy":
		return false
	case "builtin", "1", "true":
		return true
	}
	return UseBuiltinEngine()
}

// UseBuiltinEngine 是否启用自研引擎。
// v0.0.25 起**默认就是自研引擎**；显式设置 SCAN_ENGINE=tscan 才回退到 TscanPlus。
func UseBuiltinEngine() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SCAN_ENGINE")))
	if v == "tscan" || v == "legacy" {
		return false
	}
	return true
}

func max1(v float64) float64 {
	if v < 1 {
		return 1
	}
	return v
}

// portKey 便于日志里展示 ip:port
func portKey(ip string, port int) string {
	return ip + ":" + strconv.Itoa(port)
}
