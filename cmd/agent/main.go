package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"asset-platform/internal/portscan"
	"asset-platform/internal/scanner"
	"asset-platform/internal/version"
)

var (
	serverURL = flag.String("server", "", "中心服务地址，如 http://10.1.1.1:8080")
	nodeName  = flag.String("name", "", "节点名称（唯一）")
	token     = flag.String("token", "default-token", "注册 token")
)

var nodeID uint

// scanTask 服务端任务配置
type scanTask struct {
	ID        uint   `json:"id"`
	Name      string `json:"name"`
	IPRanges  string `json:"ipRanges"`
	PortMode  string `json:"portMode"`
	Ports     string `json:"ports"`
	Modules   string `json:"modules"`
	Finger    string `json:"finger"`
	Thread    int    `json:"thread"`
	Timeout   int    `json:"timeout"`
	NoPing    bool   `json:"noPing"`
	NoSmart   bool   `json:"noSmart"`
	ExtraArgs string `json:"extraArgs"`
}

// AgentTask 分片任务
type AgentTask struct {
	ID       uint   `json:"id"`
	TaskID   uint   `json:"taskId"`
	NodeName string `json:"nodeName"`
	IPRanges string `json:"ipRanges"`
	Status   string `json:"status"`
	Total    int    `json:"total"`
	Progress int    `json:"progress"`
	Message  string `json:"message"` // "cleanup:[...]" = 清理审计库的 project
}

type nodeConf struct {
	Timeout     int `json:"timeout"`
	Concurrency int `json:"concurrency"`
}

type taskDetail struct {
	AgentTask AgentTask `json:"agentTask"`
	Task      scanTask  `json:"task"`
	NodeConf  nodeConf  `json:"nodeConf"`
}

// ctrlCmd 平台随心跳响应下发的调控指令（**幂等状态投影**，非事件）。
// action: none|pause|resume|limit —— agent 每次心跳把当前全量态应用到引擎句柄：
//   - pause → Controls.Pause()（许可清零，扫描挂起、分片保持占用、进度冻结）
//   - resume/none → Controls.Resume()（none=服务端无调控态；重启后也能解除旧暂停，保证收敛）
//   - limit → SetLimit+SetTimeoutMs（下一个探针即生效）
//
// 引擎句柄未注册（扫描还没进 RunBuiltinPortScan）时 ControlScan 返回 false，
// 无所谓——下一条心跳再投影。取消（cancel）不在这里：走 cancelled 字段既有链路。

// shardRun 运行中分片：取消句柄
type shardRun struct {
	cancel context.CancelFunc
}

var running sync.Map // agentTaskID -> *shardRun

// ringBuf 保存最近 N 行日志，定期上报给中心用于「节点终端」展示
type ringBuf struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func (r *ringBuf) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ln := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(ln) != "" {
			r.lines = append(r.lines, ln)
		}
	}
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
	return len(p), nil
}

func (r *ringBuf) Snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

var logRing = &ringBuf{max: 300}

// nodeToken 注册时由服务端下发，随每个请求带上用于认证
var nodeToken string

const (
	agentStateFile  = "agent.json"  // {"id":5,"token":"..."}
	legacyTokenFile = "agent.token" // 旧格式：仅 token
)

type agentState struct {
	ID    uint   `json:"id"`
	Token string `json:"token"`
}

// loadNodeToken 读取本地凭据（节点 ID + 长期令牌），
// 有了它重启时无需再次使用一次性加入令牌
func loadNodeToken() {
	if b, err := os.ReadFile(agentStateFile); err == nil {
		var st agentState
		if json.Unmarshal(b, &st) == nil && st.Token != "" {
			nodeToken = st.Token
			if st.ID > 0 {
				nodeID = st.ID
			}
			return
		}
	}
	// 兼容旧版仅存 token 的文件
	if b, err := os.ReadFile(legacyTokenFile); err == nil {
		nodeToken = strings.TrimSpace(string(b))
	}
}

// saveNodeToken 持久化节点 ID 与长期令牌
func saveNodeToken(id uint, tk string) {
	nodeToken = tk
	if id > 0 {
		nodeID = id
	}
	if tk == "" {
		return
	}
	if b, err := json.Marshal(agentState{ID: nodeID, Token: tk}); err == nil {
		_ = os.WriteFile(agentStateFile, b, 0o600)
	}
}

// agentReq 统一的 agent 请求入口（自动带 X-Node-Token）
func agentReq(method, url string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return nil, err
	}
	if nodeToken != "" {
		req.Header.Set("X-Node-Token", nodeToken)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return http.DefaultClient.Do(req)
}

// selfVersion 自身二进制的版本（数字版本号）
func selfVersion() string {
	return version.Version
}

var myVersion = "unknown"

// selfUpgrade 下载新的控制文件，校验后替换自身并重启
func selfUpgrade(target string) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("[agent] 获取自身路径失败: %v", err)
		return
	}
	tmp := exe + ".new"
	// 用节点自身凭据下载（一次性加入令牌早已过期）；
	// 必须带 arch，否则 ARM64 节点会拿到 amd64 二进制
	dlURL := fmt.Sprintf("%s/api/nodes/agent-binary?os=%s&arch=%s&nodeId=%d",
		*serverURL, runtime.GOOS, runtime.GOARCH, nodeID)
	req, err := http.NewRequest("GET", dlURL, nil)
	if err != nil {
		log.Printf("[agent] 构造下载请求失败: %v", err)
		return
	}
	req.Header.Set("X-Node-Token", nodeToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[agent] 下载新控制文件失败: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		log.Printf("[agent] 下载新控制文件失败: HTTP %d %s", resp.StatusCode, string(body))
		return
	}
	f, err := os.Create(tmp)
	if err != nil {
		log.Printf("[agent] 创建临时文件失败: %v", err)
		return
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		log.Printf("[agent] 写入临时文件失败: %v", err)
		return
	}
	f.Close()

	// 信任 Release（校验二进制可执行：文件非空）
	if fi, err := os.Stat(tmp); err != nil || fi.Size() < 1024 {
		os.Remove(tmp)
		log.Printf("[agent] 下载的控制文件异常（太小）")
		return
	}
	_ = os.Chmod(tmp, 0755)

	// 备份旧文件后原子替换
	_ = os.Remove(exe + ".old")
	_ = os.Rename(exe, exe+".old")
	if err := os.Rename(tmp, exe); err != nil {
		log.Printf("[agent] 替换失败: %v，回滚", err)
		_ = os.Rename(exe+".old", exe)
		return
	}
	log.Printf("[agent] 控制文件已更新 %s -> %s，正在交接重启", myVersion, target)

	// Windows 不支持 syscall.Exec，改用 exec.Command 启动新进程
	if runtime.GOOS == "windows" {
		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			log.Printf("[agent] 启动新进程失败: %v（watchdog 会拉起）", err)
		} else {
			log.Printf("[agent] 新进程已启动 PID=%d，旧进程退出", cmd.Process.Pid)
		}
		os.Exit(0)
	}

	// Linux/Mac：原子替换当前进程
	if err := syscallExec(exe); err != nil {
		log.Printf("[agent] 重启失败: %v（watchdog 会拉起）", err)
	}
}

