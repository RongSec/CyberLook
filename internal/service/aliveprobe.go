package service

// 服务端存活探测（host discovery）—— ICMP + TCP 两阶段
//
// 语义（与产品定义一致）：
//   开启 → 平台先把目标 IP 探一遍，只把「存活」的 IP 放进扫描队列
//   关闭 → 不做任何预探测，全部目标 IP 直接入队分批下发
//
// 为什么必须两层：只探几个 TCP 端口时，目标如果把 SYN 静默丢弃（防火墙默认 DROP），
// 我们既收不到 RST 也连不上，就会把这台**活着但端口被过滤**的机器判成"死"，
// 整台机器的资产从此再也扫不到 —— 这是存活探测最主要的漏报来源。
//
// 判定链（任一命中即判存活，命中后不再进下一阶段）：
//
//	阶段 1  ICMP Echo（非特权 udp4：只要 net.ipv4.ping_group_range 允许即可，
//	        容器默认 NET_RAW + ping_group_range=0..max 就满足；无权限时自动跳过本阶段）
//	        · Echo Reply               → 存活
//	        · Destination Unreachable  → 明确不可达，判死并跳过 TCP 阶段（省时间）
//	阶段 2  TCP connect 到 16 个常见端口（Web / 运维 / Windows / 数据库 / 中间件）
//	        · 连上（SYN-ACK）          → 存活
//	        · 被拒绝（RST）            → 存活（主机回了包，只是端口关着）
//	        · 全部超时                 → 判死
//
// 与正式扫描的分工：存活探测只回答"这台机器要不要扫"，端口真值由后续扫描给出，
// 所以超时取很短（ICMP 1.2s / TCP 350ms），宁可快。

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/target"
	"asset-platform/internal/tasklog"
)

// aliveProbePorts TCP 阶段端口集。比早期那 5 个宽：单一服务的机器（只有 8080、只有 6379）
// 也要能判出来。顺序即优先级，常见的排前面，命中越早越省时间。
var aliveProbePorts = []int{
	80, 443, 22, 445, 135, 139, 3389, 8080, 8443, 9443,
	3306, 6379, 1433, 5432, 27017, 9200,
}

const (
	// aliveProbeConc TCP 阶段并发主机数。
	aliveProbeConc = 1000
	// aliveICMPTimeout 发完后统一等回包的时间窗口。
	aliveICMPTimeout = 1200 * time.Millisecond
	// aliveTCPTimeout 单端口 TCP 超时。局域网 RST 是毫秒级；超时说明被过滤。
	aliveTCPTimeout = 350 * time.Millisecond
	// aliveTCPPerHostConc 单台主机同时试几个端口（命中即取消其余）。
	aliveTCPPerHostConc = 4
	// aliveProbeFlushEvery 每探这么多主机回调一次进度。
	aliveProbeFlushEvery = 500
	// aliveICMPBatch 每批发多少个 Echo（一次灌太多容易被限速丢包）。
	aliveICMPBatch = 1000
)

// AliveStats 存活探测的分阶段统计，写进任务日志便于解释"为什么这台被判死"。
type AliveStats struct {
	Total    int
	ByICMP   int
	ByTCP    int
	Unreach  int
	Dead     int
	ICMPSkip bool
	Elapsed  time.Duration
}

