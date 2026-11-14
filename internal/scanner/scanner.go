package scanner

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RongSec/CyberLook-Scan/portscan"
	// sqlite 驱动注册在 sqlite_driver.go（!mipsle）/ sqlite_driver_unsupported.go（mipsle），
	// 不能在这里无条件 import——否则 mipsle 交叉编译被 modernc.org/libc 的
	// 无 MIPS 实现直接卡死（v0.0.27 出包实测发现，审计旁路在 mipsle 上降级为空）。
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

	// Ctrl 共享并发限速器（可选）。
	//
	// 队列模式下一个节点会并行跑 K 个单元扫描（QUEUE_UNIT_CONCURRENCY，默认 6）。
	// 若每个单元各建一个限速器，机器总在途探针数就是 K×thread，网卡/防火墙会先炸，
	// "/control limit" 也只对其中一路生效。传同一个 *portscan.Controls 进来后：
	//   - K 路扫描共用一份许可池 → 总在途探针恒 ≤ 当前 limit（K 只影响租约在途数）
	//   - 一次 SetLimit/Pause 对所有路同时生效（全局调速语义）
	// 传 nil = 老行为（每次扫描自建限速器，按 opts.Thread 初始化）。
	Ctrl *portscan.Controls
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

// ===== 队列模式：taskID 级共享 limiter 注册表 =====
//
// 为什么必须按 taskID 而不是 projectID 索引：
//
// 队列模式下 K 个单元（goroutine）并行扫同一个任务，它们共用一份
// *portscan.Controls（ScanOptions.Ctrl）。而上面的 running 表按**单元级**
// ProjectID 索引（agent-q-<session>-<ip>），ControlScan(proj) 只能命中"某一个
// 单元"的句柄。K=1 时两者恰好重合（所以单单元测 limit 是好的），但 K>1 时
// pause/limit 必须打到 K 路共用的那一个 ctrl 上——用单元键去投影，就会出现
// "旋钮在最该生效的场景里失效"（v0.0.27 实测：K=6 下 limit 打不上）。
//
// 这里单独用 taskID 做键，让**任何**单元的心跳都能投影到同一句柄，
// 既不依赖"恰好有单元 tick"，也不会漏掉某个单元。seq 单调递增做乱序防护：
// K 路并发下老心跳晚到会把新值盖回去，seq 更小的直接丢弃。
var (
	taskCtrlMu sync.RWMutex
	taskCtrls  = make(map[uint]*taskCtrlHandle)
)

type taskCtrlHandle struct {
	ctrl    *portscan.Controls
	lastSeq int64 // 已应用的最大调控版本号（乱序防护）
	// manualAt 最近一次「人工调控」（服务端下发的 limit/pause）时间。
	// 智能调速靠它让位：人在调的时候机器不要抢方向盘，否则会出现
	// "我设 60，十秒后被自动挡推回 1200"这种让人失去信任的行为。
	manualAt time.Time
}

// EnsureTaskCtrl 返回任务级共享 limiter；**仅首次**调用用 conc/timeoutMs 建初值，
// 之后一律复用已有实例。
//
// 关键约束：复用路径上**禁止**写 SetLimit——那正是 v0.0.27 "limit 只生效一瞬间
// 又被盖回节点并发值"的直接原因（每轮 claim 都拿节点默认并发重跑一遍 SetLimit）。
// 创建即定初值，之后的速率只由 ApplyTaskCtrl 依据 seq 推进。
func EnsureTaskCtrl(taskID uint, conc, timeoutMs int) *portscan.Controls {
	if taskID == 0 {
		return nil
	}
	taskCtrlMu.Lock()
	defer taskCtrlMu.Unlock()
	if h, ok := taskCtrls[taskID]; ok {
		return h.ctrl
	}
	if conc <= 0 {
		conc = 1000
	}
	if timeoutMs <= 0 {
		timeoutMs = 1500
	}
	h := &taskCtrlHandle{ctrl: portscan.NewControls(conc, timeoutMs)}
	taskCtrls[taskID] = h
	return h.ctrl
}

// TaskCtrl 取任务级共享 limiter（不存在返回 nil）。
func TaskCtrl(taskID uint) *portscan.Controls {
	taskCtrlMu.RLock()
	defer taskCtrlMu.RUnlock()
	if h, ok := taskCtrls[taskID]; ok {
		return h.ctrl
	}
	return nil
}

// UnregisterTaskCtrl 任务收敛（done/failed/stopped/删除）时释放句柄。
//
// 不释放会随任务数泄漏：Controls 内部有 sync.Cond 等待者，任务跑几轮就攒一批
// ctrl + 挂起的 goroutine。任务结束后下一次 claim 会用 EnsureTaskCtrl 重新建。
func UnregisterTaskCtrl(taskID uint) {
	if taskID == 0 {
		return
	}
	taskCtrlMu.Lock()
	delete(taskCtrls, taskID)
	taskCtrlMu.Unlock()
}

