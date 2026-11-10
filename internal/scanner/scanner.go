package scanner

import (
	"crypto/tls"
	"io"
	"net/http"
	"regexp"
	"time"
	"sync"
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"asset-platform/internal/target"
	_ "modernc.org/sqlite"
)

// ScanResult 单条端口扫描结果
type ScanResult struct {
	IP          string `json:"ip"`
	Port        int    `json:"port"`
	Protocol    string `json:"protocol"`
	Service     string `json:"service"`
	Banner      string `json:"banner"`
	Title       string `json:"title"`
	Fingerprint string `json:"fingerprint"` // web 指纹
	StatusCode  string `json:"statusCode"`  // HTTP 响应码
	ContentLength int  `json:"contentLength"` // 响应体长度
	Response      string `json:"-"`            // 响应体（仅用于算页面指纹，不入库）
	OS          string `json:"os"`
	Mid         string `json:"mid"`
}

// ScanOptions 扫描参数（由模板/任务拼接）
type ScanOptions struct {
	Engine    string // 引擎选择：builtin=自研（默认）/ tscan=TscanPlus；空=按 SCAN_ENGINE 环境变量
	ProjectID string // -pr 项目名（并发任务隔离用）
	Modules   string // -m 模块，如 port 或 port,url,poc（默认 port）
	Ports     string // 端口（空=Top100）
	Thread    int    // 并发 -t
	Timeout   int    // 超时 -time
	NoPing    bool   // -np 禁用存活探测
	NoSmart   bool   // -nosmart 禁用启发式
	Finger    string // -finger web 指纹策略 min/tiny/all（默认 tiny）
	ExtraArgs string // 额外参数（自由拼接）
}

// LogFunc 实时日志回调（isProgress 表示是否为进度条行）
type LogFunc func(line string, isProgress bool)

// TscanClientPath TscanClient 二进制绝对路径
var TscanClientPath = ""

func SetTscanClientPath(p string) { TscanClientPath = p }

// running 记录正在执行的扫描进程（key = ProjectID），用于停止任务
var running sync.Map

// StopScan 杀掉指定项目正在运行的扫描进程
func StopScan(projectID string) bool {
	if v, ok := running.Load(projectID); ok {
		if cmd, ok2 := v.(*exec.Cmd); ok2 && cmd.Process != nil {
			_ = cmd.Process.Kill()
			return true
		}
	}
	return false
}