// ProbeAlive 两阶段存活探测。onProgress(done, alive) 返回 false 表示调用方要求中止。
// 返回值带上分阶段统计，便于把"ICMP 命中多少 / TCP 补多少"写进任务日志。
func ProbeAlive(ctx context.Context, ips []string, onProgress func(done, alive int) bool) ([]string, AliveStats, error) {
	if len(ips) == 0 {
		return nil, AliveStats{}, nil
	}
	if onProgress == nil {
		onProgress = func(int, int) bool { return true }
	}
	if err := ctx.Err(); err != nil {
		return nil, AliveStats{}, err
	}
	start := time.Now()

	// ── 阶段 1：ICMP ──
	icmpAlive, unreach, icmpSkip := icmpSweep(ctx, ips, onProgress)
	baseAlive := len(icmpAlive)

	var remain []string
	for _, ip := range ips {
		if icmpAlive[ip] || unreach[ip] {
			continue
		}
		remain = append(remain, ip)
	}
	if err := ctx.Err(); err != nil {
		return orderedAlive(ips, icmpAlive, nil), AliveStats{Total: len(ips), ByICMP: len(icmpAlive), ICMPSkip: icmpSkip}, err
	}

	// ── 阶段 2：TCP（只对 ICMP 没结果的主机）──
	tcpAlive, aborted := tcpSweep(ctx, remain, len(ips)-len(remain), baseAlive, onProgress)

	out := orderedAlive(ips, icmpAlive, tcpAlive)
	st := AliveStats{Total: len(ips), Unreach: len(unreach), ICMPSkip: icmpSkip, Elapsed: time.Since(start)}
	for _, ip := range out {
		if icmpAlive[ip] {
			st.ByICMP++
		} else if tcpAlive[ip] {
			st.ByTCP++
		}
	}
	st.Dead = st.Total - len(out)

	if icmpSkip {
		log.Printf("[存活探测] %d 个目标：ICMP 不可用（无 CAP_NET_RAW 或 ping_group_range 未放开），退化为纯 TCP；TCP 命中 %d，不可达 %d，判死 %d，耗时 %s",
			st.Total, st.ByTCP, st.Unreach, st.Dead, st.Elapsed.Round(time.Second))
	} else {
		log.Printf("[存活探测] %d 个目标：ICMP 命中 %d，TCP 补命中 %d，不可达 %d，判死 %d，耗时 %s",
			st.Total, st.ByICMP, st.ByTCP, st.Unreach, st.Dead, st.Elapsed.Round(time.Second))
	}

	if aborted {
		return out, st, context.Canceled
	}
	return out, st, nil
}

func orderedAlive(ips []string, a, b map[string]bool) []string {
	if len(a)+len(b) == 0 {
		return nil
	}
	out := make([]string, 0, len(a)+len(b))
	for _, ip := range ips {
		if a[ip] || b[ip] {
			out = append(out, ip)
		}
	}
	return out
}

// ===== 阶段 1：ICMP Echo =====

// icmpSweep 批量发 Echo 并收集回包。返回 (存活, 明确不可达, 是否跳过)。
// ICMP 不可用时返回 (nil, nil, true)，调用方退化为纯 TCP。
//
// 实现要点：一个 socket 发全部、一个 goroutine 收全部，按源 IP 归集；
// 不做逐 IP 同步等待（那样 9 万个 IP 就要等 9 万次超时）。
func icmpSweep(ctx context.Context, ips []string, onProgress func(done, alive int) bool) (map[string]bool, map[string]bool, bool) {
	conn, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		// 非 Linux / 无 NET_RAW / ping_group_range 未放开都会走到这里
		return nil, nil, true
	}
	defer conn.Close()

	alive := make(map[string]bool, len(ips)/4)
	unreach := make(map[string]bool, 64)
	var mu sync.Mutex

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 1500)
		for {
			n, peer, err := conn.ReadFrom(buf)
			if err != nil {
				return // 读超时或连接关闭
			}
			addr, ok := peer.(*net.UDPAddr)
			if !ok {
				continue
			}
			msg, err := icmp.ParseMessage(1, buf[:n]) // 1 = ICMPv4
			if err != nil {
				continue
			}
			ipStr := addr.IP.String()
			switch msg.Type {
			case ipv4.ICMPTypeEchoReply:
				mu.Lock()
				alive[ipStr] = true
				mu.Unlock()
			case ipv4.ICMPTypeDestinationUnreachable:
				mu.Lock()
				if !alive[ipStr] {
					unreach[ipStr] = true
				}
				mu.Unlock()
			}
		}
	}()

	// 发包：分批 + 批间小睡，避免突发丢包
	id := os.Getpid() & 0xffff
	seq := 0
	for i := 0; i < len(ips); i++ {
		if ctx.Err() != nil {
			break
		}
		seq++
		msg := icmp.Message{
			Type: ipv4.ICMPTypeEcho, Code: 0,
			Body: &icmp.Echo{ID: id, Seq: seq & 0xffff, Data: []byte("alive-probe")},
		}
		b, err := msg.Marshal(nil)
		if err != nil {
			continue
		}
		dst := &net.UDPAddr{IP: net.ParseIP(ips[i])}
		if dst.IP == nil {
			continue
		}
		if _, err := conn.WriteTo(b, dst); err != nil {
			mu.Lock()
			unreach[ips[i]] = true
			mu.Unlock()
		}
		if i%aliveICMPBatch == aliveICMPBatch-1 {
			// 顺手把已发出的数量报上去，别让前端在 ICMP 阶段一直是 0
			mu.Lock()
			a := len(alive)
			mu.Unlock()
			if !onProgress(i+1, a) {
				_ = conn.SetReadDeadline(time.Now())
				<-readDone
				return alive, unreach, false
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// 统一等回包窗口
	_ = conn.SetReadDeadline(time.Now().Add(aliveICMPTimeout))
	<-readDone
	return alive, unreach, false
}

// ===== 阶段 2：TCP connect =====

// tcpSweep 对 remain 做 TCP 探测；任一端口连通或被拒绝即判存活。
// doneBase：阶段 1 已处理的主机数（进度累加用）；baseAlive：阶段 1 的存活数。
func tcpSweep(ctx context.Context, ips []string, doneBase, baseAlive int, onProgress func(done, alive int) bool) (map[string]bool, bool) {
	alive := make(map[string]bool, len(ips)/4)
	if len(ips) == 0 {
		return alive, false
	}

	var (
		idx     int64
		done    int64
		aborted int32
		mu      sync.Mutex
		wg      sync.WaitGroup
	)
	conc := aliveProbeConc
	if conc > len(ips) {
		conc = len(ips)
	}
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if atomic.LoadInt32(&aborted) == 1 || ctx.Err() != nil {
					return
				}
				i := int(atomic.AddInt64(&idx, 1)) - 1
				if i >= len(ips) {
					return
				}
				if hostAliveTCP(ctx, ips[i]) {
					mu.Lock()
					alive[ips[i]] = true
					mu.Unlock()
				}
				n := int(atomic.AddInt64(&done, 1))
				if n%aliveProbeFlushEvery == 0 || n == len(ips) {
					mu.Lock()
					a := baseAlive + len(alive)
					mu.Unlock()
					if !onProgress(doneBase+n, a) {
						atomic.StoreInt32(&aborted, 1)
					}
				}
			}
		}()
	}
	wg.Wait()
	return alive, atomic.LoadInt32(&aborted) == 1
}

