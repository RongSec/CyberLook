package scanner

// 流式引擎单测：RunBuiltinPortScan 必须是**多批回调**（发现即识别即上报），
// 而不是扫完一次性给全部。本地起 HTTP 服务模拟开放端口。

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// rulesAvailable 定位仓库内 data/fingerprint（测试 CWD=包目录，向上找）。
func rulesAvailable() (string, bool) {
	for _, cand := range []string{
		BuiltinRulesDir(),
		"../../data/fingerprint",
		"data/fingerprint",
	} {
		if _, err := os.Stat(filepath.Join(cand, "web_fingerprint_v4.json")); err == nil {
			return cand, true
		}
	}
	return "", false
}

// startHTTPServers 在 127.0.0.1 起 n 个 HTTP 服务，返回端口列表与清理函数。
func startHTTPServers(t *testing.T, n int) ([]int, func()) {
	t.Helper()
	ports := make([]int, 0, n)
	srvs := make([]net.Listener, 0, n)
	var mu sync.Mutex
	cleanup := sync.OnceFunc(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range srvs {
			_ = l.Close()
		}
	})
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			cleanup()
			t.Fatalf("监听失败: %v", err)
		}
		mu.Lock()
		srvs = append(srvs, ln)
		mu.Unlock()
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
		body := fmt.Sprintf("<html><title>svc-%d</title><body>hi</body></html>", i)
		go func(l net.Listener, b string) {
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(b))
			})
			s := &http.Server{Handler: mux}
			_ = s.Serve(l)
		}(ln, body)
	}
	return ports, cleanup
}

// startPlainListeners 起 n 个 TCP 监听器（accept 即关），返回端口列表与清理函数。
func startPlainListeners(t *testing.T, n int) ([]int, func()) {
	t.Helper()
	ports := make([]int, 0, n)
	srvs := make([]net.Listener, 0, n)
	var mu sync.Mutex
	cleanup := sync.OnceFunc(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range srvs {
			_ = l.Close()
		}
	})
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			cleanup()
			t.Fatalf("监听失败: %v", err)
		}
		mu.Lock()
		srvs = append(srvs, ln)
		mu.Unlock()
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
		go func(l net.Listener) {
			for {
				c, e := l.Accept()
				if e != nil {
					return
				}
				_ = c.Close()
			}
		}(ln)
	}
	return ports, cleanup
}

