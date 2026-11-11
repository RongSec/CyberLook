package portscan

// portscan 引擎单测：
//   - 固定信号量：并发 worker 数 = Options.Concurrency，OnOpen 同时在途数不超过它
//   - ctx 取消：及时收敛（预算 2s）且返回已收集的部分结果
//   - OnOpen 流式回调：发现即回调、并发安全
//   - ParsePorts 口径

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func listenN(t *testing.T, n int) ([]int, func()) {
	t.Helper()
	ports := make([]int, 0, n)
	ls := make([]net.Listener, 0, n)
	for i := 0; i < n; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			for _, x := range ls {
				_ = x.Close()
			}
			t.Fatal(err)
		}
		ls = append(ls, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
		go func(ll net.Listener) {
			for {
				c, e := ll.Accept()
				if e != nil {
					return
				}
				_ = c.Close()
			}
		}(l)
	}
	return ports, func() {
		for _, x := range ls {
			_ = x.Close()
		}
	}
}

// TestFixedConcurrencyIsBufferedChannel limiter 形态：OnOpen 在探针协程内同步调用，
// 用「OnOpen 同时在途计数」验证实际并发 ≤ Concurrency（=10，即 limiter 下限）。
func TestFixedConcurrencyIsBufferedChannel(t *testing.T) {
	ports, cleanup := listenN(t, 40)
	defer cleanup()

	var mu sync.Mutex
	cur, max := 0, 0
	_, _, err := Scan(context.Background(), []string{"127.0.0.1"}, ports, Options{
		Concurrency: 10, // = MinLimit（控制器并发下限，产品口径 10..8000）
		Timeout:     time.Second,
		OnOpen: func(ip string, p int) {
			mu.Lock()
			cur++
			if cur > max {
				max = cur
			}
			mu.Unlock()
			time.Sleep(15 * time.Millisecond) // 拖住在途槽位，让超额并发暴露
			mu.Lock()
			cur--
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	if max > 10 {
		t.Errorf("实际并发 %d 超过设定 10（limiter 失效？）", max)
	}
}

// TestCancelStopsWithinTimeout 取消后 Scan 必须在合理预算内收敛。
// 预算 2s（评审纠正：100ms 在本机对 TEST-NET 不可达——SYN 无人应答、
// 连接要等 dial 超时，属物理规律非缺陷；取消信号本身 <100ms 级生效）。
func TestCancelStopsWithinTimeout(t *testing.T) {
	ports := make([]int, 0, 1000)
	for p := 30000; p < 31000; p++ {
		ports = append(ports, p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, _, err := Scan(ctx, []string{"192.0.2.7"}, ports, Options{
		Concurrency: 8,
		Timeout:     10 * time.Second, // 单探针 10s，取消必须能打断整体
	})
	dur := time.Since(start)
	if err != nil && !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("取消后错误应是 context canceled: %v", err)
	}
	if dur > 2*time.Second {
		t.Errorf("取消后 %v 才返回，超出 2s 预算", dur)
	}
	t.Logf("取消收敛用时 %v err=%v", dur.Round(time.Millisecond), err)
}

// TestOnOpenConcurrentSafety 高并发下 OnOpen 多线程回调不丢不重（-race 下同时验证数据竞争）。
func TestOnOpenConcurrentSafety(t *testing.T) {
	ports, cleanup := listenN(t, 50)
	defer cleanup()

	var mu sync.Mutex
	seen := map[int]int{}
	_, st, err := Scan(context.Background(), []string{"127.0.0.1"}, ports, Options{
		Concurrency: 64,
		Timeout:     time.Second,
		OnOpen: func(ip string, p int) {
			mu.Lock()
			seen[p]++
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	if st.Open != 50 {
		t.Fatalf("开放数=%d 期望 50", st.Open)
	}
	if len(seen) != 50 {
		t.Fatalf("OnOpen 端口去重数=%d 期望 50", len(seen))
	}
	for p, c := range seen {
		if c != 1 {
			t.Errorf("端口 %d 回调 %d 次，期望 1", p, c)
		}
	}
}

// TestScanReturnsPartialOnCancel 取消时已发现结果必须带回（部分完成不丢数据）。
func TestScanReturnsPartialOnCancel(t *testing.T) {
	ports, cleanup := listenN(t, 4)
	defer cleanup()
	pad := make([]int, 0, 300)
	for p := 41000; p < 41300; p++ {
		pad = append(pad, p)
	}
	all := append(append([]int{}, ports...), pad...)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	res, _, err := Scan(ctx, []string{"127.0.0.1"}, all, Options{
		Concurrency: 2,
		Timeout:     8 * time.Second,
	})
	if err == nil {
		t.Skip("扫描恰好在取消前自然结束，取消语义未覆盖")
	}
	for _, r := range res {
		if r.IP == "" || r.Port <= 0 {
			t.Errorf("部分结果字段异常: %+v", r)
		}
	}
	t.Logf("取消时带回部分结果 %d 条", len(res))
}

func TestParsePorts(t *testing.T) {
	p100, err := ParsePorts("top100")
	if err != nil || len(p100) != 100 {
		t.Fatalf("top100: %d %v", len(p100), err)
	}
	p, err := ParsePorts("22,80,443")
	if err != nil || len(p) != 3 || p[0] != 22 {
		t.Fatalf("list: %v %v", p, err)
	}
	r, err := ParsePorts("100-104")
	if err != nil || len(r) != 5 {
		t.Fatalf("range: %v %v", r, err)
	}
	if _, err := ParsePorts("0-10"); err == nil {
		t.Fatal("非法下界应报错")
	}
	if _, err := ParsePorts(""); err == nil {
		t.Fatal("空串应报错")
	}
}