// RunPortScan 执行扫描：目标展开去重排除 → 写 host 文件 → 拼接命令 → 实时输出/入库 → 读 ipscan
// onResult 为可选的实时结果回调（扫描过程中每发现新结果即回调，用于实时入库）
func RunPortScan(targets, excludes []string, opts ScanOptions, logFn LogFunc, onResult func([]ScanResult)) ([]ScanResult, error) {
	// 引擎路由：任务级 opts.Engine 优先，否则按全局默认（SCAN_ENGINE 环境变量）
	if useEngine(opts.Engine) {
		return RunBuiltinPortScan(targets, excludes, opts, logFn, onResult)
	}
	if TscanClientPath == "" {
		return nil, fmt.Errorf("TscanClient 路径未配置")
	}
	workDir := filepath.Dir(TscanClientPath)
	dbPath := filepath.Join(workDir, "config", "config.db")

	if _, err := os.Stat(dbPath); err == nil {
		_ = clearProjectIPScan(dbPath, opts.ProjectID)
	}

	// 目标处理：展开 + 去重 + 去除避开（平台侧完成，不用 -hn）
	finalTargets := target.Resolve(targets, excludes)
	if len(finalTargets) == 0 {
		return nil, fmt.Errorf("去除避开范围后无有效目标")
	}
	logf(logFn, fmt.Sprintf("[目标] 展开去重后共 %d 个 IP，已去除 %d 个避开范围", len(finalTargets), len(excludes)))

	hostFile, err := writeHostsFile(finalTargets)
	if err != nil {
		return nil, err
	}
	defer os.Remove(hostFile)

	args := buildArgs(hostFile, opts)
	logf(logFn, "[命令] "+filepath.Base(TscanClientPath)+" "+strings.Join(args, " "))

	cmd := exec.Command(TscanClientPath, args...)
	cmd.Dir = workDir
	// 推模式：子进程每次写 stdout/stderr 立即回调，保证实时性
	lw := &lineWriter{fn: logFn}
	// 优先用伪终端：TscanClient 检测到 stdout 是 TTY 时才绘制进度条
	// 可用 TSCAN_PTY=0 关闭（回退普通管道）
	ptyWait := func() error { return nil }
	started := false
	if os.Getenv("TSCAN_PTY") != "0" {
		if wait, perr := startWithPTY(cmd, lw); perr == nil {
			ptyWait = wait
			started = true
		} else {
			logf(logFn, "[提示] PTY 启动失败，回退普通管道: "+perr.Error())
		}
	}
	if !started {
		cmd.Stdout = lw
		cmd.Stderr = lw
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("启动扫描失败: %v", err)
		}
	}
	running.Store(opts.ProjectID, cmd)
	defer running.Delete(opts.ProjectID)

	// 扫描过程中实时读 config.db，把新发现的 IP:端口实时回显并回调入库（TscanClient 结果写 db 不打印 stdout）
	stopPoll := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		seen := map[string]bool{}
		for {
			select {
			case <-stopPoll:
				return
			case <-ticker.C:
				if found, err := readIPScan(dbPath, opts.ProjectID); err == nil {
					var newResults []ScanResult
					for _, r := range found {
						key := r.IP + ":" + strconv.Itoa(r.Port)
						if !seen[key] {
							seen[key] = true
							logf(logFn, fmt.Sprintf("[发现] %s:%d  %s  %s", r.IP, r.Port, r.Protocol, r.Banner))
							newResults = append(newResults, r)
						}
					}
					if len(newResults) > 0 && onResult != nil {
						onResult(newResults)
					}
				}
			}
		}
	}()

	if err := ptyWait(); err != nil {
		close(stopPoll)
		return nil, fmt.Errorf("扫描执行失败: %v", err)
	}
	close(stopPoll)
	logf(logFn, "[完成] 扫描结束")

	results, err := readIPScan(dbPath, opts.ProjectID)
	if err != nil {
		return nil, err
	}
	// 若启用 url 模块，合并 urlscan 表结果
	if strings.Contains(opts.Modules, "url") {
		if urls, err := readURLScan(dbPath, opts.ProjectID); err == nil {
			results = append(results, urls...)
		}
	}
	return results, nil
}

// buildArgs 拼接 TscanClient 命令参数
func buildArgs(hostFile string, opts ScanOptions) []string {
	modules := opts.Modules
	if modules == "" {
		// 默认带 url 模块：只有 url 才会真正发 HTTP 请求，拿到响应码/标题/响应长度，
		// 否则无法区分「端口开放」和「服务能访问」
		modules = "port,url"
	}
	args := []string{"-m", modules}
	if opts.ProjectID != "" {
		args = append(args, "-pr", opts.ProjectID)
	}
	args = append(args, "-hf", hostFile)
	if opts.Ports != "" {
		args = append(args, "-p", opts.Ports)
	}
	if opts.Thread > 0 {
		args = append(args, "-t", strconv.Itoa(opts.Thread))
	}
	if opts.Timeout > 0 {
		args = append(args, "-time", strconv.Itoa(opts.Timeout))
	}
	if opts.NoPing {
		args = append(args, "-np")
	}
	if opts.NoSmart {
		args = append(args, "-nosmart")
	}
	if strings.Contains(modules, "url") && opts.Finger != "" {
		args = append(args, "-finger", opts.Finger)
	}
	args = append(args, "-nocolor")
	if opts.ExtraArgs != "" {
		args = append(args, strings.Fields(opts.ExtraArgs)...)
	}
	return args
}