// disableProxyTransport agent 只与内网中心通信，必须直连。
// 系统若设置了 HTTP_PROXY/HTTPS_PROXY，DefaultClient 会把内网请求也送去代理，
// 导致心跳/任务请求静默失败（日志里看不到任何报错）。
func disableProxyTransport() {
	http.DefaultTransport = &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        64,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	http.DefaultClient.Transport = http.DefaultTransport
}

func main() {
	disableProxyTransport()
	flag.Parse()
	log.SetOutput(io.MultiWriter(os.Stderr, logRing))
	myVersion = selfVersion()
	loadNodeToken()
	// 审计落库仅 agent 进程启用：config/config.db 与 scope-agent 同目录
	// （下载 exe 到哪、审计库就生成到哪；不可写自动回退 ./config/）
	scanner.EnableAudit()
	checkNoFileLimit()
	// 所有日志带主机名前缀，便于在中心端区分是哪个节点在执行
	if hn, err := os.Hostname(); err == nil && hn != "" {
		log.SetPrefix("[" + hn + "] ")
		log.Printf("[agent] 启动，主机名=%s", hn)
	}
	if *serverURL == "" || *nodeName == "" {
		fmt.Println("用法: scope-agent -server http://中心IP:8080 -name 节点名 [-token xxx]")
		os.Exit(1)
	}
	*serverURL = strings.TrimRight(*serverURL, "/")
	log.Printf("[agent] 扫描引擎：自研（portscan+svcid，进程内执行，无需外部二进制）；审计库=%s", scanner.EngineDBPath())

	// 注册失败自动重试（服务端可能还没就绪）
	for i := 1; i <= 60; i++ {
		if err := register(); err == nil && nodeID > 0 {
			break
		}
		if i < 60 {
			log.Printf("[agent] 注册未成功（第 %d 次），5 秒后重试", i)
			time.Sleep(5 * time.Second)
		}
	}
	if nodeID == 0 {
		log.Fatal("[agent] 注册失败：未能获取节点 ID，退出")
	}
	log.Printf("[agent] 注册成功，节点 ID=%d，中心=%s", nodeID, *serverURL)
	log.Printf("[agent] 已建立唤醒通道，空闲时不轮询（有任务服务端会主动唤醒）")

	go heartbeatLoop()
	go streamLoop()   // SSE 长连接：有任务时被唤醒
	go fetchAndRun()  // 启动时拉一次（处理重启前遗留任务/清理指令）
	go queueLoop()    // v0.0.27 IP 租约队列消费循环（任务是否入队由服务端 queue_mode 决定）
	go auditJanitor() // 清理已结束任务的审计库 Project 行（防 config.db 无界增长）

	select {}
}

// ===== v0.0.27 IP 租约队列：agent 侧消费循环 =====
//
// 结构：轮询 /queue-tasks（该节点可消费的在途队列任务）→ 逐个 /claim 原子领取
// 单 IP 单元 → 进程内扫这一个 IP（流式回调照常 postResults）→ 带 ip 的心跳每 2s
// 续租+上报探针读数 → 单元完成发 unitDone 终报 → 立刻领下一个。
// claim 空（队列消费完/暂停/任务收敛）→ 退避 3s 再查。
// 暂停：claim 响应 paused=true 时不领取；在扫单元由 cmd=pause 投影冻结（进度归零）。
// 取消：会话行被 StopTask 置 cancelled → 心跳 cancelled=true → StopScan 停当前单元、
// 退出本轮领取（不再 claim）。
//
// 旧分片通道（fetchAndRun）保持运行：清理指令（cleanup:）与 USE_IP_QUEUE=false
// 的回退任务都还走它，一个版本周期后随分片制一起退役。

type queueTaskRef struct {
	TaskID    uint   `json:"taskId"`
	TaskName  string `json:"taskName"`
	Claimable bool   `json:"claimable"`
	SessionID uint   `json:"sessionId"`
}

type claimResp struct {
	HasUnit bool `json:"hasUnit"`
	Paused  bool `json:"paused"`
	// Units 批量领取结果（v0.0.27.1 起）；老服务端只回 unit，这里做了兜底。
	Units    []claimUnitRef `json:"units"`
	Unit     *claimUnitRef  `json:"unit"`
	Task     scanTask       `json:"task"`
	NodeConf struct {
		Timeout     int `json:"timeout"`
		Concurrency int `json:"concurrency"`
	} `json:"nodeConf"`
	// Cmd 该任务的调控幂等状态（pause/limit）；每轮 claim 都会带回来，
	// 用于在**共享 limiter** 上做 node 级持久投影（不依赖单元心跳恰好 tick 到）。
	Cmd ctrlCmd `json:"cmd"`
}

type ctrlCmd struct {
	Action      string `json:"action"`
	Paused      bool   `json:"paused"`
	Concurrency int    `json:"concurrency"`
	TimeoutMs   int    `json:"timeoutMs"`
}

type claimUnitRef struct {
	IP          string `json:"ip"`
	ProbesTotal int64  `json:"probesTotal"`
	SessionID   uint   `json:"sessionId"`
}

// units 取本次领取到的单元列表（兼容只有 unit 的老服务端）。
func (c *claimResp) unitList() []claimUnitRef {
	if c == nil {
		return nil
	}
	if len(c.Units) > 0 {
		return c.Units
	}
	if c.Unit != nil && c.Unit.IP != "" {
		return []claimUnitRef{*c.Unit}
	}
	return nil
}

// queueUnitConcurrency 节点内并行单元数 K（env QUEUE_UNIT_CONCURRENCY，默认 6，钳制 1..32）。
//
// 为什么需要 K>1：旧实现"领 1 个 → 扫完 → 再领"，每个单元 1 个 IP×100 端口 ≈2.1s
// （受单 IP 内最慢探针的超时支配），节点吞吐被卡在 ~47 pps，/26 段要 9 分钟。
// K 路并行后同样的单 IP 延迟不再是端到端瓶颈，只是租约在途数变多；
// 发包总量由共享限速器（scanner.ScanOptions.Ctrl）统一约束，K 不会放大流量。
func queueUnitConcurrency() int {
	k := 6
	if v := strings.TrimSpace(os.Getenv("QUEUE_UNIT_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			k = n
		}
	}
	if k < 1 {
		k = 1
	}
	if k > 32 {
		k = 32
	}
	return k
}

