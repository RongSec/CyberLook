package service

// 服务端存活探测
//
// 语义（与产品定义一致）：
//   开启 → 平台先把目标 IP 探一遍，只把「存活」的 IP 放进扫描队列
//   关闭 → 不做任何预探测，全部目标 IP 直接入队分批下发
//
// 为什么放在服务端而不是节点：
//  1. 语义就是"平台先探测"，结果要能入库（alive_count / probe_offset），
//     重跑和审计时能看到"当时判死的是哪些 IP"；
//  2. 节点只需理解"扫这个 IP 的这些端口"，不必再理解存活语义，职责更单一；
//  3. 探测阶段可以单独限速/取消，不占用节点的扫描并发。
//
// 关于 ICMP：这套引擎是 TCP connect 扫描，本来就不发 ICMP，
// 所以"禁用 ping(-np)"这种开关在自研引擎里没有实际对应物，已从界面移除。
//
// 判定口径：对每个 IP 依次试 aliveProbePorts，**任一端口能完成 TCP 握手即判存活**
// （收到 RST 也算"主机在线"，因为它证明有主机回了包）。全部超时/不可达才判死。

import (
	"context"
	"log"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/target"
	"asset-platform/internal/tasklog"
)

// aliveProbePorts 存活探测端口。只挑"跨平台最常见且有服务监听"的几个：
// 80/443 覆盖 Web，22 覆盖 Linux 运维口，445/3389 覆盖 Windows。
// 端口越少越快（探测耗时 ≈ IP 数 × 端口数 × 超时 ÷ 并发）。
var aliveProbePorts = []int{80, 443, 22, 445, 3389}

const (
	// aliveProbeConc 探测并发。这是纯 connect，不加载指纹，1000 并发约 1000 个 socket。
	aliveProbeConc = 1000
	// aliveProbeTimeout 单个端口的超时。存活探测宁可快一点：超时的当作不可达。
	aliveProbeTimeout = 800 * time.Millisecond
	// aliveProbeFlushEvery 每探这么多 IP 回调一次进度（写库/检查取消）。
	aliveProbeFlushEvery = 500
)

// ProbeAlive 对 ips 做 TCP 存活探测，返回存活 IP（按输入顺序稳定排序）。
//
// onProgress(done, alive) 返回 false 表示调用方要求中止（任务被暂停/取消）。
// 中止时返回已探出的存活列表 + ctx.Err()，已探部分不丢。
func ProbeAlive(ctx context.Context, ips []string, onProgress func(done, alive int) bool) ([]string, error) {
	if len(ips) == 0 {
		return nil, nil
	}
	if onProgress == nil {
		onProgress = func(int, int) bool { return true }
	}

	var (
		idx      int64
		aliveMu  sync.Mutex
		aliveSet = make([]bool, len(ips)) // 按下标标记，保证输出顺序稳定
		done     int64
		aborted  int32
		wg       sync.WaitGroup
	)

	worker := func() {
		defer wg.Done()
		for {
			if atomic.LoadInt32(&aborted) == 1 || ctx.Err() != nil {
				return
			}
			i := int(atomic.AddInt64(&idx, 1)) - 1
			if i >= len(ips) {
				return
			}
			if hostAlive(ctx, ips[i]) {
				aliveMu.Lock()
				aliveSet[i] = true
				aliveMu.Unlock()
			}
			n := int(atomic.AddInt64(&done, 1))
			// 进度回调：每 flushEvery 个一次，或最后一个
			if n%aliveProbeFlushEvery == 0 || n == len(ips) {
				aliveMu.Lock()
				cnt := 0
				for _, v := range aliveSet {
					if v {
						cnt++
					}
				}
				aliveMu.Unlock()
				if !onProgress(n, cnt) {
					atomic.StoreInt32(&aborted, 1)
				}
			}
		}
	}

	conc := aliveProbeConc
	if conc > len(ips) {
		conc = len(ips)
	}
	wg.Add(conc)
	for i := 0; i < conc; i++ {
		go worker()
	}
	wg.Wait()

	var out []string
	for i, v := range aliveSet {
		if v {
			out = append(out, ips[i])
		}
	}
	if ctx.Err() != nil || atomic.LoadInt32(&aborted) == 1 {
		return out, context.Canceled
	}
	return out, nil
}

// hostAlive 单个 IP 是否存活：任一端口能连通（含被拒绝）即算在线。
func hostAlive(ctx context.Context, ip string) bool {
	for _, p := range aliveProbePorts {
		if ctx.Err() != nil {
			return false
		}
		d := net.Dialer{Timeout: aliveProbeTimeout}
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(p)))
		if err == nil {
			_ = conn.Close()
			return true
		}
		// 端口被拒绝（RST）= 主机在线且端口关闭，同样证明它活着。
		// 只有超时/网络不可达才继续试下一个端口。
		if isConnRefused(err) {
			return true
		}
	}
	return false
}

// isConnRefused 判断是否是"对端明确拒绝"（说明主机在线）。
func isConnRefused(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// 跨平台文案：Linux "connection refused"，Windows "No connection could be made..."
	return containsAny(s, "connection refused", "actively refused", "no connection could be made")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) <= len(s) && indexFold(s, sub) >= 0 {
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
// 仅用于让"只下发存活 IP"的顺序可预期，便于人工核对。
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
// 异步执行并立即返回：94,239 个 IP × 5 个端口，即使 1000 并发也要几分钟，
// 不能把 HTTP 请求堵在那里。探测期间任务状态就是 running，但还没有单元，
// 进度接口会显示"存活探测中 x/y"，节点领取时拿不到单元会自然空转等待。
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
	tasklog.AppendTS(task.ID, "[存活探测] 开始探测 %d 个 IP（%d 端口 connect，命中任一即判存活）",
		len(all), len(aliveProbePorts))

	go func() {
		start := time.Now()
		alive, err := ProbeAlive(context.Background(), all, func(done, aliveCnt int) bool {
			// 回写进度：前端据此显示"存活探测中"，而不是干等一个 0% 的进度条
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
		elapsed := time.Since(start).Round(time.Second)
		sortIPsStable(alive)

		if len(alive) == 0 {
			// 一个都不存活：不能建空队列（会永远不收敛），直接把任务收成 done 并说明原因。
			config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
				"status": "done", "finished_at": time.Now(), "probe_offset": len(all),
				"error": "存活探测未发现存活主机（可关闭存活探测后重试，或检查探测端口是否被防火墙丢弃）",
			})
			tasklog.AppendTS(task.ID, "[存活探测] 未发现存活主机（%d 个目标，耗时 %s），任务结束", len(all), elapsed)
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
		tasklog.AppendTS(task.ID, "[存活探测] 完成：%d/%d 存活（%.1f%%），耗时 %s；入队 %d 个 IP × %d 端口",
			len(alive), len(all), 100*float64(len(alive))/float64(len(all)), elapsed, total, portCount)

		local := finishQueueActivation(&task, created, total, portCount)
		if local {
			log.Printf("[存活探测] 任务 #%d 无在线节点，转本机消费", task.ID)
		}
	}()
	return true
}
