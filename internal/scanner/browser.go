package scanner

// 单例浏览器。
//
// 改造前：每次渲染/截图都 exec 一个 chromium 进程（--dump-dom / --screenshot），
// 单进程实测 200MB 上下，用完即退。三个问题：
//  1. 冷启动 1~2s、每次都要建临时 profile（/tmp/.org.chromium.Chromium.*），
//     被 timeout kill 时目录留在盘上，长期跑会持续吃空间；
//  2. 并发三路就是三个浏览器，7.9G 内存的机器上直接抢爆（历史审计：6 并发 72 次失败 61 次）；
//  3. crashpad 子进程没人回收，容器里留下一堆 <defunct>。
//
// 现在：全进程只维护**一个** chromium，通过 CDP（Chrome DevTools Protocol）驱动它开 tab；
// 连续 CHROME_IDLE_SEC 秒没有任务就把实例连同 profile 一起销毁，下个任务再拉起。
//
// 协议细节：--remote-debugging-port=0 时端口是动态的，chromium 会把「端口 / browser 路径」
// 写进 <profile>/DevToolsActivePort，读它拿 ws 地址即可。并发靠 tab 复用同一进程，
// 不再 n 份内存。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

const (
	defaultChromeIdle = 5 * time.Minute
	defaultTabLimit   = 2
	chromeStartWait   = 20 * time.Second
)

// chromeIdle 空闲多久销毁实例
func chromeIdle() time.Duration {
	if v := os.Getenv("CHROME_IDLE_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultChromeIdle
}

// chromeTabLimit 同时进行的页面数（同一实例多 tab）
func chromeTabLimit() int {
	if v := os.Getenv("CHROME_MAX_TABS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 8 {
			return n
		}
	}
	return defaultTabLimit
}

// ===== 实例管理 =====

type browserInst struct {
	cmd     *exec.Cmd
	wsURL   string
	profile string
	lastUse atomic.Int64
	dead    atomic.Bool
}

func (b *browserInst) touch() { b.lastUse.Store(time.Now().UnixNano()) }

var (
	brMu      sync.Mutex
	brInst    *browserInst
	brIdleRun sync.Once
)

func acquireBrowser() (*browserInst, error) {
	brMu.Lock()
	defer brMu.Unlock()
	if brInst != nil && !brInst.dead.Load() {
		brInst.touch()
		return brInst, nil
	}
	b, err := startBrowser()
	if err != nil {
		return nil, err
	}
	brInst = b
	brIdleRun.Do(func() { go idleWatcher() })
	return b, nil
}

// ShutdownBrowser 主动销毁（进程退出时调用）
func ShutdownBrowser() {
	brMu.Lock()
	b := brInst
	brInst = nil
	brMu.Unlock()
	if b != nil {
		b.close()
	}
}

// ResetBrowser 强制销毁当前实例，下次调用重建。
//
// 用途：AI 识别 worker 的超时看门狗。实测批量补识别时整条队列停摆——
// chromium 页面挂住后 CDP 调用迟迟不返回，3 个 worker 全被占死，
// 重启服务才恢复。销毁实例会让所有在途调用立即报「连接中断」并退出，
// 从而把 worker 还回来，代价只是这一条识别失败（下次还能重试）。
func ResetBrowser() {
	brMu.Lock()
	b := brInst
	brInst = nil
	brMu.Unlock()
	if b != nil {
		b.close()
	}
}

func dropBrowser(b *browserInst) {
	brMu.Lock()
	if brInst == b {
		brInst = nil
	}
	brMu.Unlock()
	b.close()
}

func idleWatcher() {
	iv := 20 * time.Second
	if d := chromeIdle(); d < iv {
		iv = d / 2
	}
	for {
		time.Sleep(iv)
		brMu.Lock()
		b := brInst
		if b == nil {
			brMu.Unlock()
			continue
		}
		if time.Since(time.Unix(0, b.lastUse.Load())) < chromeIdle() {
			brMu.Unlock()
			continue
		}
		brInst = nil
		brMu.Unlock()
		log.Printf("[browser] 空闲超时（%s），销毁 chromium 实例并清理 profile", chromeIdle())
		b.close()
	}
}