// queueCtrl 节点级共享限速器（K 路单元扫描共用一份许可池）。
// 首次 claim 拿到 nodeConf 时初始化；之后所有单元 Reuse，"/control limit" 一次全生效。
var (
	queueCtrlMu   sync.Mutex
	queueCtrlInst *portscan.Controls
)

func queueLimiter(conc, timeoutMs int) *portscan.Controls {
	queueCtrlMu.Lock()
	defer queueCtrlMu.Unlock()
	if queueCtrlInst == nil {
		if conc <= 0 {
			conc = 1000
		}
		if timeoutMs <= 0 {
			timeoutMs = 1500
		}
		queueCtrlInst = portscan.NewControls(conc, timeoutMs)
	}
	return queueCtrlInst
}

// ===== 本机 fd 上限自检（v0.0.27 假阴性防护）=====
//
// 自研引擎是 connect 扫描：**每个在途探针占一个 socket**。容器默认 nofile=1024 时，
// 一旦在途探针数超过上限，dial 会立刻返回 EMFILE——如果按"连接失败=端口关闭"处理，
// 扫描器就会对外输出"该主机没有开放端口"（假阴性，比慢一百倍更致命）。
// 所以启动时就把"安全在途上限"算出来，并把默认并发钳到安全值。
//
// 规则：
//
//	soft < 2048  → 醒目 WARN，默认并发钳到 soft-256（留 256 个给日志/DB/SSH 等）
//	soft >= 8192 → 默认并发提到 1500（足够跑满 800+ pps）
//	其余          → 默认并发 = min(1500, soft-256)，不低于 200
var safeInflight int = 800

func checkNoFileLimit() {
	soft, err := nofileSoft()
	if err != nil {
		log.Printf("[agent] 警告：读取 RLIMIT_NOFILE 失败（%v），按 nofile=1024 保守处理", err)
		safeInflight = 700
		return
	}
	switch {
	case soft < 2048:
		safeInflight = soft - 256
		if safeInflight < 200 {
			safeInflight = 200
		}
		log.Printf("[agent] ⚠️ nofile=%d，高并发会触发假阴性风险（fd 耗尽的探针会被误判为端口关闭）；"+
			"已将扫描在途上限钳到 %d。生产环境请 `ulimits.nofile: 65535`", soft, safeInflight)
	case soft >= 8192:
		safeInflight = 1500
		log.Printf("[agent] nofile=%d ≥ 8192，扫描在途上限按 %d 运行（足以跑满 800+ pps）", soft, safeInflight)
	default:
		safeInflight = soft - 256
		if safeInflight > 1500 {
			safeInflight = 1500
		}
		log.Printf("[agent] nofile=%d，扫描在途上限按 %d 运行", soft, safeInflight)
	}
}

// clampInflight 把任务/节点配置的并发钳到本机安全在途上限。
func clampInflight(conc int) int {
	if conc <= 0 || conc > safeInflight {
		return safeInflight
	}
	return conc
}

// applyQueueCtrl 把任务级调控状态投影到节点共享 limiter（幂等，可每轮调用）。
func applyQueueCtrl(c ctrlCmd) {
	ctrl := queueLimiterQueueCtrl()
	if ctrl == nil {
		return
	}
	switch {
	case c.Paused || c.Action == "pause":
		ctrl.Pause()
	case c.Concurrency > 0 || c.TimeoutMs > 0:
		if c.Concurrency > 0 {
			ctrl.SetLimit(clampInflight(c.Concurrency))
		}
		if c.TimeoutMs > 0 {
			ctrl.SetTimeoutMs(c.TimeoutMs)
		}
		ctrl.Resume() // 产品语义：设了新值就按新值跑（顺带解除暂停）
	}
}

// queueLimiterQueueCtrl 取已初始化的节点级 limiter（未初始化返回 nil）。
func queueLimiterQueueCtrl() *portscan.Controls {
	queueCtrlMu.Lock()
	defer queueCtrlMu.Unlock()
	return queueCtrlInst
}

func queueLoop() {
	for {
		consumed := consumeQueue()
		if consumed > 0 {
			continue // 有活就立刻续轮，直到领空
		}
		// 没活：睡 3 秒或被唤醒信号踢醒（SSE wake / 旧通道拉任务也踢）
		select {
		case <-queueWake:
		case <-time.After(3 * time.Second):
		}
	}
}

// queueWake 唤醒队列消费循环（SSE 信号与本机事件共用）。
var queueWake = make(chan struct{}, 1)

// auditJanitor 定期清理审计库里"会话已结束"的单元 Project 行。
//
// 背景：队列模式下每个 IP 单元用自己的 Project（agent-q-<sessionID>-<ip>）落审计库，
// 任务结束/删除后这些行没有任何人删（v0.0.27 之前 ClearAuditProject 全仓零调用），
// config.db 会随扫描量无界增长直到撑爆节点磁盘。这里每 5 分钟对一次账：
// 以服务端仍在途的队列会话为准，其余 "agent-q-" 前缀的 Project 整条清掉。
func auditJanitor() {
	if !scanner.AuditEnabled() {
		return
	}
	tk := time.NewTicker(5 * time.Minute)
	defer tk.Stop()
	for {
		select {
		case <-tk.C:
		}
		pruneStaleAuditProjects()
	}
}

// pruneStaleAuditProjects 对账并清理一次；返回被清掉的 Project（便于日志/排查）。
func pruneStaleAuditProjects() []string {
	keep := liveQueueSessions()
	removed := scanner.PruneAuditProjects("agent-q-", keep)
	if len(removed) > 0 {
		log.Printf("[审计库] 清理已结束会话的 Project %d 个（保留在途会话 %d 个）", len(removed), len(keep))
	}
	return removed
}

