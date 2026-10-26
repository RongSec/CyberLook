package scanner

import (
	"context"
	"os"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"fmt"
	"net"
	"sync"
	"time"
)

// AliveResult 存活探测结果
type AliveResult struct {
	IP    string
	Alive bool
	MS    int // 响应时间（毫秒）
}

// AliveCheck 存活探测（并发 ICMP/TCP）
type AliveCheck struct {
	Concurrency int
	Timeout     time.Duration
	Workers     int
}

// NewAliveCheck 创建存活探测器
func NewAliveCheck() *AliveCheck {
	return &AliveCheck{
		Concurrency: 64,
		Timeout:     2 * time.Second,
		// 之前 100 个并发、每个 IP 还 fork 一个 ping 进程，
		// 256 个 IP 会在瞬间拉起几百个进程，fork 失败即被判为离线，
		// 于是同一个网段每次探测结果都不一样（实测 24 / 1 / 39 / 160 乱跳）。
		Workers: 128,
	}
}

// Probe 批量探测 IP 存活
//
// 策略：先用单个 raw socket 批量发 ICMP Echo（不 fork 进程、不建连接），
// 收不到回包的主机再用 TCP 端口兜底。
// 之前两种做法都不靠谱：
//   - fork ping 进程：高并发 fork 失败，静默判死，结果乱跳（24/1/39/160）
//   - 纯 TCP：只开非常见端口或只回 ICMP 的主机全部漏报（256 台只探到 1~3 台）
func (a *AliveCheck) Probe(ctx context.Context, ips []string, onProgress func(done, total int, alive int)) []AliveResult {
	results := make([]AliveResult, len(ips))
	if len(ips) == 0 {
		return results
	}
	startAll := time.Now()

	// ---------- 阶段一：ICMP 批量 ----------
	icmpAlive := probeICMPBatch(ips)
	icmpHit := len(icmpAlive)

	ProbeLog("阶段一 ICMP：%d 个目标，命中 %d", len(ips), len(icmpAlive))

	done, alive := 0, 0
	for i, ip := range ips {
		if icmpAlive[ip] {
			results[i] = AliveResult{IP: ip, Alive: true, MS: int(time.Since(startAll).Milliseconds())}
			alive++
		}
		done++
	}
	if onProgress != nil {
		onProgress(done, len(ips), alive)
	}

	// 规模自适应降级：阶段二每个 IP 要试 14 个端口，7 万 IP 约 40 分钟，
	// 所以 /16 以上只做 ICMP 粗筛。
	// 探测只是「加速择机」，扫不扫最终由扫描器自己的存活判定兜底，降级不会漏扫。
	n := len(ips)
	doTCP := n <= 65536

	// ---------- 阶段二：未响应的用 TCP 兜底 ----------
	var rest []int
	for i := range ips {
		if !results[i].Alive {
			rest = append(rest, i)
		}
	}
	if len(rest) == 0 || !doTCP {
		if onProgress != nil {
			onProgress(len(ips), len(ips), alive)
		}
		return results
	}
	if !doTCP {
		ProbeLog("目标 %d 个，仅做 ICMP 粗筛（不下发 TCP 兜底）", n)
	}

	sem := make(chan struct{}, a.Workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, idx := range rest {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			ip := ips[idx]
			ok := a.probePortsParallel(ip, fastPorts) || a.probePortsParallel(ip, slowPorts)
			results[idx] = AliveResult{IP: ip, Alive: ok, MS: int(time.Since(startAll).Milliseconds())}
			mu.Lock()
			done++
			if ok {
				alive++
			}
			if onProgress != nil && done%50 == 0 {
				onProgress(done, len(ips), alive)
			}
			mu.Unlock()
		}(idx)
	}
	wg.Wait()
	ProbeLog("阶段二 TCP：补测 %d 个，新增命中 %d（累计 %d）", len(rest), alive-icmpHit, alive)

	if onProgress != nil {
		onProgress(len(ips), len(ips), alive)
	}
	return results
}

// 探测端口：<b>故意不含 80/443</b>。
//
// 实测本网络有透明代理/上网行为管理设备，对**任意** IP 的 80/443 都会回 SYN-ACK：
//   192.0.2.1:80  OK 10ms   ← 192.0.2.1 是 RFC5737 保留测试地址，不可能有服务
//   192.0.2.1:443 OK 12ms
//   192.0.2.1:445 FAIL 800ms（i/o timeout）
// 用 80/443 判存活会把整个网段判成「全部在线」（实测 256/256 全存活）。
var fastPorts = []string{"445", "22", "3389", "8080"}

// 兜底端口：只开非常见服务的机器靠这些兜住
var slowPorts = []string{"8443", "3306", "6379", "1433", "5432", "9200", "7001", "8888",
	"21", "23", "25", "110", "143", "993", "995", "1521"}

