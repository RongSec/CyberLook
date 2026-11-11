package scanner

// 自研扫描引擎（v0.0.27 起平台唯一引擎，TscanPlus 已移除）。
//
// 执行结构 = **流水线并行**：
//
//	portscan.Scan（connect 扫描，固定 worker 池 = Options.Concurrency）
//	   │  OnOpen：发现开放端口即投递
//	   ▼
//	identCh ──► svcid.Identify 工作池（默认 32，可 IDENT_CONCURRENCY 覆盖）
//	                │ 识别完成
//	                ▼
//	              聚合器（独占 flushBuf）：攒满 200 条或 300ms 未发 → flush
//	                     flush = 累积 all + 审计库旁路写 + onResult(batch) 回调
//
// 扫描结束（或 ctx 取消）后：close(identCh) → 识别池排空 → close(resCh) →
// 聚合器收尾 flush。**部分结果照常回调**（取消不丢已拿到的数据）。
// 返回 []ScanResult 语义与旧版一致，agent 端 postResults 逻辑原样复用。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// ===== 规则/探针索引包级缓存 =====
// 全量载入 FingerprintHub（web 3450 + service 11938）约 1.4s，
// 以前每次 RunBuiltinPortScan 都重新 Load+BuildServiceProbes；缓存后只有首次付代价。
// 失效条件：SCAN_RULES_DIR 逗号列表命中的任一脚本文件 mtime 变化（rules-update 换库后自动重载）。

type ruleCacheEntry struct {
	rules  *svcid.RuleSet
	probes *svcid.ProbeIndex
	sum    string // 命中文件的路径+mtime 指纹
}

var ruleCache sync.Map // rulesDir -> *ruleCacheEntry

// loadRulesCached 载入并缓存规则集与主动探针索引。
// 并发下可能重复构建，但结果幂等，sync.Map 覆盖写安全。
func loadRulesCached(paths string) (*svcid.RuleSet, *svcid.ProbeIndex, error) {
	sum, err := ruleFilesFingerprint(paths)
	if err != nil {
		return nil, nil, err
	}
	if v, ok := ruleCache.Load(paths); ok {
		e := v.(*ruleCacheEntry)
		if e.sum == sum {
			return e.rules, e.probes, nil
		}
	}
	rs, err := svcid.Load(paths)
	if err != nil {
		return nil, nil, err
	}
	e := &ruleCacheEntry{rules: rs, probes: rs.BuildServiceProbes(), sum: sum}
	ruleCache.Store(paths, e)
	return e.rules, e.probes, nil
}

// ruleFilesFingerprint 对逗号分隔路径列表命中的全部规则文件生成 mtime 指纹
// （与 svcid.collectRuleFiles 的收集口径一致：目录 glob / 直接给文件）。
func ruleFilesFingerprint(paths string) (string, error) {
	var b strings.Builder
	n := 0
	for _, raw := range strings.Split(paths, ",") {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		fi, e := os.Stat(p)
		if e != nil {
			return "", fmt.Errorf("规则路径 %s 不可用: %w", p, e)
		}
		var files []string
		if fi.IsDir() {
			w, _ := filepath.Glob(filepath.Join(p, "web_fingerprint_v4*.json"))
			s, _ := filepath.Glob(filepath.Join(p, "service_fingerprint_v4*.json"))
			files = append(files, w...)
			files = append(files, s...)
		} else {
			files = []string{p}
		}
		for _, f := range files {
			st, e2 := os.Stat(f)
			if e2 != nil {
				continue
			}
			fmt.Fprintf(&b, "%s|%d|", f, st.ModTime().UnixNano())
			n++
		}
	}
	if n == 0 {
		return "", fmt.Errorf("路径列表 %s 中没有规则文件", paths)
	}
	return b.String(), nil
}

// identWorkers 识别工作池并发（env IDENT_CONCURRENCY 可配，默认 32）
func identWorkers() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("IDENT_CONCURRENCY"))); err == nil && v > 0 && v <= 512 {
		return v
	}
	return 32
}

// flushBatchSize / flushInterval 聚合器参数：攒满即发、最多憋 300ms
const (
	flushBatchSize = 200
	flushInterval  = 300 * time.Millisecond
)