// liveQueueSessions 本节点当前仍可消费的队列会话集合（拿不到服务端列表时保守返回 nil：
// 保守 = 一个都不删，宁可多留一轮也不误删在途任务的审计数据）。
func liveQueueSessions() map[uint]bool {
	resp, err := agentReq("GET", fmt.Sprintf("%s/api/agent/nodes/%d/queue-tasks", *serverURL, nodeID), nil)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var payload struct {
		Tasks []queueTaskRef `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil
	}
	keep := make(map[uint]bool, len(payload.Tasks))
	for _, t := range payload.Tasks {
		if t.SessionID > 0 {
			keep[t.SessionID] = true
		}
	}
	return keep
}

func kickQueue() {
	select {
	case queueWake <- struct{}{}:
	default:
	}
}

// consumeQueue 扫一遍可领任务；返回本轮实际消费（领取并执行）的单元数。
func consumeQueue() int {
	resp, err := agentReq("GET", fmt.Sprintf("%s/api/agent/nodes/%d/queue-tasks", *serverURL, nodeID), nil)
	if err != nil {
		return 0
	}
	var payload struct {
		Tasks []queueTaskRef `json:"tasks"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	resp.Body.Close()

	K := queueUnitConcurrency()
	consumed := 0
	for _, t := range payload.Tasks {
		if !t.Claimable {
			continue
		}
		done := make(chan struct{}, K)
		inflight := 0
		stopped := false // 服务端取消该任务 → 不再补位，等在途跑完即退
		for {
			if stopped && inflight == 0 {
				break
			}
			want := K - inflight
			if want <= 0 {
				<-done // 等任一单元收工再补位
				inflight--
				continue
			}
			c, err := claimUnits(t.TaskID, want)
			if err != nil {
				log.Printf("[queue] 任务 #%d 领取失败: %v", t.TaskID, err)
				if inflight > 0 { // 别空转打服务端：没活就等在途收工
					<-done
					inflight--
					continue
				}
				break
			}
			// node 级持久投影：每轮把任务当前的 pause/limit 状态应用到共享 limiter。
			// 这样 K=1 与 K>1 语义一致，也不依赖"恰好有单元在心跳"（v0.0.27 实测缺陷）。
			applyQueueCtrl(c.Cmd)
			units := c.unitList()
			if len(units) == 0 {
				if c != nil && c.Paused {
					log.Printf("[queue] 任务 #%d 已暂停，停领新单元（在途单元跑完即止）", t.TaskID)
				}
				if inflight > 0 {
					<-done
					inflight--
					continue
				}
				break
			}
			for i := range units {
				u := units[i]
				inflight++
				go func(u claimUnitRef) {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("[queue] 单元 %s 执行异常: %v", u.IP, r)
						}
						done <- struct{}{}
					}()
					if runQueueUnit(t.TaskID, c, u) {
						stopped = true // 取消：多 worker 下幂等——在途跑完就不再补位
					}
				}(u)
			}
			consumed += len(units)
		}
		for inflight > 0 { // 收尾：等在途全部落盘
			<-done
			inflight--
		}
		if stopped {
			log.Printf("[queue] 任务 #%d 已被取消，退出领取循环", t.TaskID)
		}
	}
	return consumed
}

// claimUnits 一次批量领取最多 n 个单元（服务端一条 SQL 领完，SKIP LOCKED 原子）。
// n=1 时与旧的单领行为等价；老服务端不认 n 也没关系——它会回 1 个（unit 字段兜底）。
func claimUnits(taskID uint, n int) (*claimResp, error) {
	if n <= 0 {
		n = 1
	}
	body, _ := json.Marshal(map[string]interface{}{"nodeId": nodeID, "n": n})
	resp, err := agentReq("POST", fmt.Sprintf("%s/api/agent/tasks/%d/claim", *serverURL, taskID), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		log.Printf("[queue] claim 任务 #%d 被拒 HTTP %d: %s", taskID, resp.StatusCode, string(b))
		return nil, nil
	}
	var c claimResp
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// runQueueUnit 执行一个单 IP 单元（与 runAgentTask 同构：取消重试 StopScan、
// cmd 幂等投影、进度/结果流式上报；差别只在目标=1 个 IP 且心跳带 ip 续租）。
//
// 返回值 stopTask=true 表示服务端已取消该任务（停止/删除），调用方应立刻退出
// 该任务的领取循环，不要再 claim 下一个单元。
func runQueueUnit(taskID uint, c *claimResp, u claimUnitRef) (stopTask bool) {
	at := u.SessionID
	if at == 0 {
		at = c.Unit.SessionID
	}
	task := c.Task
	conf := c.NodeConf
	thread := conf.Concurrency
	if thread <= 0 {
		thread = 1000
	}
	timeout := conf.Timeout
	if timeout <= 0 {
		timeout = task.Timeout * 1000
		if timeout <= 0 {
			timeout = 1500
		}
	}
	ports := task.Ports
	switch task.PortMode {
	case "top100":
		ports = ""
	case "all":
		ports = "1-65535"
	}
	// ProjectID 单元级唯一：同会话串行执行不会重叠，但取消句柄按单元精确命中
	proj := fmt.Sprintf("agent-q-%d-%s", at, u.IP)
	opts := scanner.ScanOptions{
		ProjectID: proj,
		Modules:   task.Modules, Finger: task.Finger, Ports: ports,
		Thread: thread, Timeout: timeout, NoPing: task.NoPing, NoSmart: task.NoSmart,
		ExtraArgs: task.ExtraArgs,
		// K 路单元共享同一台机器的许可池：并发总量由它统一约束，
		// "/control limit" 与 pause 一次对所有在途单元生效（不会被后启动单元覆盖）。
		// 并发先按本机 nofile 钳一次，避免把机器推到 fd 墙（那会产出假阴性）。
		Ctrl: queueLimiter(clampInflight(thread), timeout),
	}
	log.Printf("[queue] 单元领取成功：任务 #%d IP=%s（探针 %d，会话 #%d）", taskID, u.IP, u.ProbesTotal, at)

	// 取消通道：心跳响应 cancelled=true → StopScan 重试直至命中句柄（预算 <5s）
	scanReturned := make(chan struct{})
	var closeOnce sync.Once
	closeReturned := func() { closeOnce.Do(func() { close(scanReturned) }) }
	defer closeReturned()
	doCancel := func() {
		go func() {
			for i := 0; i < 40; i++ {
				if scanner.StopScan(proj) {
					return
				}
				select {
				case <-scanReturned:
					return
				case <-time.After(150 * time.Millisecond):
				}
			}
		}()
	}

	var mu sync.Mutex
	scanned, total := 0, int(u.ProbesTotal)
	ppsVal := 0.0
	openFound, identified := 0, 0
	lastPct := -1
	finished := false
	cancelled := false

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(2 * time.Second)
		defer tk.Stop()
		appliedPaused := false
		n := 0
		for range tk.C {
			mu.Lock()
			done := finished
			s, t2, lp := scanned, total, lastPct
			pp, of, idf := ppsVal, openFound, identified
			mu.Unlock()
			if done {
				return
			}
			pct := 0
			if lp >= 0 {
				pct = lp
			} else if t2 > 0 {
				pct = s * 100 / t2
			}
			if pct > 100 {
				pct = 100
			}
			n++
			r := reportUnitProgress(at, taskID, u.IP, s, t2, pp, of, idf, pct, n%2 == 1)
			if r.Cancelled {
				log.Printf("[queue] 会话 #%d 收到取消：停掉单元 %s 的扫描并退出领取循环", at, u.IP)
				mu.Lock()
				cancelled = true
				finished = true
				mu.Unlock()
				doCancel()
				return
			}
			cc := r.Cmd
			switch {
			case cc.Paused || cc.Action == "pause":
				if scanner.ControlScan(proj, scanner.ScanCtl{Action: "pause"}) && !appliedPaused {
					log.Printf("[queue] 会话 #%d 调控：扫描已暂停", at)
					appliedPaused = true
				}
			case cc.Action == "limit":
				if scanner.ControlScan(proj, scanner.ScanCtl{Action: "limit", Concurrency: cc.Concurrency, TimeoutMs: cc.TimeoutMs}) && appliedPaused {
					log.Printf("[queue] 会话 #%d 调控：限速 %d/%dms 并解除暂停", at, cc.Concurrency, cc.TimeoutMs)
					appliedPaused = false
				}
			default:
				if appliedPaused {
					if scanner.ControlScan(proj, scanner.ScanCtl{Action: "resume"}) {
						log.Printf("[queue] 会话 #%d 调控：扫描已恢复", at)
						appliedPaused = false
					}
				}
			}
		}
	}()

	results, err := scanner.RunPortScan([]string{u.IP}, fetchExcludes(), opts,
		func(line string, isProgress bool) {
			if isProgress {
				if m := portRe.FindStringSubmatch(line); len(m) == 3 {
					if a, e1 := strconv.Atoi(m[1]); e1 == nil {
						if b, e2 := strconv.Atoi(m[2]); e2 == nil && b > 0 {
							mu.Lock()
							if b > total {
								total = b
							}
							if a > scanned && a <= total {
								scanned = a
							}
							mu.Unlock()
						}
					}
				}
				if m := ppsRe.FindStringSubmatch(line); len(m) == 2 {
					if v, e := strconv.ParseFloat(m[1], 64); e == nil {
						mu.Lock()
						ppsVal = v
						mu.Unlock()
					}
				}
				if m := openRe.FindStringSubmatch(line); len(m) == 3 {
					a, _ := strconv.Atoi(m[1])
					b, _ := strconv.Atoi(m[2])
					mu.Lock()
					if a > openFound {
						openFound = a
					}
					if b > identified {
						identified = b
					}
					mu.Unlock()
				}
				if m := pctRe.FindStringSubmatch(line); len(m) == 2 {
					if v, e := strconv.Atoi(m[1]); e == nil {
						mu.Lock()
						if v > lastPct {
							lastPct = v
						}
						mu.Unlock()
					}
				}
			}
			// 带 [agent-task #会话] 前缀：服务端 updateShardLog 按该标记切分会话日志
			log.Printf("[agent-task #%d] [queue] %s", at, line)
		},
		func(batch []scanner.ScanResult) {
			for i := 0; i < len(batch); i += 200 {
				end := i + 200
				if end > len(batch) {
					end = len(batch)
				}
				postResults(taskID, batch[i:end])
			}
		})

	mu.Lock()
	fd, ft, of, idf, pp := scanned, total, openFound, identified, ppsVal
	if len(results) > of {
		of = len(results)
		idf = of
	}
	wasCancelled := cancelled
	finished = true
	mu.Unlock()
	wg.Wait()

	switch {
	case wasCancelled:
		// 取消：单元由服务端 StopTask 已回 pending；结果已随流 flush，这里只停不报。
		log.Printf("[queue] 单元 %s 已取消（部分结果 %d 条已流式上报）", u.IP, len(results))
	case err != nil:
		if errors.Is(err, portscan.ErrLocalResourceExhausted) {
			// 本机 fd/端口不够，这一轮的结果不可信：绝不能落 done（否则就是静默假阴性）
			log.Printf("[queue] ERROR 本地 fd/资源不足，单元 %s 结果不可信，已退回队列（请调大 nofile 或调小并发）：%v", u.IP, err)
		} else {
			log.Printf("[queue] 单元 %s 失败: %v（回池重试）", u.IP, err)
		}
		// 带 failed 终报让 reaper 逻辑即时回池（不等租约过期；迟到心跳命中不到 leased 会静默忽略）
		reportUnitFinal(at, taskID, u.IP, fd, ft, of, idf, pp, "failed")
	default:
		if fd == 0 {
			fd = ft // 快单元没赶上进度行：按满额计
		}
		reportUnitFinal(at, taskID, u.IP, fd, ft, of, idf, pp, "done")
		log.Printf("[queue] 单元 %s 完成：%d 探针 %d 条结果", u.IP, fd, len(results))
	}
	return wasCancelled
}

