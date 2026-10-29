package main

import (
	"runtime"
	"asset-platform/internal/version"
	"path/filepath"
	"syscall"
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"asset-platform/internal/scanner"
)

var (
	serverURL = flag.String("server", "", "中心服务地址，如 http://10.1.1.1:8080")
	nodeName  = flag.String("name", "", "节点名称（唯一）")
	token     = flag.String("token", "default-token", "注册 token")
	tscanPath = flag.String("tscan", "", "TscanClient 二进制路径（默认同目录查找）")
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
	Message  string `json:"message"` // "cleanup:[...]" = 清理 config.db 的 project
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

var running sync.Map

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

// selfVersion 自身二进制的版本（SHA256 前 8 位）
func selfVersion() string {
	// 版本号用数字型（0.0.1），不再用哈希
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

	// 备份旧文件后原子替换（Linux 允许覆盖运行中的文件）
	_ = os.Remove(exe + ".old")
	_ = os.Rename(exe, exe+".old")
	if err := os.Rename(tmp, exe); err != nil {
		log.Printf("[agent] 替换失败: %v，回滚", err)
		_ = os.Rename(exe+".old", exe)
		return
	}
	log.Printf("[agent] 控制文件已更新 %s -> %s，正在交接重启", myVersion, target)
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		log.Printf("[agent] 重启失败: %v（请手动重启）", err)
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
	// 所有日志带主机名前缀，便于在中心端区分是哪个节点在执行
	if hn, err := os.Hostname(); err == nil && hn != "" {
		log.SetPrefix("[" + hn + "] ")
		log.Printf("[agent] 启动，主机名=%s", hn)
	}
	if *serverURL == "" || *nodeName == "" {
		fmt.Println("用法: scope-agent -server http://中心IP:8080 -name 节点名 [-token xxx] [-tscan /path/to/TscanClient]")
		os.Exit(1)
	}
	*serverURL = strings.TrimRight(*serverURL, "/")
	if *tscanPath == "" {
		*tscanPath = findTscanClient()
	}
	if *tscanPath == "" {
		log.Println("[warn] 未找到 TscanClient，请用 -tscan 指定路径")
	}
	scanner.SetTscanClientPath(*tscanPath)

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
	go streamLoop()          // SSE 长连接：有任务时被唤醒
	go fetchAndRun()         // 启动时拉一次（处理重启前遗留任务）

	select {}
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

func findTscanClient() string {
	candidates := []string{
		"./TscanClient", "./TscanClient.exe",
		"./third_party/TscanClient", "./third_party/TscanClient_Linux_Amd64",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			abs, _ := os.Getwd()
			return abs + "/" + strings.TrimPrefix(p, "./")
		}
	}
	return ""
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

// doHeartbeat 心跳上报版本，并按服务端指令自更新
// syncLicense 拉取分配给本节点的 Tscan License 并写入 config/License.lic
func syncLicense() {
	if tscanPath == nil || *tscanPath == "" {
		return
	}
	url := fmt.Sprintf("%s/api/agent/nodes/%d/license", *serverURL, nodeID)
	resp, err := agentReq("GET", url, nil)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var d struct {
		Content string `json:"content"`
	}
	if json.NewDecoder(resp.Body).Decode(&d) != nil || strings.TrimSpace(d.Content) == "" {
		return
	}
	dir := filepath.Join(filepath.Dir(*tscanPath), "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	target := filepath.Join(dir, "License.lic")
	// 内容相同就不重写，避免每次心跳都落盘
	if cur, err := os.ReadFile(target); err == nil && string(cur) == d.Content {
		return
	}
	if err := os.WriteFile(target, []byte(d.Content), 0o644); err == nil {
		log.Printf("[license] 已写入 %s（%d 字节）", target, len(d.Content))
	}
}

func doHeartbeat() {
	// 心跳顺便带上 config.db 的 project 列表（节点「数据库管理」用）
	var projects []string
	if *tscanPath != "" {
		dbPath := filepath.Join(filepath.Dir(*tscanPath), "config", "config.db")
		if list, err := scanner.ListProjects(dbPath); err == nil {
			projects = list
		}
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
		LatestVersion string `json:"latestVersion"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&r)
	// UpgradeReq=true 就强制重新下载（即使版本号相同，也允许用户手动重刷控制文件）
	if r.Upgrade && r.LatestVersion != "" {
		log.Printf("[agent] 收到升级指令 -> %s（当前 %s）", r.LatestVersion, myVersion)
		selfUpgrade(r.LatestVersion)
	}
	syncLicense()
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
		if _, loaded := running.LoadOrStore(at.ID, true); loaded {
			continue // 已在执行
		}
		go runAgentTask(at)
	}
}

var (
	// 每个 IP 扫描结束都会打一行
	ipDoneRe = regexp.MustCompile(`Ip扫描结束[:：]\s*(\S+)`)
	// 进度条形如：端口扫描  21% [████░░] (283200/1279104) [3m4s:10m23s]
	pctRe    = regexp.MustCompile(`(\d{1,3})\s*%`)
	portRe   = regexp.MustCompile(`\((\d+)\s*/\s*(\d+)\)`)
	// 进度条尾部 [已用:剩余]，如 [2m36s:10m54s]
	etaRe = regexp.MustCompile(`\[[0-9hms]+:([0-9hms]+)\]`)
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

// runAgentTask 执行子任务，执行期间每 2 秒上报进度
func runAgentTask(at AgentTask) {
	defer running.Delete(at.ID)

	// 清理指令：删除 config.db 的指定 project
	if strings.HasPrefix(at.Message, "cleanup:") {
		var projects []string
		_ = json.Unmarshal([]byte(strings.TrimPrefix(at.Message, "cleanup:")), &projects)
		if len(projects) > 0 && *tscanPath != "" {
			dbPath := filepath.Join(filepath.Dir(*tscanPath), "config", "config.db")
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
		// 兜底用任务的 timeout。注意：任务的 timeout 是「秒」，TscanClient -time 是「毫秒」
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

	var mu sync.Mutex
	// total 的初始值＝IP 数 × 端口数。
	// 之前直接用 IP 数，Linux 上有进度条会被覆盖成真实端口数，
	// 但 Windows 走普通管道、拿不到进度条，total 就永远停在 IP 数，
	// 于是「6 个 IP」被显示成「待扫描 6 个端口」，扫完还立刻判定完成。
	scanned, total := 0, len(targets)*countPortsIn(opts.Ports)
	doneIPs := map[string]bool{}
	lastPct := -1
	etaSec := 0
	finished := false
	reportCount := 0

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(2 * time.Second)
		defer tk.Stop()
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
				pct = lp                     // 优先用 TscanClient 输出的百分比
			} else if t > 0 {
				pct = s * 100 / t
			}
			if pct > 100 {
				pct = 100
			}
			reportCount++
			if reportProgressWithLog(at.ID, t, s, pct, eta, "扫描中", reportCount%2 == 1) {
				log.Printf("[agent-task #%d] 收到取消指令，终止扫描", at.ID)
				scanner.StopScan(opts.ProjectID)
				mu.Lock()
				finished = true
				mu.Unlock()
				return
			}
		}
	}()

	results, err := scanner.RunPortScan(targets, fetchExcludes(), opts, func(line string, isProgress bool) {
		// TscanClient Free 模式没有进度条，用「Ip扫描结束:<ip>」统计已完成 IP 数
		if m := ipDoneRe.FindStringSubmatch(line); len(m) == 2 {
			mu.Lock()
			doneIPs[m[1]] = true
			// 注意：不要在这里覆盖 scanned —— scanned 是「端口维度」，由进度条 (a/b) 维护
			// 之前写成 scanned = len(doneIPs) 会把 25 万端口数打回 0，导致进度恒为 0
			mu.Unlock()
		}
		// 跳过存活探测阶段：它的 b 是「批大小」（如 10000），不是端口总数
		// 若不跳过，自定义端口任务会被污染成 total=10000（实际只有 2 个端口）
		if isProgress && !strings.Contains(line, "存活探测") {
			// 进度行里的 (已扫/总数) 是端口维度
			// TscanClient 多阶段切换时 b 会跳变（存活探测 10000 → 端口扫描 98 → 全量 1279104）
			// 两个值都取历史最大值，避免 total 被小阶段覆盖、进度回退
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
				// 不做区间过滤：早期算出的长耗时（如 91h）会随样本增加自然回落
				if sec := parseDurationSec(m[1]); sec > 0 {
					mu.Lock()
					etaSec = sec
					mu.Unlock()
				}
			}
		}
		// 原生输出：进度条每帧都原样打印，不做过滤
		log.Printf("[agent-task #%d] %s", at.ID, line)
	}, func(batch []scanner.ScanResult) {
		// 扫描过程中实时回传，按 200 条分批，避免单次请求过大超时
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
	mu.Unlock()
	wg.Wait()

	if err != nil {
		log.Printf("[agent-task #%d] 失败: %v", at.ID, err)
		reportFinish(at.ID, false)
		return
	}
	reportProgress(at.ID, total, total, 100, "完成")
	reportFinish(at.ID, true)
	// 分批上传，防止结果集过大导致 timeout
	for i := 0; i < len(results); i += 200 {
		end := i + 200
		if end > len(results) {
			end = len(results)
		}
		postResults(task.ID, results[i:end])
	}
	log.Printf("[agent-task #%d] 完成，%d 条结果", at.ID, len(results))
}

// reportProgressWithLog 上报进度；withLog=true 时附带最近日志
func reportProgressWithLog(id uint, total, scanned, pct, eta int, msg string, withLog bool) bool {
	payload := map[string]interface{}{
		"total": total, "scanned": scanned, "progress": pct, "eta": eta, "message": msg,
	}
	if withLog {
		payload["log"] = logRing.Snapshot()
	}
	body, _ := json.Marshal(payload)
	resp, err := agentReq("POST", fmt.Sprintf("%s/api/agent/tasks/%d/progress", *serverURL, id), body)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var r struct {
		Cancelled bool `json:"cancelled"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return r.Cancelled
}

// reportProgress 上报进度，返回 true 表示服务端要求取消
func reportProgress(id uint, total, scanned, pct int, msg string) bool {
	body, _ := json.Marshal(map[string]interface{}{
		"total": total, "scanned": scanned, "progress": pct, "eta": 0, "message": msg,
	})
	resp, err := agentReq("POST", fmt.Sprintf("%s/api/agent/tasks/%d/progress", *serverURL, id), body)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var r struct {
		Cancelled bool `json:"cancelled"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return r.Cancelled
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