// probeICMPBatch 用一个 raw socket 批量发 Echo 并收包，发两轮提高召回率。
//
// 关键点：
//   - 不限速发太快会被内核/设备丢包 → 每 16 个停 12ms
//   - 收包不用固定 deadline 硬切，而是「连续 idle 内没有新包」才停，
//     否则最后一批回包刚好卡在 deadline 边界会被丢掉
//   - 第一轮没回应的再补发一轮，解决偶发丢包
func probeICMPBatch(ips []string) map[string]bool {
	alive := make(map[string]bool)
	if len(ips) == 0 {
		return alive
	}
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return alive // 没有 CAP_NET_RAW 时退化为纯 TCP
	}
	defer conn.Close()

	id := os.Getpid() & 0xffff
	var seq uint16

	send := func(targets []string) {
		n := 0
		for _, ip := range targets {
			parsed := net.ParseIP(ip)
			if parsed == nil {
				continue
			}
			seq++
			msg := icmp.Message{
				Type: ipv4.ICMPTypeEcho,
				Code: 0,
				Body: &icmp.Echo{ID: id, Seq: int(seq), Data: []byte("weijian-alive")},
			}
			b, err := msg.Marshal(nil)
			if err != nil {
				continue
			}
			if _, err := conn.WriteTo(b, &net.IPAddr{IP: parsed}); err == nil {
				n++
			}
			if n%16 == 0 {
				time.Sleep(12 * time.Millisecond)
			}
		}
	}

	collect := func(window, idle time.Duration) {
		deadline := time.Now().Add(window)
		lastPkt := time.Now()
		buf := make([]byte, 1500)
		for {
			if time.Now().After(deadline) {
				break
			}
			// 收到过包且已空闲超过 idle → 认为本轮收完
			if len(alive) > 0 && time.Since(lastPkt) > idle {
				break
			}
			wait := idle
			if remain := time.Until(deadline); remain < wait {
				wait = remain
			}
			if wait <= 0 {
				break
			}
			_ = conn.SetReadDeadline(time.Now().Add(wait))
			n, peer, err := conn.ReadFrom(buf)
			if err != nil {
				// 读超时：若已空闲超过 idle 则结束，否则继续等
				if time.Since(lastPkt) > idle {
					break
				}
				continue
			}
			m, err := icmp.ParseMessage(1, buf[:n]) // 1 = ICMPv4
			if err != nil || m.Type != ipv4.ICMPTypeEchoReply {
				continue
			}
			if addr, ok := peer.(*net.IPAddr); ok {
				alive[addr.IP.String()] = true
				lastPkt = time.Now()
			}
		}
	}

	// 收包窗口随规模放大：IP 越多，回包越分散，固定 3s 会截断尾部
	window := 3*time.Second + time.Duration(len(ips)/2000)*time.Second
	if window > 15*time.Second {
		window = 15 * time.Second
	}

	// 第 1 轮
	send(ips)
	collect(window, 800*time.Millisecond)

	// 第 2 轮：只补发还没响应的
	var rest []string
	for _, ip := range ips {
		if !alive[ip] {
			rest = append(rest, ip)
		}
	}
	if len(rest) > 0 && len(rest) < len(ips) {
		send(rest)
		w2 := window / 2
		if w2 < 2*time.Second {
			w2 = 2 * time.Second
		}
		collect(w2, 800*time.Millisecond)
	}

	return alive
}

// pingOne 探测单个 IP 是否存活。
//
// 两个关键点（都是之前结果乱跳的原因）：
//  1. 不再 fork 外部 ping 进程 —— 高并发下 fork 失败会静默判死；
//  2. 单 IP 的多个端口「并行」试，而不是串行 —— 否则每个 IP 要等 N×超时，
//     为保证总时长只能拉高并发，反而把 conntrack 打满，导致连接随机失败。
//
// 峰值连接数 = Workers × len(fastPorts) = 32 × 6 = 192，稳定可控。
func (a *AliveCheck) pingOne(ip string) bool {
	if a.probePortsParallel(ip, fastPorts) {
		return true
	}
	return a.probePortsParallel(ip, slowPorts)
}

// probePortsParallel 并行探测同一 IP 的多个端口，任一通即视为存活
func (a *AliveCheck) probePortsParallel(ip string, ports []string) bool {
	if len(ports) == 0 {
		return false
	}
	ch := make(chan bool, len(ports))
	for _, p := range ports {
		go func(port string) {
			ch <- a.pingTCP(ip, port)
		}(p)
	}
	alive := false
	for range ports {
		if <-ch {
			alive = true
		}
	}
	return alive
}

// pingTCP TCP 连接探测（300ms，内网足够）
func (a *AliveCheck) pingTCP(ip, port string) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, port), 600*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ProbeLog 存活探测日志（带时间戳）
func ProbeLog(format string, args ...interface{}) {
	fmt.Printf("[alive] "+format+"\n", args...)
}