// reportUnitProgress 队列心跳（带 ip）：续租 + 单元读数；返回取消/调控指令。
func reportUnitProgress(sessionID, taskID uint, ip string, scanned, total int, pps float64, open, identified, pct int, withLog bool) progressResp {
	payload := map[string]interface{}{
		"ip": ip, "probesDone": scanned, "probesTotal": total, "pps": pps,
		"openFound": open, "identified": identified,
		"total": total, "scanned": scanned, "progress": pct, "message": "扫描中",
	}
	if withLog {
		payload["log"] = logRing.Snapshot()
	}
	body, _ := json.Marshal(payload)
	resp, err := agentReq("POST", fmt.Sprintf("%s/api/agent/tasks/%d/progress", *serverURL, sessionID), body)
	if err != nil {
		return progressResp{}
	}
	defer resp.Body.Close()
	var r progressResp
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return r
}

// reportUnitFinal 单元终报（done/failed）。
func reportUnitFinal(sessionID, taskID uint, ip string, done, total int, open, identified int, pps float64, status string) {
	payload := map[string]interface{}{
		"ip": ip, "probesDone": done, "probesTotal": total, "pps": pps,
		"openFound": open, "identified": identified,
		"unitDone": status == "done", "unitStatus": status,
		"total": total, "scanned": done, "progress": 100, "message": "单元" + status,
	}
	body, _ := json.Marshal(payload)
	resp, err := agentReq("POST", fmt.Sprintf("%s/api/agent/tasks/%d/progress", *serverURL, sessionID), body)
	if err == nil {
		resp.Body.Close()
	}
}

// countPortsIn 估算端口数量：空=Top100；支持 "1-65535"、"80,443"、"1-1024,3389"
func countPortsIn(ports string) int {
	ports = strings.TrimSpace(ports)
	if ports == "" {
		return 100 // 默认 Top100
	}
	n := 0
	for _, seg := range strings.Split(ports, ",") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if i := strings.Index(seg, "-"); i > 0 {
			a, e1 := strconv.Atoi(strings.TrimSpace(seg[:i]))
			b, e2 := strconv.Atoi(strings.TrimSpace(seg[i+1:]))
			if e1 == nil && e2 == nil && a > 0 && b >= a {
				n += b - a + 1
			}
		} else if _, err := strconv.Atoi(seg); err == nil {
			n++
		}
	}
	if n == 0 {
		return 100
	}
	return n
}

func localIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "unknown"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func register() error {
	// 已在本机保存过节点 ID + 长期令牌：直接复用，跳过注册
	// （加入令牌是一次性的，重启后不能再用）
	if nodeID > 0 && nodeToken != "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"name": *nodeName, "ip": localIP(), "token": *token, "version": myVersion})
	resp, err := http.Post(*serverURL+"/api/nodes/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data))
	}
	var node struct {
		ID    uint   `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &node); err != nil {
		return fmt.Errorf("解析注册响应失败: %s", string(data))
	}
	if node.ID == 0 {
		return fmt.Errorf("响应中缺少节点 ID: %s", string(data))
	}
	nodeID = node.ID
	if node.Token != "" {
		saveNodeToken(node.ID, node.Token)
	}
	return nil
}

func heartbeatLoop() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		doHeartbeat()
	}
}

// doHeartbeat 心跳上报版本，并按服务端指令自更新/卸载。
// 心跳还带上审计库的 project 列表（节点「数据库管理」用）。
func doHeartbeat() {
	var projects []string
	if list, err := scanner.ListProjects(scanner.EngineDBPath()); err == nil {
		projects = list
	}
	if projects == nil {
		projects = []string{}
	}
	body, _ := json.Marshal(map[string]interface{}{"version": myVersion, "projects": projects})
	resp, err := agentReq("POST", fmt.Sprintf("%s/api/nodes/%d/heartbeat", *serverURL, nodeID), body)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// 节点在中心已被删除时，本地凭据即失效：
	// 清掉凭据并退出，由 systemd / 计划任务带着 -token 重启完成重新注册。
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		log.Printf("[agent] 凭据已失效（HTTP %d），节点可能已在中心被删除，正在清除本地凭据并重新注册", resp.StatusCode)
		_ = os.Remove(agentStateFile)
		_ = os.Remove(legacyTokenFile)
		os.Exit(2)
	}
	var r struct {
		Upgrade       bool   `json:"upgrade"`
		Uninstall     bool   `json:"uninstall"`
		LatestVersion string `json:"latestVersion"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&r)
	// UpgradeReq=true 就强制重新下载（即使版本号相同，也允许用户手动重刷控制文件）
	if r.Upgrade && r.LatestVersion != "" {
		log.Printf("[agent] 收到升级指令 -> %s（当前 %s）", r.LatestVersion, myVersion)
		selfUpgrade(r.LatestVersion)
	}
	// Uninstall=true → 清理并退出
	if r.Uninstall {
		log.Printf("[agent] 收到卸载指令，开始清理...")
		selfUninstall()
	}
}

// selfUninstall 自清理：停止进程 + 删除服务/计划任务 + 删除文件
func selfUninstall() {
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)

	if runtime.GOOS == "windows" {
		// 写清理脚本到临时目录（不被安装目录删除影响）
		cleanup := os.Getenv("TEMP") + `\weijian-cleanup.ps1`
		script := fmt.Sprintf(`# 自清理脚本（由 scope-agent 生成）
Start-Sleep -Seconds 2
$Dir = "%s"
# 关键：先把当前目录切到 TEMP。否则进程的 CWD 就在安装目录里，
# Remove-Item 会因为"目录正在使用"而静默失败（实测残留 scope-agent.exe）。
Set-Location -Path $env:TEMP
taskkill /F /IM scope-agent.exe 2>$null
schtasks /Delete /TN "WeijianAgent" /F 2>$null
schtasks /Delete /TN "WeijianAgentWatchdog" /F 2>$null
# 只删 agent 自己的文件，用户文件保留
foreach ($f in @("scope-agent.exe", "start-agent.bat", "agent.json", "agent.token")) {
    Remove-Item -Force -LiteralPath (Join-Path $Dir $f) -ErrorAction SilentlyContinue
}
# 目录空了才删；非空说明还有用户的文件，保留目录
if ((Get-ChildItem -LiteralPath $Dir -Force -ErrorAction SilentlyContinue | Measure-Object).Count -eq 0) {
    Remove-Item -Force -LiteralPath $Dir -ErrorAction SilentlyContinue
}
Remove-Item -Force -LiteralPath $PSCommandPath -ErrorAction SilentlyContinue
`, dir)
		os.WriteFile(cleanup, []byte(script), 0644)

		// 启动清理脚本（后台）；CWD 设在 TEMP，避免占用安装目录
		cmd := exec.Command("powershell.exe", "-WindowStyle", "Hidden", "-NoProfile",
			"-ExecutionPolicy", "Bypass", "-File", cleanup)
		cmd.Dir = os.Getenv("TEMP")
		cmd.Start()
		log.Printf("[agent] 清理脚本已启动（Windows），退出")
	} else {
		// Linux/Mac：写清理脚本到 /tmp
		cleanup := "/tmp/weijian-cleanup.sh"
		script := fmt.Sprintf(`#!/bin/bash
# 自清理脚本（由 scope-agent 生成）
DIR="%s"
cd / || cd /tmp
sleep 2
systemctl stop weijian-agent 2>/dev/null
systemctl disable weijian-agent 2>/dev/null
rm -f /etc/systemd/system/weijian-agent.service
systemctl daemon-reload 2>/dev/null
rm -f "$DIR/scope-agent" "$DIR/start-agent.sh" "$DIR/agent.json" "$DIR/agent.token"
rmdir "$DIR" 2>/dev/null
rm -f /tmp/weijian-cleanup.sh
`, dir)
		os.WriteFile(cleanup, []byte(script), 0755)

		// 启动清理脚本（后台）；CWD 切到 /，否则删不掉 agent 所在的目录
		cmd := exec.Command("bash", cleanup)
		cmd.Dir = "/"
		cmd.Start()
		log.Printf("[agent] 清理脚本已启动（Linux），退出")
	}
	os.Exit(0)
}

// streamLoop SSE 长连接：服务端有任务时唤醒，避免空轮询
func streamLoop() {
	for {
		url := fmt.Sprintf("%s/api/agent/stream/%d", *serverURL, nodeID)
		resp, err := agentReq("GET", url, nil)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "event: wake") {
				log.Printf("[agent] 收到唤醒信号，拉取任务")
				go fetchAndRun()
				go kickQueue() // 队列模式：同样踢醒领取循环
			}
		}
		resp.Body.Close()
		time.Sleep(3 * time.Second) // 断线重连
	}
}

// fetchAndRun 拉取分配给本节点的待执行子任务
func fetchAndRun() {
	url := fmt.Sprintf("%s/api/agent/nodes/%d/tasks", *serverURL, nodeID)
	resp, err := agentReq("GET", url, nil)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var list []AgentTask
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return
	}
	for _, at := range list {
		if _, loaded := running.LoadOrStore(at.ID, &shardRun{}); loaded {
			continue // 已在执行
		}
		go runAgentTask(at)
	}
}