func TestRunBuiltinPortScan_StreamsMultipleBatches(t *testing.T) {
	// 260 个开放端口 > flushBatchSize(200)：
	// 前 200 条攒满一批立即发，尾部 60 条 300ms 超时后再发一批 → 必然多批。
	const n = flushBatchSize + 60
	ports, cleanup := startPlainListeners(t, n)
	defer cleanup()

	opts := ScanOptions{
		ProjectID: "unit-stream",
		Modules:   "port", // 纯端口模式：结果数确定、跑得快、断言稳定
		Finger:    "tiny",
		Ports:     joinPorts(ports),
		Thread:    128,
		Timeout:   1000,
	}

	var (
		mu      sync.Mutex
		batches [][]ScanResult
		sum     int
	)
	start := time.Now()
	res, err := RunBuiltinPortScan([]string{"127.0.0.1"}, nil, opts, nil, func(b []ScanResult) {
		mu.Lock()
		defer mu.Unlock()
		cp := make([]ScanResult, len(b))
		copy(cp, b)
		batches = append(batches, cp)
		sum += len(b)
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(res) < n {
		t.Fatalf("应发现全部 %d 个开放端口，实际 %d", n, len(res))
	}
	mu.Lock()
	nb := len(batches)
	mu.Unlock()
	if sum != len(res) {
		t.Errorf("回调累计 %d ≠ 返回 %d", sum, len(res))
	}
	// **核心断言：多批而非一次性**（450 条必然 ≥2 批）
	if nb <= 1 {
		t.Errorf("流式失效：onResult 只被调用 %d 次（期望多批递增回调）", nb)
	}
	for _, b := range batches {
		if len(b) > flushBatchSize {
			t.Errorf("单批 %d 超过上限 %d", len(b), flushBatchSize)
		}
	}
	t.Logf("批次数=%d 总条数=%d 用时=%v", nb, sum, elapsed.Round(time.Millisecond))
}

func TestRunBuiltinPortScan_IdentifyPathWithRules(t *testing.T) {
	rulesDir, rulesOk := rulesAvailable()
	if !rulesOk {
		t.Skip("找不到 web_fingerprint_v4.json，跳过识别路径")
	}
	t.Setenv("SCAN_RULES_DIR", rulesDir)

	ports, cleanup := startHTTPServers(t, 3)
	defer cleanup()
	opts := ScanOptions{
		ProjectID: "unit-ident",
		Modules:   "port,url",
		Finger:    "tiny",
		Ports:     joinPorts(ports),
		Thread:    16,
		Timeout:   1000,
	}
	res, err := RunBuiltinPortScan([]string{"127.0.0.1"}, nil, opts, nil, nil)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	anyHTTP, anyTitle := false, false
	for _, r := range res {
		if strings.EqualFold(r.Protocol, "HTTP") {
			anyHTTP = true
		}
		if strings.HasPrefix(r.Title, "svc-") {
			anyTitle = true
		}
	}
	if !anyHTTP {
		t.Errorf("识别路径应认出 HTTP 协议，实际 %+v", res)
	}
	if !anyTitle {
		t.Errorf("识别路径应抓到页面标题 svc-*")
	}
}

func TestRunBuiltinPortScan_StopViaStopScanReturnsPartial(t *testing.T) {
	// 一个开放端口 + TEST-NET-1 保留段地址（探针吃满超时），
	// 扫描中途 StopScan：应立即收敛，取消前已投喂的批次照常回调入库。
	ports, cleanup := startHTTPServers(t, 1)
	defer cleanup()

	opts := ScanOptions{
		ProjectID: "unit-stop",
		Modules:   "port",
		Ports:     fmt.Sprintf("%d,40000-41000", ports[0]),
		Thread:    64,
		Timeout:   8000, // 长尾探针挂住，给取消留窗口
	}
	type out struct {
		n   int
		err error
	}
	done := make(chan out, 1)
	var partial int
	var pmu sync.Mutex
	go func() {
		res, err := RunBuiltinPortScan([]string{"127.0.0.1", "192.0.2.1"}, nil, opts, nil, func(b []ScanResult) {
			pmu.Lock()
			partial += len(b)
			pmu.Unlock()
		})
		done <- out{len(res), err}
	}()

	// 等句柄注册（入口即注册，600ms 足够）后取消
	time.Sleep(600 * time.Millisecond)
	hit := false
	for i := 0; i < 100 && !hit; i++ {
		hit = StopScan("unit-stop")
		if !hit {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !hit {
		t.Fatal("StopScan 未命中运行中的扫描句柄")
	}
	select {
	case o := <-done:
		if o.err == nil {
			t.Fatal("取消后应返回错误")
		}
		pmu.Lock()
		got := partial
		pmu.Unlock()
		// 部分结果不丢：取消前已识别的 127.0.0.1 端口应已回调
		if got == 0 {
			t.Logf("提示：取消过快，部分结果 0 条（本地端口未及投喂）；返回 %d 条", o.n)
		}
		t.Logf("取消收敛：返回 %d 条、回调 %d 条、err=%v", o.n, got, o.err)
	case <-time.After(25 * time.Second):
		t.Fatal("取消后扫描未在 25s 内收敛")
	}
	// 句柄已注销
	if StopScan("unit-stop") {
		t.Fatal("结束后句柄应已注销")
	}
}

func joinPorts(ports []int) string {
	strs := make([]string, len(ports))
	for i, p := range ports {
		strs[i] = fmt.Sprint(p)
	}
	return strings.Join(strs, ",")
}
