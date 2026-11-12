// Package portscan 自研端口扫描引擎（替代 TscanPlus 的 port 模块）
//
// 设计取舍：
//   - 用 connect() 全连接而不是裸 SYN：**准确率优先**。SYN 扫描在丢包/防火墙静默丢弃时
//     会把开放端口误判成关闭（静默漏报，事后不可回溯）；connect 扫描三次握手成功才算开，
//     漏报只发生在超时，且超时可调、可复测。
//   - 吞吐靠"高并发 + 短超时"：filtered 端口是超时主导，吞吐 ≈ 并发数 / 超时；
//     4096 并发 × 500ms 约 8000 探针/秒，已超过 TscanPlus 实测的 864~5044/秒。
//   - 关闭端口会立刻回 RST（ECONNREFUSED），这类不受超时影响，可跑满并发。
//   - 流式发现：开放端口经 OnOpen **发现即回调**（不等全部扫完），
//     供上层流水线（识别工作池）即时消费。
//   - 运行期调控（Controls）：动态并发（10..8000）/动态超时/暂停/继续，
//     atomic 目标 + sync.Cond 挂起唤醒（无 busy-wait）；与 ctx 取消正交。
package portscan

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Options 扫描参数
type Options struct {
	Concurrency int           // 初始并发连接数，默认 4096
	Timeout     time.Duration // 初始单端口超时，默认 500ms
	// OnProgress 每完成一批回调（done=已完成探针数，total=总探针数，open=已发现开放数）
	OnProgress func(done, total int64, open int64)
	// ProgressEvery 进度回调间隔，默认 2s
	ProgressEvery time.Duration
	// OnOpen 发现开放端口即回调（在扫描 worker 内调用，**实现必须并发安全**）。
	// 缓冲满时形成背压（该 worker 暂停接活），属预期保护。
	OnOpen func(ip string, port int)
	// Dial 探针拨号实现（nil = net.Dialer.DialContext）。
	// 只为单测注入"本地资源耗尽"这类错误，生产路径不要传。
	Dial func(ctx context.Context, timeout time.Duration, addr string) (net.Conn, error)
	// Controls 运行期调控句柄；nil = 内部按 Concurrency/Timeout 自建
	// （默认行为与固定并发等价）。传入后可在扫描期间 SetLimit/SetTimeoutMs/Pause/Resume。
	Controls *Controls
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
	// LocalResErrs 本地资源错误数（EMFILE/ENFILE/ENOBUFS/EAGAIN 等）。
	// >0 表示结果可能不完整：这些探针既不是"关闭"也不是"超时"，
	// 而是本机发不出包（fd/端口/内存不够）——绝不能当成开放端口为空。
	LocalResErrs int64
	Elapsed      time.Duration
}

// PPS 探针速率
func (s Stats) PPS() float64 {
	if s.Elapsed <= 0 {
		return 0
	}
	return float64(s.Probes) / s.Elapsed.Seconds()
}

// Scan 对 targets × ports 做全连接扫描，返回开放端口（按 IP、端口排序）。
//
// 并发形态：派发协程每投一个探针前先向 Controls limiter 领许可（暂停=0 许可时
// 挂起在 cond 上，非 busy-wait），探针协程结束即归还——在途探针数恒 ≤ 当前 limit，
// 调高 SetLimit 立刻被下一个 acquire 用上，调低自然收敛，无需重建 worker 池。
// ctx 取消时返回**已拿到的部分结果** + ctx.Err()（调用方照常消费部分结果）。
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
	// 初始许可 = min(Concurrency, MaxLimit)：limiter 是唯一并发约束，
	// 不预建 worker 池——派发协程逐个领许可、探针协程结束即归还，
	// 在途探针数恒 ≤ 当前 limit（调高立即用满、调低自然收敛、暂停=0 挂起）。
	ctrl := opt.Controls
	if ctrl == nil {
		initLimit := opt.Concurrency
		if initLimit > MaxLimit {
			initLimit = MaxLimit
		}
		ctrl = NewControls(initLimit, int(opt.Timeout/time.Millisecond))
	}

	total := int64(len(targets)) * int64(len(ports))
	st := Stats{Targets: len(targets), Ports: len(ports)}

	var mu sync.Mutex
	results := make([]Result, 0, 256)
	var done, open, closed, filtered, errs, localRes int64

	// ctx 取消 → 持续唤醒 cond，让挂在 acquire 上的派发协程及时看到 ctx 退出
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
		case <-stopWatch:
			return
		}
		for {
			ctrl.wakeAll()
			select {
			case <-stopWatch:
				return
			case <-time.After(30 * time.Millisecond):
			}
		}
	}()

	start := time.Now()

	// 进度上报（派发期间就要跑：暂停/长尾时上层靠它观察 pps→0）
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

	var wg sync.WaitGroup
	var cancelled bool