// runAgentTask 执行子任务。
//
// 控制面（v0.0.27）：
//   - 取消：每次心跳响应带 cancelled=true → taskCtx cancel → 引擎立即收敛，
//     已拿到的部分结果照常 flush 上报后 reportFinish；
//   - cmd 字段为调控通道骨架（当前恒为 none；运行期调速/暂停已按评审移除）。
func runAgentTask(at AgentTask) {
	defer running.Delete(at.ID)

	// 清理指令：删除审计库的指定 project
	if strings.HasPrefix(at.Message, "cleanup:") {
		var projects []string
		_ = json.Unmarshal([]byte(strings.TrimPrefix(at.Message, "cleanup:")), &projects)
		if len(projects) > 0 {
			dbPath := scanner.EngineDBPath()
			if err := scanner.DeleteProjects(dbPath, projects); err == nil {
				log.Printf("[agent-task #%d] 已清理 %d 个 project: %v", at.ID, len(projects), projects)
			} else {
				log.Printf("[agent-task #%d] 清理失败: %v", at.ID, err)
			}
		}
		reportFinish(at.ID, true)
		return
	}

	log.Printf("[agent-task #%d] 开始：%s", at.ID, at.IPRanges)

	// 拉取任务配置 + 节点参数
	var detail taskDetail
	dResp, err := agentReq("GET", fmt.Sprintf("%s/api/agent/tasks/%d", *serverURL, at.ID), nil)
	if err != nil {
		log.Printf("[agent-task #%d] 拉详情失败: %v", at.ID, err)
		reportFinish(at.ID, false)
		return
	}
	_ = json.NewDecoder(dResp.Body).Decode(&detail)
	dResp.Body.Close()

	task := detail.Task
	conf := detail.NodeConf
	thread := conf.Concurrency
	if thread <= 0 {
		thread = 1000
	}
	timeout := conf.Timeout
	if timeout <= 0 {
		// 兜底用任务的 timeout。任务的 timeout 是「秒」，引擎按毫秒处理
		timeout = task.Timeout * 1000
		if timeout <= 0 {
			timeout = 1500
		}
	}

	targets := parseTargets(at.IPRanges)
	ports := task.Ports
	switch task.PortMode {
	case "top100":
		ports = ""
	case "all":
		ports = "1-65535"
	}

	opts := scanner.ScanOptions{
		ProjectID: fmt.Sprintf("agent-%d", at.ID),
		Modules:   task.Modules, Finger: task.Finger, Ports: ports,
		Thread: thread, Timeout: timeout, NoPing: task.NoPing, NoSmart: task.NoSmart,
		ExtraArgs: task.ExtraArgs,
	}

	// 取消 = 唯一入口 scanner.StopScan(opts.ProjectID)（评审拍板，不给 RunPortScan 塞 ctx）。
	// 引擎句柄在 RunBuiltinPortScan 入口即注册；心跳早于注册时靠重试兜底，
	// 平台取消→agent 生效预算 <5s（重试每 200ms；扫描返回后重试自动停止）。
	scanReturned := make(chan struct{})
	var closeOnce sync.Once
	closeReturned := func() { closeOnce.Do(func() { close(scanReturned) }) }
	defer closeReturned()
	doCancel := func() {
		go func() {
			for i := 0; i < 60; i++ {
				if scanner.StopScan(opts.ProjectID) {
					return // 句柄命中，引擎正在收敛
				}
				select {
				case <-scanReturned:
					return // 扫描已结束（可能压根没注册过句柄），无需再停
				case <-time.After(200 * time.Millisecond):
				}
			}
		}()
	}
	h := &shardRun{cancel: doCancel}
	if v, ok := running.Load(at.ID); ok {
		v.(*shardRun).cancel = doCancel
		h = v.(*shardRun)
	} else {
		running.Store(at.ID, h)
	}

	var mu sync.Mutex
	// total 的初始值＝IP 数 × 端口数（端口维度，与平台分母口径一致）。
	scanned, total := 0, len(targets)*countPortsIn(opts.Ports)
	lastPct := -1
	etaSec := 0
	finished := false
	cancelledByServer := false
	reportCount := 0

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(2 * time.Second)
		defer tk.Stop()
		appliedPaused := false // 本地已应用的暂停态（与服务端投影对齐，避免重复打日志）
		for range tk.C {
			mu.Lock()
			done := finished
			s, t, lp, eta := scanned, total, lastPct, etaSec
			mu.Unlock()
			if done {
				return
			}
			pct := 0
			if lp >= 0 {
				pct = lp
			} else if t > 0 {
				pct = s * 100 / t
			}
			if pct > 100 {
				pct = 100
			}
			reportCount++
			cmd := reportProgressWithLog(at.ID, t, s, pct, eta, "扫描中", reportCount%2 == 1)
			if cmd.Cancelled {
				log.Printf("[agent-task #%d] 收到取消指令，终止扫描（保留部分结果）", at.ID)
				mu.Lock()
				cancelledByServer = true
				finished = true
				mu.Unlock()
				doCancel() // 唯一取消入口：重试 StopScan 直至命中引擎句柄
				return
			}
			// 应用调控指令（幂等状态投影 → ControlScan；句柄未注册时 false，下条心跳再试）
			c := cmd.Cmd
			switch {
			case c.Paused || c.Action == "pause":
				if scanner.ControlScan(opts.ProjectID, scanner.ScanCtl{Action: "pause"}) && !appliedPaused {
					log.Printf("[agent-task #%d] 调控：扫描已暂停（许可清零，在途探针自然结束）", at.ID)
					appliedPaused = true
				}
			case c.Action == "limit":
				if scanner.ControlScan(opts.ProjectID, scanner.ScanCtl{
					Action: "limit", Concurrency: c.Concurrency, TimeoutMs: c.TimeoutMs}) && appliedPaused {
					log.Printf("[agent-task #%d] 调控：限速 %d/%dms 并解除暂停", at.ID, c.Concurrency, c.TimeoutMs)
					appliedPaused = false
				}
			default: // none/resume：若本地处于暂停态，恢复
				if appliedPaused {
					if scanner.ControlScan(opts.ProjectID, scanner.ScanCtl{Action: "resume"}) {
						log.Printf("[agent-task #%d] 调控：扫描已恢复", at.ID)
						appliedPaused = false
					}
				}
			}
		}
	}()

	// 流式扫描：onResult 每批（≤200 条）实时回传；引擎内部已按批回调，这里直接转发
	results, err := scanner.RunPortScan(targets, fetchExcludes(), opts, func(line string, isProgress bool) {
		if isProgress {
			// 引擎进度行格式：「(done/total) ... [已用:剩余]」
			if m := portRe.FindStringSubmatch(line); len(m) == 3 {
				if a, err1 := strconv.Atoi(m[1]); err1 == nil {
					if b, err2 := strconv.Atoi(m[2]); err2 == nil && b > 0 {
						mu.Lock()
						if b > total {
							total = b
						}
						if a > scanned && a <= total {
							scanned = a
						}
						mu.Unlock()
					}
				}
			}
			if m := pctRe.FindStringSubmatch(line); len(m) == 2 {
				if v, err := strconv.Atoi(m[1]); err == nil {
					mu.Lock()
					if v > lastPct {
						lastPct = v
					}
					mu.Unlock()
				}
			}
			if m := etaRe.FindStringSubmatch(line); len(m) == 2 {
				if sec := parseDurationSec(m[1]); sec > 0 {
					mu.Lock()
					etaSec = sec
					mu.Unlock()
				}
			}
		}
		log.Printf("[agent-task #%d] %s", at.ID, line)
	}, func(batch []scanner.ScanResult) {
		// 实时回传（引擎聚合器已按 ≤200 条分批，这里再兜一层切片防超限）
		for i := 0; i < len(batch); i += 200 {
			end := i + 200
			if end > len(batch) {
				end = len(batch)
			}
			postResults(at.TaskID, batch[i:end])
		}
	})

	mu.Lock()
	finished = true
	wasCancelled := cancelledByServer
	mu.Unlock()
	wg.Wait()

	if err != nil {
		if wasCancelled || strings.Contains(err.Error(), "context canceled") {
			// 取消：**不报 finish、不改分片状态**（平台 StopTask 已把分片置 cancelled、
			// 主任务置 stopped，agent 只负责停扫描并收尾；补报会把 cancelled 判成 done）。
			// 部分结果照常上传：引擎回调里已 flush 的批次不重发（results 与回调同源）。
			log.Printf("[agent-task #%d] 已取消，保留 %d 条部分结果（已随流式回调上报）", at.ID, len(results))
			return
		}
		log.Printf("[agent-task #%d] 失败: %v", at.ID, err)
		reportFinish(at.ID, false)
		return
	}
	reportProgressPlain(at.ID, total, total, 100, "完成")
	reportFinish(at.ID, true)
	log.Printf("[agent-task #%d] 完成，%d 条结果（实时流式已上报）", at.ID, len(results))
}

