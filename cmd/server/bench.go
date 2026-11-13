package main

// 端口扫描 + 指纹识别压测/对拍工具（替代 TscanPlus 的自研链路）
//
// 用法（容器内）：
//   asset-server bench-scan --targets /root/bench/targets.txt --ports 1-65535 \
//       --concurrency 4096 --timeout 500ms --rules data/fingerprint --identify \
//       --out /root/bench/ours.json

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RongSec/CyberLook-Scan/portscan"
	"github.com/RongSec/CyberLook-Scan/svcid"
)

type benchOut struct {
	Rules      map[string]int      `json:"rules"`
	RulesLoadMs int64              `json:"rulesLoadMs"`
	Targets    int                 `json:"targets"`
	Ports      int                 `json:"ports"`
	Probes     int64               `json:"probes"`
	Open       int64               `json:"open"`
	Closed     int64               `json:"closed"`
	Filtered   int64               `json:"filtered"`
	Errors     int64               `json:"errors"`
	ElapsedSec float64             `json:"elapsedSec"`
	PPS        float64             `json:"pps"`
	OpenPorts  []string            `json:"openPorts"` // ip:port
	Services   []svcid.Service     `json:"services,omitempty"`
	ProtoStat  map[string]int      `json:"protoStat,omitempty"`
	ProductTop map[string]int      `json:"productTop,omitempty"`
	WithProduct int                `json:"withProduct"`
	WithVersion int                `json:"withVersion"`
	WithEvidence int               `json:"withEvidence"` // 有证据的识别（不含端口推测）
	IdentSec   float64             `json:"identSec,omitempty"`
}