dispatch:
	for _, ip := range targets {
		for _, p := range ports {
			if ctx.Err() != nil {
				cancelled = true
				break dispatch
			}
			if !ctrl.acquire(ctx) { // 暂停期间挂在这里；取消返回 false
				cancelled = true
				break dispatch
			}
			wg.Add(1)
			go func(ip string, p int) {
				defer wg.Done()
				addr := net.JoinHostPort(ip, strconv.Itoa(p))
				// 探针级重试只针对**本机资源不足**：那是"本机发不出去"，
				// 既不代表端口关闭也不代表被防火墙丢弃，必须退让后重试，
				// 重试仍失败就只记 localRes（上层据此判整轮不可信），绝不记 closed。
				permitHeld := true
				for attempt := 1; ; attempt++ {
					t0 := time.Now()
					// 超时在 dial 前读当前值：SetTimeout 运行期生效（下一个探针即变）
					timeout := time.Duration(ctrl.TimeoutMs()) * time.Millisecond
					conn, err := dialProbe(ctx, opt, timeout, addr)
					if err == nil {
						_ = conn.Close()
						rtt := time.Since(t0)
						mu.Lock()
						results = append(results, Result{IP: ip, Port: p, RTT: rtt})
						mu.Unlock()
						atomic.AddInt64(&open, 1)
						if opt.OnOpen != nil {
							select {
							case <-ctx.Done(): // 已取消：不再投喂，结果集以已收集为准
							default:
								opt.OnOpen(ip, p)
							}
						}
						break
					}
					if isLocalResourceErr(err) {
						// 只在**重试用尽**时计数：阈值口径是"有多少探针最终没发出去"，
						// 按尝试次数计会把极少数失败放大 N 倍（3 次重试 → 占比×3）。
						if attempt < localResRetryTimes {
							// 让出许可 → 退让 → 重新排队，给系统回收 fd 的机会
							ctrl.release()
							permitHeld = false
							select {
							case <-ctx.Done():
							case <-time.After(localResRetryDelay):
							}
							if ctx.Err() != nil || !ctrl.acquire(ctx) {
								atomic.AddInt64(&done, 1)
								return
							}
							permitHeld = true
							continue
						}
						atomic.AddInt64(&localRes, 1)
						break // 用尽重试：只记 localRes，不算 closed/filtered
					}
					if isRefused(err) {
						atomic.AddInt64(&closed, 1)
					} else if isTimeout(err) {
						atomic.AddInt64(&filtered, 1)
					} else {
						atomic.AddInt64(&errs, 1)
					}
					break
				}
				if permitHeld {
					ctrl.release()
				}
				atomic.AddInt64(&done, 1)
			}(ip, p)
		}
	}

	wg.Wait()
	close(progDone)

	st.Elapsed = time.Since(start)
	st.Probes = atomic.LoadInt64(&done)
	st.Open = atomic.LoadInt64(&open)
	st.Closed = atomic.LoadInt64(&closed)
	st.Filtered = atomic.LoadInt64(&filtered)
	st.Errors = atomic.LoadInt64(&errs)
	st.LocalResErrs = atomic.LoadInt64(&localRes)
	if cancelled || ctx.Err() != nil {
		return results, st, ctx.Err()
	}
	// 本地资源耗尽占比超阈值 → 整轮结果不可信，必须让上层按失败处理。
	// 注意：这里返回 results 供审计留痕，但 error 非 nil，调用方不得落 done。
	if st.LocalResErrs > 0 && float64(st.LocalResErrs) > localResFailRatio*float64(st.Probes) {
		return results, st, fmt.Errorf("%w：%d/%d 探针因本机资源不足未发出（占 %.2f%%）",
			ErrLocalResourceExhausted, st.LocalResErrs, st.Probes,
			100*float64(st.LocalResErrs)/float64(max64(st.Probes, 1)))
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

// isLocalResourceErr 判断错误是否来自**本机资源不足**（fd/端口/内存），而不是目标的状态。
//
// 为什么必须单独判：EMFILE/ENFILE/ENOBUFS/EAGAIN 时 connect 会立刻失败，
// 如果按"连接失败=端口关闭"处理，扫描器就会对外输出"该主机没有开放端口"——
// 安全扫描器的假阴性比慢一百倍更致命（客户以为扫过了，其实一个包都没发出去）。
// 跨平台稳妥做法：errors.As 解 syscall 错误 + 错误文本兜底（Windows 的
// WSAENOBUFS/"too many open files" 文本不一致或没有 syscall.Errno）。
func isLocalResourceErr(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		var sysErr *os.SyscallError
		if errors.As(opErr.Err, &sysErr) && sysErr.Err != nil {
			if isResourceErrno(sysErr.Err) {
				return true
			}
		}
		if isResourceErrno(opErr.Err) {
			return true
		}
	}
	var sysErr *os.SyscallError
	if errors.As(err, &sysErr) && sysErr.Err != nil && isResourceErrno(sysErr.Err) {
		return true
	}
	if isResourceErrno(err) {
		return true
	}
	// 兜底：文本匹配（不同平台/不同 Go 版本措辞不一致，但都含这些片段）
	s := strings.ToLower(err.Error())
	for _, k := range []string{
		"too many open files",              // EMFILE/ENFILE
		"no buffer space",                  // ENOBUFS
		"cannot allocate memory",           // ENOMEM
		"resource temporarily unavailable", // EAGAIN
		"address already in use",           // EADDRINUSE（本地端口耗尽的表现之一）
		"cannot assign requested address",  // EADDRNOTAVAIL（临时端口耗尽）
	} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// isResourceErrno 判 errno 是否属于"本机资源不足"。
func isResourceErrno(err error) bool {
	for _, target := range []error{
		syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.EAGAIN,
		syscall.ENOMEM, syscall.EADDRNOTAVAIL, syscall.EADDRINUSE,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// ErrLocalResourceExhausted 本机资源不足导致结果不可信（fd/端口/内存）。
//
// 上层必须把它当成**单元失败**（走 attempts/隔离），绝不能落成 done——
// 否则就是"扫了但什么都没扫到"的静默假阴性。
var ErrLocalResourceExhausted = errors.New("本地 fd/端口资源不足，本轮探测结果不完整")

// 探针级重试参数：本地资源错误不是目标的答复，值得等一下再试。
const (
	localResRetryTimes = 3
	localResRetryDelay = 200 * time.Millisecond
	// localResFailRatio 本地资源错误占比超过它 → 整轮结果不可信（0.5%）
	localResFailRatio = 0.005
)

// dialProbe 单次拨号（Dial 可注入，测试用）。
func dialProbe(ctx context.Context, opt Options, timeout time.Duration, addr string) (net.Conn, error) {
	if opt.Dial != nil {
		return opt.Dial(ctx, timeout, addr)
	}
	d := net.Dialer{Timeout: timeout}
	return d.DialContext(ctx, "tcp", addr)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
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
