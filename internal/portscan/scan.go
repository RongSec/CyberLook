// Package portscan 自研端口扫描引擎（替代 TscanPlus 的 port 模块）
//
// 设计取舍：
//   - 用 connect() 全连接而不是裸 SYN：**准确率优先**。SYN 扫描在丢包/防火墙静默丢弃时
//     会把开放端口误判成关闭（静默漏报，事后不可回溯）；connect 扫描三次握手成功才算开，
//     漏报只发生在超时，且超时可调、可复测。
//   - 吞吐靠"高并发 + 短超时"：filtered 端口是超时主导，吞吐 ≈ 并发数 / 超时；
//     4096 并发 × 500ms 约 8000 探针/秒，已超过 TscanPlus 实测的 864~5044/秒。
//   - 关闭端口会立刻回 RST（ECONNREFUSED），这类不受超时影响，可跑满并发。
package portscan

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options 扫描参数
type Options struct {
	Concurrency int           // 并发连接数，默认 4096
	Timeout     time.Duration // 单端口超时，默认 500ms
	// OnProgress 每完成一批回调（done=已完成探针数，total=总探针数，open=已发现开放数）
	OnProgress func(done, total int64, open int64)
	ProgressEvery time.Duration // 进度回调间隔，默认 2s
}

// Result 一个开放端口
type Result struct {
	IP   string
	Port int
	RTT  time.Duration
}

// Stats 扫描统计
type Stats struct {
	Targets  int
	Ports    int
	Probes   int64
	Open     int64
	Closed   int64
	Filtered int64
	Errors   int64
	Elapsed  time.Duration
}

// PPS 探针速率
func (s Stats) PPS() float64 {
	if s.Elapsed <= 0 {
		return 0
	}
	return float64(s.Probes) / s.Elapsed.Seconds()
}

// Scan 对 targets × ports 做全连接扫描，返回开放端口（按 IP、端口排序）。
func Scan(ctx context.Context, targets []string, ports []int, opt Options) ([]Result, Stats, error) {
	if len(targets) == 0 || len(ports) == 0 {
		return nil, Stats{}, fmt.Errorf("目标和端口不能为空")
	}
	if opt.Concurrency <= 0 {
		opt.Concurrency = 4096
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 500 * time.Millisecond
	}
	if opt.ProgressEvery <= 0 {
		opt.ProgressEvery = 2 * time.Second
	}

	total := int64(len(targets)) * int64(len(ports))
	st := Stats{Targets: len(targets), Ports: len(ports)}

	type job struct {
		ip   string
		port int
	}
	jobs := make(chan job, opt.Concurrency*2)
	var mu sync.Mutex
	results := make([]Result, 0, 256)
	var done, open, closed, filtered, errs int64

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < opt.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := net.Dialer{Timeout: opt.Timeout}
			for j := range jobs {
				if ctx.Err() != nil {
					return
				}
				t0 := time.Now()
				conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(j.ip, strconv.Itoa(j.port)))
				if err == nil {
					_ = conn.Close()
					rtt := time.Since(t0)
					mu.Lock()
					results = append(results, Result{IP: j.ip, Port: j.port, RTT: rtt})
					mu.Unlock()
					atomic.AddInt64(&open, 1)
				} else if isRefused(err) {
					atomic.AddInt64(&closed, 1)
				} else if isTimeout(err) {
					atomic.AddInt64(&filtered, 1)
				} else {
					atomic.AddInt64(&errs, 1)
				}
				atomic.AddInt64(&done, 1)
			}
		}()
	}

	// 进度上报
	progDone := make(chan struct{})
	if opt.OnProgress != nil {
		go func() {
			t := time.NewTicker(opt.ProgressEvery)
			defer t.Stop()
			for {
				select {
				case <-progDone:
					return
				case <-t.C:
					opt.OnProgress(atomic.LoadInt64(&done), total, atomic.LoadInt64(&open))
				}
			}
		}()
	}

	// 投递任务
	var cancelled bool
	for _, ip := range targets {
		for _, p := range ports {
			jobs <- job{ip: ip, port: p}
			if ctx.Err() != nil {
				cancelled = true
				break
			}
		}
		if cancelled {
			break
		}
	}
	close(jobs)
	wg.Wait()
	close(progDone)

	st.Elapsed = time.Since(start)
	st.Probes = atomic.LoadInt64(&done)
	st.Open = atomic.LoadInt64(&open)
	st.Closed = atomic.LoadInt64(&closed)
	st.Filtered = atomic.LoadInt64(&filtered)
	st.Errors = atomic.LoadInt64(&errs)
	if cancelled {
		return results, st, ctx.Err()
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].IP == results[j].IP {
			return results[i].Port < results[j].Port
		}
		return results[i].IP < results[j].IP
	})
	return results, st, nil
}

// isRefused 端口关闭（RST）——包括 Windows 的 forcibly closed
func isRefused(err error) bool {
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "forcibly closed") ||
		strings.Contains(s, "actively refused")
}

func isTimeout(err error) bool {
	s := err.Error()
	return strings.Contains(s, "i/o timeout") || strings.Contains(s, "context deadline exceeded")
}

// ParsePorts 解析端口表达式：1-65535 / 22,80,443 / top100 / top1000
func ParsePorts(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("端口为空")
	}
	switch strings.ToLower(spec) {
	case "top100":
		return append([]int(nil), TopN[:100]...), nil
	case "top1000":
		return append([]int(nil), TopN...), nil
	}

	seen := map[int]bool{}
	var out []int
	for _, part := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, "-"); i > 0 {
			lo, err1 := strconv.Atoi(strings.TrimSpace(part[:i]))
			hi, err2 := strconv.Atoi(strings.TrimSpace(part[i+1:]))
			if err1 != nil || err2 != nil || lo <= 0 || hi < lo || hi > 65535 {
				return nil, fmt.Errorf("端口区间非法: %s", part)
			}
			for p := lo; p <= hi; p++ {
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
			continue
		}
		p, err := strconv.Atoi(part)
		if err != nil || p <= 0 || p > 65535 {
			return nil, fmt.Errorf("端口非法: %s", part)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// TopN 常见端口（Top1000 的简化版，按内网出现概率排序）
var TopN = []int{
	22, 80, 443, 3389, 445, 139, 135, 21, 23, 25, 110, 143, 161, 162, 389, 636,
	873, 1433, 1521, 3306, 5432, 6379, 27017, 9200, 9300, 11211, 5672, 15672,
	8080, 8081, 8082, 8088, 8443, 8000, 8001, 8888, 9000, 9001, 9090, 9443,
	10000, 18080, 5000, 5001, 7001, 7002, 8009, 8010, 8060, 8161, 9008,
	53, 67, 68, 69, 123, 137, 138, 427, 514, 515, 548, 623, 631, 993, 995,
	1080, 1723, 2049, 2375, 2376, 3128, 3260, 4440, 4848, 5002, 5060, 5222,
	5269, 5357, 5433, 5601, 5900, 5984, 5985, 5986, 6000, 6443, 7077, 8006,
	8019, 8020, 8069, 8090, 8091, 8180, 8200, 8300, 8500, 8529, 8834, 8983,
	9002, 9003, 9080, 9091, 9092, 9093, 9201, 9418, 9999, 10050, 10051, 11212,
	15692, 27018, 50070, 50075, 50090, 61616, 61613, 10086, 10443, 12345,
}
