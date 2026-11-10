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
		portSpec = "top1000"
	}
	ports, err := portscan.ParsePorts(portSpec)
	if err != nil {
		return nil, err
	}

	conc := opts.Thread
	if conc <= 0 || conc > 8192 {
		conc = 1500
	}
	timeout := time.Duration(opts.Timeout) * time.Millisecond
	if timeout <= 0 {
		timeout = 1000 * time.Millisecond
	}

	logf(logFn, fmt.Sprintf("[自研引擎] 目标 %d 个 × 端口 %d 个 = %d 探针，并发 %d，超时 %s",
		len(ips), len(ports), len(ips)*len(ports), conc, timeout))

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

	results := make([]ScanResult, 0, len(opens))
	for _, r := range opens {
		sv := svcid.Identify(ctx, r.IP, r.Port, svcid.IdentOptions{Rules: rs, Probes: probes})
		proto := strings.ToUpper(sv.Protocol)
		if proto == "" {
			proto = "TCP"
		}
		status, body := "", ""
		if sv.Banner != "" {
			body = sv.Banner
		}
		results = append(results, ScanResult{
			IP:          r.IP,
			Port:        r.Port,
			Protocol:    proto,
			Service:     sv.Protocol,
			Banner:      sv.Banner,
			Title:       sv.Product,
			Fingerprint: strings.Join(sv.Evidence, ","),
			StatusCode:  status,
			Mid:         sv.Source,
			ContentLength: len(body),
			Response:    body,
		})
	}
	if onResult != nil && len(results) > 0 {
		onResult(results)
	}
	return results, nil
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