func startBrowser() (*browserInst, error) {
	bin := chromeBinary()
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("未找到 chromium（%s）", bin)
	}
	profile := os.Getenv("CHROME_PROFILE_DIR")
	if profile == "" {
		profile = filepath.Join(os.TempDir(), "cyberlook-chrome")
	}
	// 上次异常退出可能留下锁文件（SingletonLock），不清掉起不来
	_ = os.RemoveAll(profile)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, err
	}

	args := []string{
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
		"--disable-extensions", "--hide-scrollbars", "--mute-audio",
		"--window-size=1920,1080", "--force-device-scale-factor=1",
		"--ignore-certificate-errors", "--allow-running-insecure-content",
		// crashpad/breakpad 会额外拉子进程，容器里没人回收就是一堆 defunct
		"--disable-crash-reporter", "--disable-breakpad", "--no-first-run",
		"--no-default-browser-check", "--disable-background-networking",
		"--disable-sync", "--disable-translate", "--disable-default-apps",
		"--disable-features=IsolateOrigins,site-per-process",
		// 端口 0 = 让系统分配，真实地址写在 <profile>/DevToolsActivePort
		"--remote-debugging-port=0",
		"--remote-allow-origins=*",
		"--user-data-dir=" + profile,
		"about:blank",
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "HOME="+profile)
	setProcGroup(cmd)
	// 不接管道：接了没人读会写满缓冲区把 chromium 卡死。
	// stderr 落到 profile 目录里，启动失败时能带上真实原因。
	errFile, _ := os.Create(filepath.Join(profile, "chrome.err"))
	if errFile != nil {
		cmd.Stderr = errFile
	}
	if err := cmd.Start(); err != nil {
		if errFile != nil {
			errFile.Close()
		}
		return nil, err
	}
	if errFile != nil {
		errFile.Close() // 子进程已 dup 走，父进程不用留
	}

	port, path, err := waitDevToolsPort(filepath.Join(profile, "DevToolsActivePort"), cmd, chromeStartWait)
	if err != nil {
		terminateProc(cmd)
		_ = cmd.Wait()
		forceKillProc(cmd)
		_ = os.RemoveAll(profile)
		return nil, err
	}
	b := &browserInst{
		cmd:     cmd,
		wsURL:   fmt.Sprintf("ws://127.0.0.1:%d%s", port, path),
		profile: profile,
	}
	b.touch()
	log.Printf("[browser] chromium 已启动 pid=%d port=%d profile=%s", cmd.Process.Pid, port, profile)
	return b, nil
}

func waitDevToolsPort(file string, cmd *exec.Cmd, max time.Duration) (int, string, error) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if raws, err := os.ReadFile(file); err == nil {
			lines := strings.Split(strings.TrimSpace(string(raws)), "\n")
			if len(lines) >= 2 {
				port, _ := strconv.Atoi(strings.TrimSpace(lines[0]))
				path := strings.TrimSpace(lines[1])
				if port > 0 && strings.HasPrefix(path, "/devtools/") {
					return port, path, nil
				}
			}
		}
		// 进程真的退出了就别再等满 20 秒
		if cmd.Process != nil && !procAlive(cmd) {
			return 0, "", fmt.Errorf("chromium 启动后立即退出：%s", tailFile(file+".err", 400))
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, "", fmt.Errorf("chromium %s 内未就绪（DevToolsActivePort 未出现）：%s", max, tailFile(file+".err", 400))
}

// tailFile 取文件末尾若干字节用于报错
func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return "无输出"
	}
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}

func (b *browserInst) close() {
	if b.dead.Swap(true) {
		return
	}
	terminateProc(b.cmd)
	done := make(chan struct{})
	go func() { _ = b.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		forceKillProc(b.cmd) // 整组兜底，避免 renderer 子进程残留
	}
	_ = os.RemoveAll(b.profile)
}

// ===== CDP 连接 =====

