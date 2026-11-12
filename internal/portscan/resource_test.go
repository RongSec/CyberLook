package portscan

// 本地资源耗尽（EMFILE/ENFILE/ENOBUFS…）的正确性测试。
//
// 背景：connect 扫描把「连接失败」一律当成「端口关闭」时，agent fd 耗尽是秒失败，
// 会输出「该主机没有开放端口」——安全扫描器给出假阴性比慢一百倍严重。
// 这里注入假 dialer 连续返回 EMFILE，断言：① 不判 closed ② 有重试 ③ 最终返回资源错误。

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// emfileErr 构造一个与真实 EMFILE 同形的错误：*net.OpError → *os.SyscallError → errno。
func emfileErr() error {
	return &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: &os.SyscallError{Syscall: "socket", Err: syscall.EMFILE},
	}
}

// TestLocalResourceErrClassified 分类器本身：EMFILE 系列要认出，目标侧错误不能误判。
func TestLocalResourceErrClassified(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{emfileErr(), true},
		{&os.SyscallError{Syscall: "socket", Err: syscall.ENFILE}, true},
		{&os.SyscallError{Syscall: "sendto", Err: syscall.ENOBUFS}, true},
		{&os.SyscallError{Syscall: "connect", Err: syscall.EAGAIN}, true},
		{errors.New("dial tcp: too many open files"), true}, // 文本兜底
		{errors.New("dial tcp 1.2.3.4:80: connect: connection refused"), false},
		{errors.New("dial tcp 1.2.3.4:80: i/o timeout"), false},
		{context.DeadlineExceeded, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isLocalResourceErr(c.err); got != c.want {
			t.Fatalf("isLocalResourceErr(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// TestProbeNotClosedOnEmfile 核心断言：fd 耗尽时不得把端口判成 closed，
// 且必须整体返回 ErrLocalResourceExhausted（上层据此按失败上报，绝不落 done）。
func TestProbeNotClosedOnEmfile(t *testing.T) {
	var calls int64
	opt := Options{
		Concurrency:   8,
		Timeout:       300 * time.Millisecond,
		ProgressEvery: time.Hour,
		Dial: func(ctx context.Context, timeout time.Duration, addr string) (net.Conn, error) {
			atomic.AddInt64(&calls, 1)
			return nil, emfileErr()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, st, err := Scan(ctx, []string{"10.255.255.1"}, []int{80}, opt) // 1 个探针

	if len(res) != 0 {
		t.Fatalf("资源耗尽不该产出任何开放端口，得到 %v", res)
	}
	// ① 不能判 closed / filtered
	if st.Closed != 0 {
		t.Fatalf("fd 耗尽被误判成 closed=%d（这正是假阴性的来源）", st.Closed)
	}
	if st.Filtered != 0 {
		t.Fatalf("fd 耗尽被误判成 filtered=%d", st.Filtered)
	}
	// ② 有重试：1 个探针 × localResRetryTimes 次拨号尝试
	if got := atomic.LoadInt64(&calls); got < int64(localResRetryTimes) {
		t.Fatalf("期望至少重试 %d 次拨号，实际 %d 次", localResRetryTimes, got)
	}
	// ③ 必须返回资源错误，且能被 errors.Is 命中（agent 侧据此 ERROR + 失败上报）
	if err == nil {
		t.Fatal("fd 耗尽却返回 nil error——上层会落 done，产生静默假阴性")
	}
	if !errors.Is(err, ErrLocalResourceExhausted) {
		t.Fatalf("期望 ErrLocalResourceExhausted，实际 %v", err)
	}
	if st.LocalResErrs == 0 {
		t.Fatal("Stats.LocalResErrs 应 > 0，便于上层留痕")
	}
}

// TestMixedResourceErrBelowThreshold 少量资源错误（<0.5%）不判废，但计数要留痕。
func TestMixedResourceErrBelowThreshold(t *testing.T) {
	var n int64
	opt := Options{
		Concurrency:   4,
		Timeout:       300 * time.Millisecond,
		ProgressEvery: time.Hour,
		Dial: func(ctx context.Context, timeout time.Duration, addr string) (net.Conn, error) {
			atomic.AddInt64(&n, 1)
			// 只有 1 号端口固定 EMFILE（模拟那一个探针被本机资源卡住），其余按拒绝处理
			if strings.HasSuffix(addr, ":1") {
				return nil, emfileErr()
			}
			return nil, errors.New("dial tcp: connect: connection refused")
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 400 个探针，其中 1 个撞资源错误（3 次尝试）= 0.25% < 0.5%
	var ports []int
	for p := 1; p <= 400; p++ {
		ports = append(ports, p)
	}
	_, st, err := Scan(ctx, []string{"10.255.255.1"}, ports, opt)
	if err != nil {
		t.Fatalf("少量资源错误不该判废整轮，实际 err=%v", err)
	}
	// 口径：按"最终没发出去的探针数"计（不是尝试次数），1 个探针 = 1
	if st.LocalResErrs != 1 {
		t.Fatalf("LocalResErrs 期望 1（1 个探针重试用尽），实际 %d", st.LocalResErrs)
	}
	if st.Closed != int64(len(ports))-1 {
		t.Fatalf("其余探针应判 closed=%d，实际 %d", len(ports)-1, st.Closed)
	}
	fmt.Printf("mixed: probes=%d closed=%d localRes=%d (%.3f%%)\n",
		st.Probes, st.Closed, st.LocalResErrs, 100*float64(st.LocalResErrs)/float64(st.Probes))
}
