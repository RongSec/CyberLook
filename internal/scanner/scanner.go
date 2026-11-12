package scanner

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"asset-platform/internal/portscan"

	_ "modernc.org/sqlite"
)

// 引擎说明：v0.0.27 起 TscanPlus 已彻底移除，RunPortScan 恒走自研引擎
// （internal/portscan connect 扫描 + internal/svcid 全量指纹识别，流水线流式执行）。

// ScanResult 单条端口扫描结果
type ScanResult struct {
	IP            string `json:"ip"`
	Port          int    `json:"port"`
	Protocol      string `json:"protocol"`
	Service       string `json:"service"`
	Banner        string `json:"banner"`
	Title         string `json:"title"`
	Fingerprint   string `json:"fingerprint"`   // web 指纹
	StatusCode    string `json:"statusCode"`    // HTTP 响应码
	ContentLength int    `json:"contentLength"` // 响应体长度
	Response      string `json:"-"`             // 响应体（仅用于算页面指纹，不入库）
	OS            string `json:"os"`
	Mid           string `json:"mid"`
}

// ScanOptions 扫描参数（由模板/任务拼接）
// 注意：**没有 Ctx 字段**（评审拍板）。取消一律走 StopScan(ProjectID)：
// RunBuiltinPortScan 在入口就把取消句柄注册进 running 表，
// 调用方（agent/本地执行）收到取消信号后对 ProjectID 重试 StopScan 直至命中。
type ScanOptions struct {
	Engine    string // 已废弃：引擎唯一（自研）。字段保留仅为兼容任务/模板数据，任何值都走自研
	ProjectID string // 项目名（并发任务隔离 + 取消句柄键 + 审计库 Project 列）
	Modules   string // "port" / "port,url"：含 url（或空）才做 HTTP 服务识别
	Ports     string // 端口（空=Top100）
	Thread    int    // 并发数
	Timeout   int    // 超时（agent 传毫秒；<100 按秒换算）
	NoPing    bool   // 兼容字段：自研 connect 扫描天然不做 ICMP 预探测
	NoSmart   bool   // 兼容字段：自研按给定端口全扫
	Finger    string // "tiny"=不做主动探针；"all"/空=全量
	ExtraArgs string // 已废弃（tscan 命令行参数），保留兼容存储
}

// LogFunc 实时日志回调（isProgress 表示是否为进度条行）
type LogFunc func(line string, isProgress bool)

// ===== 运行中扫描的调控句柄（取消/暂停/继续/调速） =====
//
// key = ScanOptions.ProjectID（agent 端是队列会话名，本地执行是任务名）。
// 一个进程可能并行跑多个扫描（多分片/多任务），按 key 精确命中。
// 控制面动作域：pause / resume / limit（并发+超时）/ cancel，
// 由 service 层内存态投影随心跳下发，或直接进程内调用（本地执行路径）。

// ScanCtl 一次运行中扫描的调控指令
type ScanCtl struct {
	Action      string // "pause" | "resume" | "limit" | "cancel"
	Concurrency int    // limit 时的目标并发（10-8000）
	TimeoutMs   int    // limit 时的超时毫秒（100-30000）
}

type scanHandle struct {
	cancel context.CancelFunc
	ctrl   *portscan.Controls
}

var running sync.Map // projectID -> *scanHandle

func registerScan(key string, h *scanHandle) {
	if key != "" {
		running.Store(key, h)
	}
}

func unregisterScan(key string) {
	if key != "" {
		running.Delete(key)
	}
}

// StopScan 取消指定项目正在运行的扫描（幂等；不存在返回 false）。
// 取消后引擎会把已拿到的结果照常 flush 回调，然后 RunPortScan 返回 context.Canceled。
func StopScan(projectID string) bool {
	if v, ok := running.Load(projectID); ok {
		v.(*scanHandle).cancel()
		return true
	}
	return false
}