func writeHostsFile(ips []string) (string, error) {
	f, err := os.CreateTemp("", "scope-hosts-*.txt")
	if err != nil {
		return "", err
	}
	defer f.Close()
	for _, ip := range ips {
		_, _ = f.WriteString(ip + "\n")
	}
	return f.Name(), nil
}

func logf(fn LogFunc, line string) {
	if fn != nil {
		fn(line, false)
	}
}

// ansiRe 匹配 ANSI 颜色/控制转义序列
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// stripANSI 去除 ANSI 转义码（终端颜色乱码）
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// lineWriter 推模式日志写入器：子进程每次写输出立即按行回调
type lineWriter struct {
	fn  LogFunc
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		line := w.buf[:i]
		w.buf = w.buf[i+1:]
		s := strings.TrimSpace(string(line))
		if s == "" {
			continue
		}
		// 含进度条字符 ░▒▓█ 的行视为进度条（单独维护，覆盖刷新不刷屏）
		isProgress := strings.ContainsAny(s, "░▒▓█")
		w.fn(stripANSI(s), isProgress)
	}
	return len(p), nil
}

func clearProjectIPScan(dbPath, projectID string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if projectID == "" {
		projectID = "Default"
	}
	_, err = db.Exec("DELETE FROM ipscan WHERE Project = ?", projectID)
	return err
}

func readIPScan(dbPath, projectID string) ([]ScanResult, error) {
	return readTable(dbPath, "ipscan", projectID)
}

// readURLScan 读 url 模块结果（urlscan 表）
func readURLScan(dbPath, projectID string) ([]ScanResult, error) {
	return readTable(dbPath, "urlscan", projectID)
}

// readTable 读指定表（ipscan/urlscan 结构一致），按 Project 过滤
func readTable(dbPath, table, projectID string) ([]ScanResult, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("打开 config.db 失败: %w", err)
	}
	defer db.Close()

	if projectID == "" {
		projectID = "Default"
	}
	query := fmt.Sprintf("SELECT Host, Port, Protocol, Banner, Title, Os, Mid, StatusCode, COALESCE(Lenbody,'0'), COALESCE(substr(Response,1,200000),'') FROM %s WHERE Project = ?", table)
	rows, err := db.Query(query, projectID)
	if err != nil {
		return nil, fmt.Errorf("查询 %s 表失败: %w", table, err)
	}
	defer rows.Close()

	var results []ScanResult
	for rows.Next() {
		var r ScanResult
		var host, port, protocol, banner, title, osName, mid, statusCode, lenBody, respBody string
		if err := rows.Scan(&host, &port, &protocol, &banner, &title, &osName, &mid, &statusCode, &lenBody, &respBody); err != nil {
			continue
		}
		// host 可能是 "ip:port" 或 "http://ip:port" 格式，提取纯 IP
		r.IP = extractHost(host)
		if r.IP == "" {
			continue
		}
		r.Port, _ = strconv.Atoi(port)
		r.Protocol = cleanNone(protocol)
		r.Banner = cleanNone(banner)
		r.Title = cleanNone(title)
		// 标题 fallback：从响应体提取 <title>
		if r.Title == "" && respBody != "" {
			r.Title = extractTitle(respBody)
		}
		// 指纹：过滤安全特性 + 从响应体补充产品指纹
		r.Fingerprint = enhanceFingerprint(cleanFingerprint(cleanNone(banner)), respBody)

		// 注意：这里**不做**二次识别（identifyEnhanced）。
		// 历史上 readTable 被 500ms ticker 反复调用，每次对泛标题服务同步发 GET（8s 超时），
		// 同一服务在扫描期被反复识别几十上百次，是比 LLM 更贵的隐藏瓶颈。
		// 识别统一交给 ingestService 之后的识别链路（本地产品库/规则/LLM），
		// readTable 只负责「读 + 解析」，不发任何网络请求。
		r.StatusCode = cleanStatus(statusCode)
		if n, e := strconv.Atoi(strings.TrimSpace(lenBody)); e == nil {
			r.ContentLength = n
		}
		r.Response = respBody
		r.OS = cleanNone(osName)
		r.Mid = cleanNone(mid)
		r.Service = inferService(protocol, banner, mid)
		results = append(results, r)
	}
	return results, nil
}