func runBenchScan(args []string) {
	fs := flag.NewFlagSet("bench-scan", flag.ExitOnError)
	targetsArg := fs.String("targets", "", "目标列表（逗号分隔或文件路径，每行一个/IP 段）")
	portsSpec := fs.String("ports", "1-65535", "端口：1-65535 / 22,80 / top1000")
	conc := fs.Int("concurrency", 4096, "并发连接数")
	timeoutMs := fs.Int("timeout", 500, "单端口超时（毫秒）")
	rulesDir := fs.String("rules", "data/fingerprint", "FingerprintHub 规则目录")
	doIdent := fs.Bool("identify", true, "是否做服务识别")
	identConc := fs.Int("ident-conc", 32, "识别并发")
	outPath := fs.String("out", "", "结果 JSON 输出路径")
	reident := fs.String("reidentify", "", "只重跑识别：读取既有 ours.json 的 openPorts，不重新扫描")
	_ = fs.Parse(args)
	if *reident != "" {
		runReidentify(*reident, *rulesDir, *outPath, *identConc)
		return
	}

	// 目标解析
	var targets []string
	if st, err := os.Stat(*targetsArg); err == nil && !st.IsDir() {
		b, _ := os.ReadFile(*targetsArg)
		for _, line := range strings.FieldsFunc(string(b), func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
			line = strings.TrimSpace(line)
			if line != "" {
				targets = append(targets, line)
			}
		}
	} else {
		for _, t := range strings.FieldsFunc(*targetsArg, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
			if t = strings.TrimSpace(t); t != "" {
				targets = append(targets, t)
			}
		}
	}
	if len(targets) == 0 {
		fmt.Println("[bench] 没有目标，用 --targets 指定")
		return
	}
	ports, err := portscan.ParsePorts(*portsSpec)
	if err != nil {
		fmt.Printf("[bench] 端口解析失败: %v\n", err)
		return
	}

	// 规则加载
	var rs *svcid.RuleSet
	loadStart := time.Now()
	if *doIdent {
		rs, err = svcid.Load(*rulesDir)
		if err != nil {
			fmt.Printf("[bench] 规则加载失败: %v\n", err)
			return
		}
	}
	loadMs := time.Since(loadStart).Milliseconds()
	var probeIdx *svcid.ProbeIndex
	if rs != nil {
		probeIdx = rs.BuildServiceProbes()
		fmt.Printf("[bench] service 主动探针索引：%d 个端口组\n", probeIdx.PortCount())
	}

	fmt.Printf("[bench] 目标 %d 个 × 端口 %d 个 = %d 探针；并发 %d，超时 %dms\n",
		len(targets), len(ports), len(targets)*len(ports), *conc, *timeoutMs)
	if rs != nil {
		fmt.Printf("[bench] 规则加载 %dms：%v\n", loadMs, rs.Stats())
	}

	ctx := context.Background()
	lastPrint := time.Now()
	res, st, err := portscan.Scan(ctx, targets, ports, portscan.Options{
		Concurrency: *conc,
		Timeout:     time.Duration(*timeoutMs) * time.Millisecond,
		OnProgress: func(done, total, open int64) {
			el := time.Since(lastPrint)
			fmt.Printf("[进度] %d/%d（%.1f%%）开放 %d 已用 %s\n",
				done, total, float64(done)*100/float64(total), open, el.Round(time.Second))
		},
	})
	if err != nil && len(res) == 0 {
		fmt.Printf("[bench] 扫描失败: %v\n", err)
		return
	}

	out := benchOut{
		Targets: len(targets), Ports: len(ports),
		Probes: st.Probes, Open: st.Open, Closed: st.Closed, Filtered: st.Filtered, Errors: st.Errors,
		ElapsedSec: st.Elapsed.Seconds(), PPS: st.PPS(),
		RulesLoadMs: loadMs,
	}
	if rs != nil {
		out.Rules = rs.Stats()
	}
	for _, r := range res {
		out.OpenPorts = append(out.OpenPorts, fmt.Sprintf("%s:%d", r.IP, r.Port))
	}

	fmt.Printf("\n===== 扫描结果 =====\n")
	fmt.Printf("探针 %d，用时 %s，速率 %.0f 探针/秒\n", st.Probes, st.Elapsed.Round(time.Second), st.PPS())
	fmt.Printf("开放 %d，关闭 %d（RST），过滤/超时 %d，错误 %d\n", st.Open, st.Closed, st.Filtered, st.Errors)
	fmt.Printf("开放端口：%v\n", out.OpenPorts)

	// 识别
	if rs != nil && len(res) > 0 {
		t0 := time.Now()
		out.Services = make([]svcid.Service, 0, len(res))
		out.ProtoStat = map[string]int{}
		out.ProductTop = map[string]int{}
		var mu sync.Mutex
		sem := make(chan struct{}, *identConc)
		var wg sync.WaitGroup
		for _, r := range res {
			wg.Add(1)
			sem <- struct{}{}
			go func(ip string, port int) {
				defer wg.Done()
				defer func() { <-sem }()
				sv := svcid.Identify(ctx, ip, port, svcid.IdentOptions{Rules: rs, Probes: probeIdx})
				mu.Lock()
				out.Services = append(out.Services, sv)
				out.ProtoStat[sv.Protocol]++
				if sv.Product != "" {
					out.ProductTop[sv.Product]++
					out.WithProduct++
					if sv.Source != "port-hint" {
						out.WithEvidence++
					}
				}
				if sv.Version != "" {
					out.WithVersion++
				}
				mu.Unlock()
			}(r.IP, r.Port)
		}
		wg.Wait()
		out.IdentSec = time.Since(t0).Seconds()
		sort.Slice(out.Services, func(i, j int) bool {
			if out.Services[i].IP == out.Services[j].IP {
				return out.Services[i].Port < out.Services[j].Port
			}
			return out.Services[i].IP < out.Services[j].IP
		})

		fmt.Printf("\n===== 识别结果（%d 个开放端口，用时 %s）=====\n", len(out.Services), time.Since(t0).Round(time.Millisecond))
		fmt.Printf("有产品名 %d（%.0f%%），其中**有证据** %d（%.0f%%），有版本 %d（%.0f%%）\n",
			out.WithProduct, pctf(out.WithProduct, len(out.Services)),
			out.WithEvidence, pctf(out.WithEvidence, len(out.Services)),
			out.WithVersion, pctf(out.WithVersion, len(out.Services)))
		fmt.Printf("协议分布：%v\n", out.ProtoStat)
		type kv struct {
			K string
			V int
		}
		var tops []kv
		for k, v := range out.ProductTop {
			tops = append(tops, kv{k, v})
		}
		sort.Slice(tops, func(i, j int) bool { return tops[i].V > tops[j].V })
		fmt.Println("产品 Top20：")
		for i, t := range tops {
			if i >= 20 {
				break
			}
			fmt.Printf("  %-32s %d\n", t.K, t.V)
		}
	}

	if *outPath != "" {
		if b, e := json.MarshalIndent(out, "", "  "); e == nil {
			_ = os.WriteFile(*outPath, b, 0o644)
			fmt.Printf("\n结果已写入 %s\n", *outPath)
		}
	}
}