// RunBuiltinPortScan 用自研引擎跑一轮流式扫描。
// onResult 会被**多次**调用（每 flush 一批，批次 ≤200 条），返回值为全量累积结果。
// opts.ProjectID 同时作为审计库 Project 键与取消句柄键。
func RunBuiltinPortScan(targets, excludes []string, opts ScanOptions, logFn LogFunc, onResult func([]ScanResult)) ([]ScanResult, error) {
	// 取消句柄**入口即注册**（评审拍板：唯一取消入口 = StopScan(ProjectID)；
	// 先注册再干任何慢活，调用方重试 StopScan 必然命中，无注册竞态）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if opts.ProjectID != "" {
		registerScan(opts.ProjectID, &scanHandle{cancel: cancel})
		defer unregisterScan(opts.ProjectID)
	}

	ips := target.Resolve(targets, excludes)
	if len(ips) == 0 {
		return nil, fmt.Errorf("目标展开后为空")
	}

	portSpec := opts.Ports
	if portSpec == "" {
		portSpec = "top100" // 与历史默认 Top100 对齐
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
	if timeoutMs <= 0 {
		timeoutMs = 1500
	}

	// ===== ScanOptions 语义映射 =====
	// Modules: "port" 只扫端口；"port,url" 或空 → 额外做 HTTP/服务识别
	// Finger:  "tiny" 不做主动探针；"all"/空 → 全量（含主动探针）
	modules := strings.ToLower(strings.TrimSpace(opts.Modules))
	wantIdent := modules == "" || strings.Contains(modules, "url") || strings.Contains(modules, "finger")
	fullFinger := opts.Finger == "" || strings.Contains(strings.ToLower(opts.Finger), "all")

	// ===== 规则（缓存载入；失败降级为纯端口扫描）=====
	var rs *svcid.RuleSet
	var probes *svcid.ProbeIndex
	if r, p, e := loadRulesCached(BuiltinRulesDir()); e == nil {
		rs, probes = r, p
	} else {
		logf(logFn, "[自研引擎] 规则载入失败，仅做端口扫描："+e.Error())
	}
	identOpt := svcid.IdentOptions{Rules: rs, Probes: probes}
	if !fullFinger {
		identOpt.Probes = nil // tiny：只用规则/被动探针，不发主动探针
	}
	if !wantIdent {
		identOpt.Rules = nil
		identOpt.Probes = nil
	}
	workers := identWorkers()
	if !wantIdent || rs == nil {
		workers = 0 // 无识别：开放端口直接进聚合器
	}

	total := int64(len(ips)) * int64(len(ports))
	logf(logFn, fmt.Sprintf("[自研引擎] 目标 %d 个 × 端口 %d 个 = %d 探针，并发 %d，超时 %dms，识别=%v，识别并发 %d，全量指纹=%v",
		len(ips), len(ports), total, conc, timeoutMs, workers > 0, workers, fullFinger))

	// ===== 审计库（仅 agent 进程启用；旁路，建库失败只记日志不中断扫描）=====
	audit := newAuditWriter(opts.ProjectID, logFn)
	if audit != nil {
		defer audit.Close()
	}

	// ===== 流水线 =====
	var (
		allMu      sync.Mutex
		all        = make([]ScanResult, 0, 256)
		identified atomic.Int64
	)
	identCh := make(chan portscan.Result, 4096)
	resCh := make(chan ScanResult, 1024)

	var identWG sync.WaitGroup
	for i := 0; i < workers; i++ {
		identWG.Add(1)
		go func() {
			defer identWG.Done()
			for op := range identCh {
				resCh <- mapService(safeIdentify(ctx, op, identOpt))
			}
		}()
	}

	// 聚合器：flushBuf 由它**独占**（主协程只在它退出后读 all，无竞争）
	var aggWG sync.WaitGroup
	aggWG.Add(1)
	go func() {
		defer aggWG.Done()
		t := time.NewTicker(flushInterval)
		defer t.Stop()
		var buf []ScanResult
		var flushed int64
		flush := func() {
			if len(buf) == 0 {
				return
			}
			batch := buf
			buf = nil
			allMu.Lock()
			all = append(all, batch...)
			allMu.Unlock()
			if audit != nil {
				audit.WriteBatch(batch)
			}
			if onResult != nil {
				onResult(batch)
			}
			flushed += int64(len(batch))
			logProgressLine(logFn, fmt.Sprintf("[自研引擎] 上报批次 %d 条（累计 %d）", len(batch), flushed))
		}
		for r := range resCh {
			identified.Add(1)
			buf = append(buf, r)
			if len(buf) >= flushBatchSize {
				flush()
				t.Reset(flushInterval)
				continue
			}
			select {
			case <-t.C:
				flush()
			default:
			}
		}
		flush() // resCh 关闭 = 全部结果已投递，收尾
	}()

	// ===== 扫描（发现即投喂识别池）=====
	t0 := time.Now()
	var lastProgNs atomic.Int64
	lastProgNs.Store(t0.UnixNano())
	st, serr := runStreamingScan(ctx, ips, ports, conc, timeoutMs, workers, identCh, resCh,
		func(done, tot, open int64) {
			// 每秒 ≤1 次进度行
			now := time.Now().UnixNano()
			prev := lastProgNs.Load()
			if now-prev < int64(time.Second) || !lastProgNs.CompareAndSwap(prev, now) {
				return
			}
			emitProgress(logFn, done, tot, open, identified.Load(), time.Since(t0))
		})

	// 收尾顺序：扫描已返回（OnOpen 全部结束）→ 关识别输入 → 识别池排空 → 关结果流
	close(identCh)
	identWG.Wait()
	close(resCh)
	aggWG.Wait()

	elapsed := time.Since(t0)
	pps := 0.0
	if s := elapsed.Seconds(); s > 0 {
		pps = float64(st.Probes) / s
	}
	logProgressLine(logFn, fmt.Sprintf("[自研引擎] 扫描结束 (%d/%d) [100%%] [%s:0s] %.0f探针/秒 开放 %d 已识别 %d",
		st.Probes, total, elapsed.Round(time.Second), pps, st.Open, identified.Load()))

	allMu.Lock()
	out := append([]ScanResult(nil), all...)
	allMu.Unlock()
	if serr != nil {
		// 取消/出错：部分结果已照常 flush 回调 + 落审计库，调用方按 cancelled 语义处理
		return out, serr
	}
	return out, nil
}