// cleanNone 把 TscanClient 写入的字符串 "None" 视为空
func cleanNone(s string) string {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "none") || s == "<nil>" {
		return ""
	}
	return s
}

// extractTitle 从 HTTP 响应体提取 <title> 标签
func extractTitle(resp string) string {
	lower := strings.ToLower(resp)
	start := strings.Index(lower, "<title")
	if start < 0 {
		return ""
	}
	// 找到 > 后开始
	gt := strings.Index(resp[start:], ">")
	if gt < 0 {
		return ""
	}
	content := resp[start+gt+1:]
	end := strings.Index(strings.ToLower(content), "</title>")
	if end < 0 {
		return ""
	}
	title := strings.TrimSpace(content[:end])
	title = strings.NewReplacer("\r", "", "\n", " ", "\t", " ").Replace(title)
	// 去 HTML 实体
	title = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"", "&#39;", "'").Replace(title)
	if len(title) > 200 {
		title = title[:200]
	}
	return title
}

// cleanFingerprint 过滤非产品指纹（HSTS 等安全特性）并去重
func cleanFingerprint(fp string) string {
	fp = strings.TrimSpace(fp)
	if fp == "" || strings.EqualFold(fp, "none") {
		return ""
	}
	// 按逗号分割，过滤掉安全特性类标签
	parts := strings.Split(fp, ",")
	var keep []string
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		lower := strings.ToLower(p)
		// 跳过纯安全特性（不是产品指纹）
		switch lower {
		case "hsts", "x-frame-options", "x-content-type-options",
			"x-xss-protection", "content-security-policy", "x-powered-by":
			continue
		}
		if !seen[lower] {
			seen[lower] = true
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, ",")
}

// enhanceFingerprint 从响应体补充产品指纹
func enhanceFingerprint(fp, resp string) string {
	if resp == "" {
		return fp
	}
	lower := strings.ToLower(resp)
	var extras []string
	// 常见产品特征（简单关键词匹配）
	patterns := map[string]string{
		"浪潮bms":      "浪潮",
		"inspur":       "浪潮",
		"bmc":          "BMC",
		"ilo":          "iLO",
		"idrac":        "iDRAC",
		"ipmi":         "IPMI",
		"weblogic":     "WebLogic",
		"tomcat":       "Tomcat",
		"jboss":        "JBoss",
		"jenkins":      "Jenkins",
		"grafana":      "Grafana",
		"kibana":       "Kibana",
		"zabbix":       "Zabbix",
		"exchange":     "Exchange",
		"owa":          "Exchange-OWA",
		"vmware":       "VMware",
		"vcenter":      "vCenter",
		"citrix":       "Citrix",
		"fortinet":     "Fortinet",
		"fortigate":    "FortiGate",
		"paloalto":     "PaloAlto",
		"checkpoint":   "CheckPoint",
		"qnap":         "QNAP",
		"synology":     "Synology",
		"coremail":     "Coremail",
	}
	for keyword, name := range patterns {
		if strings.Contains(lower, keyword) {
			// 检查是否已在指纹里
			if !strings.Contains(strings.ToLower(fp), strings.ToLower(name)) {
				extras = append(extras, name)
			}
		}
	}
	if len(extras) > 0 {
		if fp != "" {
			fp = fp + "," + strings.Join(extras, ",")
		} else {
			fp = strings.Join(extras, ",")
		}
	}
	return fp
}


// cleanStatus 响应码清理：TscanClient 对非 HTTP 会写 "0"，视为空
func cleanStatus(s string) string {
	s = cleanNone(s)
	if s == "0" || s == "-1" {
		return ""
	}
	return s
}