// progressResp 心跳响应：取消标记 + 调控指令（幂等状态）
type progressResp struct {
	Cancelled bool    `json:"cancelled"`
	Cmd       ctrlCmd `json:"cmd"`
}

// reportProgressWithLog 上报进度；withLog=true 时附带最近日志。返回服务端指令。
func reportProgressWithLog(id uint, total, scanned, pct, eta int, msg string, withLog bool) progressResp {
	payload := map[string]interface{}{
		"total": total, "scanned": scanned, "progress": pct, "eta": eta, "message": msg,
	}
	if withLog {
		payload["log"] = logRing.Snapshot()
	}
	body, _ := json.Marshal(payload)
	resp, err := agentReq("POST", fmt.Sprintf("%s/api/agent/tasks/%d/progress", *serverURL, id), body)
	if err != nil {
		return progressResp{}
	}
	defer resp.Body.Close()
	var r progressResp
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return r
}

// reportProgressPlain 简单进度上报（收尾用）
func reportProgressPlain(id uint, total, scanned, pct int, msg string) {
	body, _ := json.Marshal(map[string]interface{}{
		"total": total, "scanned": scanned, "progress": pct, "eta": 0, "message": msg,
	})
	resp, err := agentReq("POST", fmt.Sprintf("%s/api/agent/tasks/%d/progress", *serverURL, id), body)
	if err == nil {
		resp.Body.Close()
	}
}

func reportFinish(id uint, ok bool) {
	// 带上最终日志，避免 terminal_log 停留在最后一次 withLog=true 的时刻（比如 16%）
	body, _ := json.Marshal(map[string]interface{}{"ok": ok, "log": logRing.Snapshot()})
	resp, err := agentReq("POST", fmt.Sprintf("%s/api/agent/tasks/%d/finish", *serverURL, id), body)
	if err == nil {
		resp.Body.Close()
	}
}

var resultClient = &http.Client{Timeout: 120 * time.Second}

func postResults(taskID uint, results []scanner.ScanResult) {
	if len(results) == 0 {
		return
	}
	body, _ := json.Marshal(map[string]interface{}{"taskId": taskID, "results": results})
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/nodes/%d/results", *serverURL, nodeID), bytes.NewReader(body))
	if err != nil {
		log.Printf("[agent] 结果上报请求构造失败: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if nodeToken != "" {
		req.Header.Set("X-Node-Token", nodeToken)
	}
	resp, err := resultClient.Do(req)
	if err != nil {
		log.Printf("[agent] 结果上报失败（%d 条）: %v", len(results), err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		log.Printf("[agent] 结果上报被拒绝（HTTP %d）: %s", resp.StatusCode, string(b))
	}
}

var (
	// 进度条形如：进度 21% (283200/1279104) [3m4s:10m23s]
	pctRe  = regexp.MustCompile(`(\d{1,3})\s*%`)
	portRe = regexp.MustCompile(`\((\d+)\s*/\s*(\d+)\)`)
	// 队列单元心跳抽取：速率与发现数（emitProgress 行格式，与 builtin.go 对齐）
	ppsRe  = regexp.MustCompile(`([\d.]+)探针/秒`)
	openRe = regexp.MustCompile(`开放 (\d+) 已识别 (\d+)`)
	// 进度条尾部 [已用:剩余]，如 [2m36s:10m54s]
	etaRe = regexp.MustCompile(`\[([0-9hms]+):([0-9hms]+)\]`)
	durRe = regexp.MustCompile(`(\d+)([hms])`)
)

// parseDurationSec 把 10m54s / 1h2m3s / 45s 转成秒
func parseDurationSec(s string) int {
	total := 0
	for _, m := range durRe.FindAllStringSubmatch(s, -1) {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "h":
			total += n * 3600
		case "m":
			total += n * 60
		case "s":
			total += n
		}
	}
	return total
}

func parseTargets(input string) []string {
	fields := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ';' || r == ' '
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// fetchExcludes 从中心拉取免扫描清单
func fetchExcludes() []string {
	resp, err := http.Get(*serverURL + "/api/excludes")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var items []struct {
		IPRange string `json:"ipRange"`
	}
	_ = json.Unmarshal(data, &items)
	var out []string
	for _, it := range items {
		if it.IPRange != "" {
			out = append(out, it.IPRange)
		}
	}
	return out
}