// runStreamingScan 调 portscan.Scan 并把 OnOpen 接进流水线；返回统计与错误。
func runStreamingScan(ctx context.Context, ips []string, ports []int,
	conc, timeoutMs, workers int, identCh chan<- portscan.Result, resCh chan<- ScanResult,
	onProgress func(done, total, open int64)) (portscan.Stats, error) {

	_, st, err := portscan.Scan(ctx, ips, ports, portscan.Options{
		Concurrency:   conc,
		Timeout:       time.Duration(timeoutMs) * time.Millisecond,
		ProgressEvery: time.Second,
		OnOpen: func(ip string, port int) {
			if workers == 0 {
				// 纯端口模式：不进识别池，直接产出裸 TCP 结果
				select {
				case resCh <- ScanResult{IP: ip, Port: port, Protocol: "TCP"}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case identCh <- portscan.Result{IP: ip, Port: port}:
			case <-ctx.Done():
			}
		},
		OnProgress: onProgress,
	})
	return st, err
}

// safeIdentify 识别单端口。panic 兜底为裸 TCP 结果——识别层任何异常都不能拖垮流水线。
func safeIdentify(ctx context.Context, op portscan.Result, opt svcid.IdentOptions) (sv svcid.Service) {
	defer func() {
		if r := recover(); r != nil {
			sv = svcid.Service{IP: op.IP, Port: op.Port, Protocol: "tcp"}
		}
	}()
	return svcid.Identify(ctx, op.IP, op.Port, opt)
}

// mapService svcid.Service → 平台 ScanResult（字段映射口径与旧版完全一致）
func mapService(sv svcid.Service) ScanResult {
	proto := strings.ToUpper(sv.Protocol)
	if proto == "" {
		proto = "TCP"
	}
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
	return ScanResult{
		IP:            sv.IP,
		Port:          sv.Port,
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
	}
}

// emitProgress 进度行：兼容 agent 侧解析格式
//
//	「(done/total)」→ portRe；「N%」→ pctRe；「[已用:剩余]」→ etaRe
//	内容含 探针 done/total、当前 pps、开放数、已识别数。
func emitProgress(fn LogFunc, done, total, open, ident int64, elapsed time.Duration) {
	pct := 0
	if total > 0 {
		pct = int(done * 100 / total)
	}
	if pct > 100 {
		pct = 100
	}
	pps := float64(done) / max1(elapsed.Seconds())
	used := elapsed.Round(time.Second)
	eta := "-"
	if pps > 0 && total > done {
		rem := int64(float64(total-done) / pps)
		if rem > 3600*24 {
			rem = 3600 * 24 // 早期样本少时 ETA 抖动巨大，封顶一天
		}
		eta = (time.Duration(rem) * time.Second).Round(time.Second).String()
	}
	logProgressLine(fn, fmt.Sprintf("[自研引擎] 进度 %d%% (%d/%d) [%s:%s] %.0f探针/秒 开放 %d 已识别 %d",
		pct, done, total, used, eta, pps, open, ident))
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