// hostAliveTCP 单台主机的 TCP 判定：端口并行试，命中即停。
//
// 为什么并行：16 个端口串行、每个 350ms 超时，最坏 5.6 秒一台；
// 4 路并行后最坏 1.4 秒，而多数主机在第一个端口就有明确结果（RST 或 SYN-ACK）。
func hostAliveTCP(ctx context.Context, ip string) bool {
	sub, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		hit  int32
		sem  = make(chan struct{}, aliveTCPPerHostConc)
		wg   sync.WaitGroup
	)
	for _, p := range aliveProbePorts {
		if atomic.LoadInt32(&hit) == 1 || sub.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			defer func() { <-sem }()
			if atomic.LoadInt32(&hit) == 1 {
				return
			}
			d := net.Dialer{Timeout: aliveTCPTimeout}
			conn, err := d.DialContext(sub, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
			if err == nil {
				_ = conn.Close()
				atomic.StoreInt32(&hit, 1)
				cancel()
				return
			}
			// 端口被拒绝（RST）= 主机在线且端口关闭，同样证明它活着
			if isConnRefused(err) {
				atomic.StoreInt32(&hit, 1)
				cancel()
			}
		}(p)
	}
	wg.Wait()
	return atomic.LoadInt32(&hit) == 1
}

// isConnRefused 判断是否是"对端明确拒绝"（说明主机在线）。
func isConnRefused(err error) bool {
	if err == nil {
		return false
	}
	return containsAny(err.Error(), "connection refused", "actively refused", "no connection could be made")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if indexFold(s, sub) >= 0 {
			return true
		}
	}
	return false
}