// extractHost 从 host 字段提取纯 IP（去掉 http:// 前缀和端口）
func extractHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	if i := strings.Index(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

// inferService 从协议/banner 推断服务名
func inferService(protocol, banner, mid string) string {
	low := strings.ToLower(banner + " " + mid)
	keywords := []struct {
		kw, name string
	}{
		{"openssh", "ssh"}, {"ssh", "ssh"},
		{"mysql", "mysql"}, {"mariadb", "mysql"},
		{"redis", "redis"},
		{"postgres", "postgresql"},
		{"mssql", "mssql"}, {"sql server", "mssql"},
		{"mongodb", "mongodb"}, {"mongo", "mongodb"},
		{"elasticsearch", "elasticsearch"},
		{"memcached", "memcached"},
		{"zookeeper", "zookeeper"},
		{"kafka", "kafka"},
		{"uvicorn", "uvicorn"},
		{"nginx", "nginx"}, {"apache", "apache"}, {"tomcat", "tomcat"},
		{"rdp", "rdp"}, {"vnc", "vnc"}, {"ftp", "ftp"}, {"smtp", "smtp"},
		{"dns", "dns"}, {"telnet", "telnet"},
	}
	for _, k := range keywords {
		if strings.Contains(low, k.kw) {
			return k.name
		}
	}
	if protocol != "" && strings.ToLower(protocol) != "tcp" {
		return strings.ToLower(protocol)
	}
	return "unknown"
}

// ListProjects 列出 config.db 里的所有 project（用于节点的「数据库管理」）
func ListProjects(dbPath string) ([]string, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT DISTINCT Project FROM ipscan
		UNION SELECT DISTINCT Project FROM urlscan
		UNION SELECT DISTINCT Project FROM poccheck
		ORDER BY Project`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil && p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// DeleteProjects 删除指定的 project 数据（config.db）
func DeleteProjects(dbPath string, projects []string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, tbl := range []string{"ipscan", "urlscan", "poccheck"} {
		for _, p := range projects {
			db.Exec(fmt.Sprintf("DELETE FROM %s WHERE Project = ?", tbl), p)
		}
	}
	return nil
}

// identifyEnhanced 调 fingerprint 包做二次识别（只在需要时调用）
func identifyEnhanced(protocol, ip string, port int) *IdentifyResult {
	// 用内部 HTTP 客户端发请求
	client := NewCompatClient(8 * time.Second)
	scheme := "http"
	if protocol == "HTTPS" || port == 443 || port == 8443 {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s:%d/", scheme, ip, port)

	resp, err := client.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	html := string(body)

	result := &IdentifyResult{}
	// title
	if m := regexp.MustCompile(`(?i)<title[^>]*>([^<]+)</title>`).FindStringSubmatch(html); m != nil {
		result.Title = strings.TrimSpace(m[1])
	}
	// meta description
	if m := regexp.MustCompile(`(?i)<meta[^>]*name=["']description["'][^>]*content=["']([^"']+)`).FindStringSubmatch(html); m != nil {
		result.MetaDesc = strings.TrimSpace(m[1])
	}
	// fingerprint：从关键词提取
	lower := strings.ToLower(html)
	keywords := map[string]string{
		"inspur": "浪潮", "浪潮": "浪潮",
		"vmware": "VMware", "esxi": "ESXi",
		"深信服": "深信服", "sangfor": "深信服",
		"华为": "华为", "h3c": "H3C",
		"hp": "HP", "ilo": "iLO",
		"ibm": "IBM", "kong": "Kong",
		"apache": "Apache", "nginx": "nginx",
	}
	var fps []string
	for kw, name := range keywords {
		if strings.Contains(lower, kw) {
			already := false
			for _, f := range fps {
				if f == name { already = true; break }
			}
			if !already {
				fps = append(fps, name)
			}
		}
	}
	result.Fingerprint = strings.Join(fps, ",")
	return result
}

// IdentifyResult 识别结果
type IdentifyResult struct {
	Title    string
	MetaDesc string
	Fingerprint string
}

// NewCompatClient 兼容的 HTTP 客户端（简化版）
func NewCompatClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   timeout,
	}
}