// ControlScan 对运行中的扫描施加调控（pause/resume/limit/cancel）。
// key 不存在（扫描未开始或已结束）返回 false——调用方把指令留在内存态，
// 下一条心跳再投影（幂等状态语义，指令不会丢）。
func ControlScan(projectID string, ctl ScanCtl) bool {
	v, ok := running.Load(projectID)
	if !ok {
		return false
	}
	h := v.(*scanHandle)
	switch ctl.Action {
	case "pause":
		h.ctrl.Pause()
	case "resume":
		h.ctrl.Resume()
	case "limit":
		if ctl.Concurrency > 0 {
			h.ctrl.SetLimit(ctl.Concurrency)
		}
		if ctl.TimeoutMs > 0 {
			h.ctrl.SetTimeoutMs(ctl.TimeoutMs)
		}
		h.ctrl.Resume() // 产品语义：设了新值就按新值跑（顺带解除暂停）
	case "cancel":
		h.cancel()
	default:
		return false
	}
	return true
}

// RunPortScan 执行端口扫描（唯一实现 = 自研引擎流式流水线）。
// onResult 为流式结果回调（每批 ≤200 条，识别完成即攒批发送），可为 nil。
func RunPortScan(targets, excludes []string, opts ScanOptions, logFn LogFunc, onResult func([]ScanResult)) ([]ScanResult, error) {
	return RunBuiltinPortScan(targets, excludes, opts, logFn, onResult)
}

// PortCount 端口串 → 端口数真值（""/top100=100，all=65535，支持 "80,443" / "1-1024"）。
// IP 租约队列的进度分母用它，创建即定，不再估算。
func PortCount(portSpec string) (int, error) {
	spec := strings.TrimSpace(portSpec)
	if spec == "" || strings.EqualFold(spec, "top100") {
		spec = "top100"
	}
	ports, err := portscan.ParsePorts(spec)
	if err != nil {
		return 0, err
	}
	return len(ports), nil
}

func logf(fn LogFunc, line string) {
	if fn != nil {
		fn(line, false)
	}
}

// logProgressLine 进度条风格日志行（isProgress=true，平台侧单独维护、覆盖刷新不刷屏）
func logProgressLine(fn LogFunc, line string) {
	if fn != nil {
		fn(line, true)
	}
}

// ansiRe 匹配 ANSI 颜色/控制转义序列
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// stripANSI 去除 ANSI 转义码（历史遗留：子进程输出清洗；现仅测试/工具引用）
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// ===== config.db（审计库）通用操作：节点「数据库管理」用 =====

// ListProjects 列出 config.db 里的所有 project（用于节点的「数据库管理」）
func ListProjects(dbPath string) ([]string, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT DISTINCT Project FROM ipscan
		UNION SELECT DISTINCT Project FROM urlscan
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
	for _, tbl := range []string{"ipscan", "urlscan"} {
		for _, p := range projects {
			db.Exec(fmt.Sprintf("DELETE FROM %s WHERE Project = ?", tbl), p)
		}
	}
	return nil
}

// ===== 历史数据解析工具（config.db 读取 / 兼容保留）=====

// cleanNone 把历史数据里的字符串 "None"/"<nil>" 视为空
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
	parts := strings.Split(fp, ",")
	var keep []string
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		lower := strings.ToLower(p)
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
	patterns := map[string]string{
		"浪潮bms": "浪潮", "inspur": "浪潮", "bmc": "BMC", "ilo": "iLO",
		"idrac": "iDRAC", "ipmi": "IPMI", "weblogic": "WebLogic", "tomcat": "Tomcat",
		"jboss": "JBoss", "jenkins": "Jenkins", "grafana": "Grafana", "kibana": "Kibana",
		"zabbix": "Zabbix", "exchange": "Exchange", "owa": "Exchange-OWA",
		"vmware": "VMware", "vcenter": "vCenter", "citrix": "Citrix",
		"fortinet": "Fortinet", "fortigate": "FortiGate", "paloalto": "PaloAlto",
		"checkpoint": "CheckPoint", "qnap": "QNAP", "synology": "Synology",
		"coremail": "Coremail",
	}
	for keyword, name := range patterns {
		if strings.Contains(lower, keyword) {
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

// cleanStatus 响应码清理：非 HTTP 的 "0"/"-1" 视为空
func cleanStatus(s string) string {
	s = cleanNone(s)
	if s == "0" || s == "-1" {
		return ""
	}
	return s
}

// extractHost 从 host 字段提取纯 IP（去掉 scheme 前缀和端口）
func extractHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	if j := strings.Index(host, "]"); j >= 0 { // IPv6 [::1]:80
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
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