// indexFold 简易大小写不敏感子串查找（避免为几个字符串引入 strings.ToLower 的分配）。
func indexFold(s, sub string) int {
	n := len(sub)
	if n == 0 {
		return 0
	}
	for i := 0; i+n <= len(s); i++ {
		ok := true
		for j := 0; j < n; j++ {
			a, b := s[i+j], sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// sortIPsStable 对 IP 列表做自然序排序（同网段内 10.0.0.9 排在 10.0.0.10 前）。
func sortIPsStable(ips []string) {
	sort.SliceStable(ips, func(i, j int) bool {
		a, b := net.ParseIP(ips[i]), net.ParseIP(ips[j])
		if a == nil || b == nil {
			return ips[i] < ips[j]
		}
		a4, b4 := a.To4(), b.To4()
		if a4 != nil && b4 != nil {
			for k := 0; k < 4; k++ {
				if a4[k] != b4[k] {
					return a4[k] < b4[k]
				}
			}
			return false
		}
		return ips[i] < ips[j]
	})
}

// unitCountOf 该任务已有多少单元（>0 表示已经入队，不需要再探测/展开）。
func unitCountOf(taskID uint) int64 {
	var n int64
	config.DB.Model(&model.TaskUnit{}).Where("task_id = ?", taskID).Count(&n)
	return n
}

// startQueueAfterAliveProbe 先探测存活，再把存活 IP 入队。
//
// 异步执行并立即返回：几万个 IP 的探测再快也要一两分钟，不能把 HTTP 请求堵在那里。
// 探测期间任务状态就是 running 但还没有单元，进度接口显示"存活探测中 x/y"。
//
// 取消/暂停：每批回写进度时顺便检查任务状态，一旦不再是 running/pending 就中止探测，
// 已探出的部分丢弃（不半途入队，避免"这轮只扫了半张网"这种难解释的结果）。
func startQueueAfterAliveProbe(task model.ScanTask, portCount int) bool {
	all := target.Resolve(parseTargets(task.IPRanges), ExcludeRangeStrings())
	if len(all) == 0 {
		log.Printf("[存活探测] 任务 #%d 目标展开为空", task.ID)
		return false
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status": "running", "queue_mode": true, "total_ips": len(all),
		"probe_offset": 0, "alive_count": 0,
	}
	if task.StartedAt == nil {
		updates["started_at"] = now
	}
	config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).Updates(updates)
	tasklog.AppendTS(task.ID, "[存活探测] 开始：%d 个 IP，ICMP Echo + TCP（%d 端口）两阶段判定",
		len(all), len(aliveProbePorts))

	go func() {
		alive, st, err := ProbeAlive(context.Background(), all, func(done, aliveCnt int) bool {
			config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).
				Updates(map[string]interface{}{"probe_offset": done, "alive_count": aliveCnt})
			var st string
			config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).Pluck("status", &st)
			return st == "running" || st == "pending"
		})
		if err != nil {
			log.Printf("[存活探测] 任务 #%d 中止（任务已停止/取消）: %v", task.ID, err)
			return
		}
		sortIPsStable(alive)

		if len(alive) == 0 {
			// 一个都不存活：不能建空队列（会永远不收敛），直接把任务收成 done 并说明原因。
			config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
				"status": "done", "finished_at": time.Now(), "probe_offset": len(all),
				"error": "存活探测未发现存活主机（可关闭存活探测后重试，或检查 ICMP/TCP 是否都被防火墙丢弃）",
			})
			tasklog.AppendTS(task.ID, "[存活探测] 未发现存活主机（%d 个目标），任务结束", len(all))
			NotifyTaskDone(task.ID, task.Name, "完成")
			return
		}

		created, total, e := InitUnitsFromIPs(task.ID, alive, portCount)
		if e != nil {
			log.Printf("[存活探测] 任务 #%d 建队失败: %v", task.ID, e)
			config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).
				Updates(map[string]interface{}{"status": "failed", "error": "建队失败: " + e.Error()})
			return
		}
		config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).
			Updates(map[string]interface{}{"total_ips": len(all), "probe_offset": len(all),
				"alive_count": len(alive)})
		icmpDesc := "ICMP 不可用"
		if !st.ICMPSkip {
			icmpDesc = fmt.Sprintf("ICMP 命中 %d", st.ByICMP)
		}
		tasklog.AppendTS(task.ID, "[存活探测] 完成：%d/%d 存活（%.1f%%），%s，TCP 补命中 %d，不可达 %d，耗时 %s；入队 %d 个 IP × %d 端口",
			len(alive), len(all), 100*float64(len(alive))/float64(len(all)),
			icmpDesc, st.ByTCP, st.Unreach, st.Elapsed.Round(time.Second), total, portCount)

		local := finishQueueActivation(&task, created, total, portCount)
		if local {
			// 单元此时才就绪，本机消费必须在这之后拉起（早启动会按全量 IP 展开）
			log.Printf("[存活探测] 任务 #%d 无在线节点，转本机消费", task.ID)
			go func() {
				if err := ExecuteTaskQueue(task.ID); err != nil {
					log.Printf("[存活探测] 任务 #%d 本机消费退出: %v", task.ID, err)
				}
			}()
		}
	}()
	return true
}