// AutoManualHold 人工调控的"让位期"：这段时间内智能调速不再改这个任务的并发。
// 人在调的时候机器不要抢方向盘 —— 否则会出现"我设 60，十秒后被自动挡推回 1200"。
const AutoManualHold = 5 * time.Minute

// 本地资源错误累计（进程级），供智能调速当反馈信号读差分。
// 单调递增、只加不清零：调速器自己记上次读数算增量，避免两边各自维护窗口撞车。
var localResTotal int64

// AtomicAddLocalRes 由引擎在每轮扫描结束后累加 st.LocalResErrs。
func AtomicAddLocalRes(n int64) {
	if n > 0 {
		atomic.AddInt64(&localResTotal, n)
	}
}

// LocalResErrsTotal 累计的本地资源错误数（EMFILE/ENOBUFS 等）。
func LocalResErrsTotal() int64 { return atomic.LoadInt64(&localResTotal) }

// ActiveTaskIDs 当前持有 limiter 的任务（智能调速按这个列表推送推荐值）。
func ActiveTaskIDs() []uint {
	taskCtrlMu.RLock()
	defer taskCtrlMu.RUnlock()
	ids := make([]uint, 0, len(taskCtrls))
	for id := range taskCtrls {
		ids = append(ids, id)
	}
	return ids
}

// SetTaskAutoLimit 智能调速专用入口：把机器算出的推荐在途数推到任务 limiter。
//
// 与 ApplyTaskCtrl 的分工：
//   - ApplyTaskCtrl 处理**服务端指令**，带 seq 乱序防护，并写入 manualAt 抢占方向盘；
//   - 本函数处理**机器自己的建议**，不参与 seq，且 manualAt 冷却期内直接放弃，
//     暂停中的任务也不碰（暂停就是暂停，不该被"自动"复活）。
//
// 返回 true 表示本次确实改了。
func SetTaskAutoLimit(taskID uint, n int) bool {
	if taskID == 0 || n <= 0 {
		return false
	}
	taskCtrlMu.Lock()
	h, ok := taskCtrls[taskID]
	if !ok {
		taskCtrlMu.Unlock()
		return false
	}
	if !h.manualAt.IsZero() && time.Since(h.manualAt) < AutoManualHold {
		taskCtrlMu.Unlock()
		return false
	}
	ctrl := h.ctrl
	taskCtrlMu.Unlock()

	if ctrl.Paused() || ctrl.Limit() == n {
		return false
	}
	ctrl.SetLimit(n)
	return true
}

// ApplyTaskCtrl 把一条心跳/claim 投影应用到任务级共享 limiter（幂等 + 乱序防护）。
//
// seq 语义：服务端每次状态变更自增，投影里带回。收到 seq 小于已应用值的
// （老心跳晚到）直接丢弃，避免"偶发回弹"的旋钮。seq<=0 视为老服务端/本地调用，
// 不做乱序判断（兼容）。
//
//   - pause   → Pause()（K 路同时冻结，在途探针自然结束）
//   - limit   → SetLimit+SetTimeoutMs，并 Resume（设了新值就按新值跑）
//   - resume / none / "" → Resume()（幂等：未暂停时是 no-op；保证 pause 后可收敛）
//
// 返回 false 表示句柄不存在或指令因乱序被丢弃。
func ApplyTaskCtrl(taskID uint, ctl ScanCtl, seq int64) bool {
	taskCtrlMu.Lock()
	h, ok := taskCtrls[taskID]
	if !ok {
		taskCtrlMu.Unlock()
		return false
	}
	if seq > 0 && seq < h.lastSeq {
		last := h.lastSeq
		taskCtrlMu.Unlock()
		log.Printf("[ctrl] 任务 #%d 丢弃乱序心跳 seq=%d（已应用 %d）", taskID, seq, last)
		return false
	}
	if seq > h.lastSeq {
		h.lastSeq = seq
	}
	// 只有服务端下发的指令（seq>0）才算"人工调控"，本地调用不算。
	if seq > 0 {
		switch ctl.Action {
		case "pause", "limit":
			h.manualAt = time.Now()
		}
	}
	ctrl := h.ctrl
	taskCtrlMu.Unlock()

	switch ctl.Action {
	case "pause":
		ctrl.Pause()
	case "limit":
		if ctl.Concurrency > 0 {
			ctrl.SetLimit(ctl.Concurrency)
		}
		if ctl.TimeoutMs > 0 {
			ctrl.SetTimeoutMs(ctl.TimeoutMs)
		}
		ctrl.Resume()
	case "resume", "none", "":
		ctrl.Resume()
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

// InterceptedPortsOf 对外暴露中间件接管端口检测（存活探测在 service 层调用）。
// 结果带缓存，重复调用不产生额外探测开销。
func InterceptedPortsOf(ports []int) []int {
	return interceptedPorts(ports)
}