type cdpMsg struct {
	ID        int             `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Error     *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type cdpConn struct {
	ws     *websocket.Conn
	mu     sync.Mutex
	seq    int
	wait   map[int]chan cdpMsg
	events chan cdpMsg
	closed bool
}

func dialCDP(wsURL string) (*cdpConn, error) {
	ws, err := websocket.Dial(wsURL, "", "http://127.0.0.1")
	if err != nil {
		return nil, err
	}
	ws.PayloadType = websocket.TextFrame
	c := &cdpConn{ws: ws, wait: map[int]chan cdpMsg{}, events: make(chan cdpMsg, 512)}
	go c.readLoop()
	return c, nil
}

func (c *cdpConn) readLoop() {
	for {
		var s string
		if err := websocket.Message.Receive(c.ws, &s); err != nil {
			c.mu.Lock()
			c.closed = true
			for id, ch := range c.wait {
				close(ch)
				delete(c.wait, id)
			}
			c.mu.Unlock()
			return
		}
		var m cdpMsg
		if json.Unmarshal([]byte(s), &m) != nil {
			continue
		}
		if m.ID > 0 {
			c.mu.Lock()
			ch := c.wait[m.ID]
			delete(c.wait, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		select {
		case c.events <- m:
		default: // 事件堆积时丢弃最旧语义不敏感的事件即可
		}
	}
}

func (c *cdpConn) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	_ = c.ws.Close()
}

func (c *cdpConn) call(ctx context.Context, method string, params any, session string) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("CDP 连接已关闭")
	}
	c.seq++
	id := c.seq
	ch := make(chan cdpMsg, 1)
	c.wait[id] = ch
	c.mu.Unlock()

	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	if session != "" {
		payload["sessionId"] = session
	}
	raw, _ := json.Marshal(payload)
	if err := websocket.Message.Send(c.ws, string(raw)); err != nil {
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("%s: 连接中断", method)
		}
		if m.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		return m.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.wait, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// waitEvent 等某个 CDP 事件（如 Page.loadEventFired）；超时由 ctx 控制
func (c *cdpConn) waitEvent(ctx context.Context, method, session string) {
	for {
		select {
		case m := <-c.events:
			if m.Method == method && (session == "" || m.SessionID == session) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// withPage 在常驻实例里开一个 tab，跑完关闭。fn 只在该 tab 的 session 内操作。
func withPage(ctx context.Context, url string, wait time.Duration, fn func(session string, c *cdpConn) error) error {
	b, err := acquireBrowser()
	if err != nil {
		return err
	}
	b.touch()
	c, err := dialCDP(b.wsURL)
	if err != nil {
		// 连不上说明实例已坏，销毁后由下次调用重建
		dropBrowser(b)
		return fmt.Errorf("连接 chromium 失败：%w", err)
	}
	defer c.Close()

	var created struct {
		TargetID string `json:"targetId"`
	}
	raw, err := c.call(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, "")
	if err != nil {
		// 浏览器级命令都失败 = 实例已经不健康，销毁，下个任务重建
		dropBrowser(b)
		return err
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.TargetID == "" {
		return fmt.Errorf("创建标签页失败")
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	raw, err = c.call(ctx, "Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true}, "")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &attached); err != nil || attached.SessionID == "" {
		return fmt.Errorf("附加标签页失败")
	}
	sid := attached.SessionID
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = c.call(cctx, "Target.closeTarget", map[string]any{"targetId": created.TargetID}, "")
		ccancel()
	}()

	if _, err := c.call(ctx, "Page.enable", nil, sid); err != nil {
		return err
	}
	if _, err := c.call(ctx, "Page.navigate", map[string]any{"url": url}, sid); err != nil {
		return err
	}
	// 等 load 事件；目标站点挂住也不死等，到点直接读 DOM（能读到多少算多少）
	lctx, cancel := context.WithTimeout(ctx, wait)
	c.waitEvent(lctx, "Page.loadEventFired", sid)
	cancel()

	// 自适应等 SPA 渲染完。
	//
	// 原来固定 sleep 600ms：对 Vue/React 的登录页远远不够——实测
	// 198.18.8.65（深信服超融合）在 600ms 时 body.innerText 还是空的，
	// 于是「渲染结果：正文摘要（空）」喂给模型，模型只能回「未知」，
	// 而页面 7 秒后其实写着「深信服超融合平台 / SANGFOR HCI」。
	//
	// 改成轮询可见文字长度：有内容且连续两次长度不变就立刻返回，
	// 既不会白等满 8 秒，也不会在页面还没渲染时就取样。
	deadline := time.Now().Add(wait)
	lastLen := -1
	stable := 0
	for time.Now().Before(deadline) {
		s, err := c.evalString(ctx, sid, "String(document.body ? document.body.innerText.length : 0)")
		if err == nil {
			if n, perr := strconv.Atoi(strings.TrimSpace(s)); perr == nil {
				if n > 50 {
					if n == lastLen {
						stable++
						if stable >= 2 {
							break
						}
					} else {
						stable = 0
					}
				}
				lastLen = n
			}
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if err := fn(sid, c); err != nil {
		return err
	}
	b.touch()
	return nil
}

// evalString 在页面里求值并取字符串结果
func (c *cdpConn) evalString(ctx context.Context, session, expr string) (string, error) {
	raw, err := c.call(ctx, "Runtime.evaluate", map[string]any{
		"expression": expr, "returnByValue": true, "awaitPromise": false,
	}, session)
	if err != nil {
		return "", err
	}
	var r struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	if r.Exception != nil && r.Exception.Text != "" {
		return "", fmt.Errorf("%s", r.Exception.Text)
	}
	if s, ok := r.Result.Value.(string); ok {
		return s, nil
	}
	return "", nil
}

// FetchHTML 打开页面并取渲染后的完整 DOM
func FetchHTML(ctx context.Context, url string, wait time.Duration) (string, error) {
	if wait <= 0 {
		wait = 8 * time.Second
	}
	var html string
	err := withPage(ctx, url, wait, func(session string, c *cdpConn) error {
		s, err := c.evalString(ctx, session, "document.documentElement.outerHTML")
		if err != nil {
			return err
		}
		html = s
		return nil
	})
	return html, err
}

// CaptureScreenshot 用同一个实例截图（PNG 落盘）
func CaptureScreenshot(ctx context.Context, url, path string, wait time.Duration) error {
	if wait <= 0 {
		wait = 8 * time.Second
	}
	err := withPage(ctx, url, wait, func(session string, c *cdpConn) error {
		if _, err := c.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
			"width": 1920, "height": 1080, "deviceScaleFactor": 1, "mobile": false,
		}, session); err != nil {
			return err
		}
		raw, err := c.call(ctx, "Page.captureScreenshot", map[string]any{
			"format": "png", "captureBeyondViewport": false,
		}, session)
		if err != nil {
			return err
		}
		var r struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		bin, err := base64.StdEncoding.DecodeString(r.Data)
		if err != nil {
			return err
		}
		if len(bin) == 0 {
			return fmt.Errorf("截图为空")
		}
		return os.WriteFile(path, bin, 0o644)
	})
	return err
}

// CleanStaleChromeProfiles 清理历史遗留的临时 profile。
// 老版本每次调用都新建一个，chromium 自建的目录名有两种：
// `.org.chromium.Chromium.XXXX`（多数版本）和 `org.chromium.Chromium.XXXX`（crashpad 遗留）。
func CleanStaleChromeProfiles() (int, int64) {
	var dirs []string
	for _, pat := range []string{".org.chromium.*", "org.chromium.Chromium.*"} {
		if m, err := filepath.Glob(filepath.Join(os.TempDir(), pat)); err == nil {
			dirs = append(dirs, m...)
		}
	}
	n := 0
	var size int64
	for _, d := range dirs {
		var s int64
		_ = filepath.Walk(d, func(_ string, info os.FileInfo, err error) error {
			if err == nil && info != nil && !info.IsDir() {
				s += info.Size()
			}
			return nil
		})
		if err := os.RemoveAll(d); err == nil {
			n++
			size += s
		}
	}
	return n, size
}