func pctf(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}

// runReidentify 复用已有扫描结果，只重跑识别（用于调优识别层而不重新扫 65 万端口）
func runReidentify(src, rulesDir, outPath string, conc int) {
	b, err := os.ReadFile(src)
	if err != nil {
		fmt.Printf("[reident] 读取失败: %v\n", err)
		return
	}
	var old benchOut
	if err := json.Unmarshal(b, &old); err != nil {
		fmt.Printf("[reident] 解析失败: %v\n", err)
		return
	}
	rs, err := svcid.Load(rulesDir)
	if err != nil {
		fmt.Printf("[reident] 规则加载失败: %v\n", err)
		return
	}
	probeIdx := rs.BuildServiceProbes()
	fmt.Printf("[reident] service 主动探针索引：%d 个端口组\n", probeIdx.PortCount())
	out := old
	out.Services = nil
	out.ProtoStat = map[string]int{}
	out.ProductTop = map[string]int{}
	out.WithProduct, out.WithVersion = 0, 0
	var mu sync.Mutex
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	ctx := context.Background()
	t0 := time.Now()
	for _, k := range old.OpenPorts {
		parts := strings.Split(k, ":")
		if len(parts) != 2 {
			continue
		}
		ip := parts[0]
		var port int
		_, _ = fmt.Sscanf(parts[1], "%d", &port)
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			sv := svcid.Identify(ctx, ip, port, svcid.IdentOptions{Rules: rs})
			mu.Lock()
			out.Services = append(out.Services, sv)
			out.ProtoStat[sv.Protocol]++
			if sv.Product != "" {
				out.ProductTop[sv.Product]++
				out.WithProduct++
				if sv.Source != "port-hint" {
					out.WithEvidence++
				}
			}
			if sv.Version != "" {
				out.WithVersion++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	out.IdentSec = time.Since(t0).Seconds()
	fmt.Printf("[reident] %d 个端口，用时 %s，有产品 %d（%.0f%%），**有证据** %d（%.0f%%），有版本 %d（%.0f%%）\n",
		len(out.Services), time.Since(t0).Round(time.Millisecond), out.WithProduct,
		pctf(out.WithProduct, len(out.Services)), out.WithEvidence,
		pctf(out.WithEvidence, len(out.Services)), out.WithVersion, pctf(out.WithVersion, len(out.Services)))
	fmt.Printf("协议：%v\n", out.ProtoStat)
	type kv struct { K string; V int }
	var tops []kv
	for k, v := range out.ProductTop { tops = append(tops, kv{k, v}) }
	sort.Slice(tops, func(i, j int) bool { return tops[i].V > tops[j].V })
	for i, t := range tops { if i >= 20 { break }; fmt.Printf("  %-40s %d\n", t.K, t.V) }
	if outPath != "" {
		if b, e := json.MarshalIndent(out, "", "  "); e == nil {
			_ = os.WriteFile(outPath, b, 0o644)
			fmt.Printf("已写入 %s\n", outPath)
		}
	}
}
