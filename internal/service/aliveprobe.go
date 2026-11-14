package service

// 服务端存活探测（host discovery）—— 平台只做编排，判定逻辑在引擎模块里
//
// 语义（与产品定义一致）：
//   开启 → 平台先把目标 IP 探一遍，只把「存活」的 IP 放进扫描队列
//   关闭 → 不做任何预探测，全部目标 IP 直接入队分批下发
//
// 为什么判定逻辑不在这里：平台和独立 CLI（github.com/RongSec/CyberLook-Scan）
// 必须保持功能与性能完全一致。早期两边各写一套（平台 ICMP+TCP、CLI 纯 TCP），
// 结果就是"同一网段平台判活 605 台、CLI 判活 295 台"这种没法解释的差异。
// 现在统一调用 portscan.DiscoverHosts，平台这边只负责：
//   - 探测进度回写（probe_offset / alive_count），让前端显示"存活探测中 x/y"
//   - 响应停止/取消（每批检查任务状态）
//   - 探测完成后建队列、唤醒节点
//
// 判定细节（ICMP Echo → TCP connect 两阶段、端口集、超时）全在引擎里，
// 详见 portscan/alive.go 的注释。

import (
	"context"
	"log"
	"net"
	"sort"
	"time"

	"github.com/RongSec/CyberLook-Scan/portscan"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/scanner"
	"asset-platform/internal/target"
	"asset-platform/internal/tasklog"
)

const (
	// aliveProbeConc 并发主机数。纯 connect/ICMP，3000 并发约 3000 个 socket。
	// 为什么是 1200：一个 /16 要发 65536×16 ≈ 105 万次 dial，被过滤的端口每个都要
	// 等满超时。并发太低（几百）界面看着像卡死，太高（3000+）又会让目标的限速/IPS
	// 丢掉我们的 SYN，把活主机判死。1200 是实测的平衡点，且引擎侧还有抽样复核兜底：
	// 一旦发现漏判就自动降速重扫（Adaptive），所以这里给得高一点也不会硬伤准确性。
	aliveProbeConc = 1200
	// aliveProbeTCPTimeout 单端口 TCP 超时（引擎默认 400ms，这里略微收紧）。
	aliveProbeTCPTimeout = 350 * time.Millisecond
)

// ProbeAlive 调引擎做两阶段存活探测，返回存活 IP 与分阶段统计。
// onProgress(done, alive) 返回 false 表示调用方要求中止。
func ProbeAlive(ctx context.Context, ips []string, onProgress func(done, alive int) bool) ([]string, portscan.AliveStats, error) {
	if len(ips) == 0 {
		return nil, portscan.AliveStats{}, nil
	}
	hosts, st := portscan.DiscoverHosts(ctx, ips, portscan.AliveOptions{
		Ports:       portscan.DefaultAlivePorts, // 与 CLI 完全同一套端口集
		Concurrency: aliveProbeConc,
		TCPTimeout:  aliveProbeTCPTimeout,
		ICMP:        true,
		// 平台只要"这台活着/不活着"：ICMP 已经判活的主机不用再跑一轮 TCP，
		// 端口真值交给后面的扫描阶段（那里本来就是全端口/前 N 端口扫）。
		SkipTCPIfAlive: true,
		// 中间件代答的端口（实测 80/443 对任意地址 1ms "连接成功"）不参与判活，
		// 否则整张网都会变成"存活"。
		InterceptedPorts: scanner.InterceptedPortsOf(portscan.DefaultAlivePorts),
		// 抽样复核：抽一小批判死的主机低速重探，发现漏判就降速重扫。
		// 这是唯一能在"自己的探测把目标打限速了"时自己发现的办法。
		Adaptive:   true,
		OnProgress: onProgress,
	})
	alive := make([]string, 0, len(hosts))
	for _, h := range hosts {
		alive = append(alive, h.IP)
	}
	if ctx.Err() != nil {
		return alive, st, ctx.Err()
	}
	return alive, st, nil
}

// sortIPsStable 对 IP 列表做自然序排序（同网段内 10.0.0.9 排在 10.0.0.10 前）。
func sortIPsStable(ips []string) {
	sort.SliceStable(ips, func(i, j int) bool {
		a, b := net.ParseIP(ips[i]).To4(), net.ParseIP(ips[j]).To4()
		if a == nil || b == nil {
			return ips[i] < ips[j]
		}
		for k := 0; k < 4; k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return false
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
		len(all), len(portscan.DefaultAlivePorts))

	// 先报一次链路中间件识别结果：它决定了"哪些端口不能信"，
	// 不写进任务日志的话，用户看到"存活 7 台"会以为是漏报。
	if bad := scanner.InterceptedPortsOf(portscan.DefaultAlivePorts); len(bad) > 0 {
		tasklog.AppendTS(task.ID, "[存活探测] 链路检测：端口 %v 被中间设备代答（对保留地址也返回连接成功），已从判活端口集中剔除", bad)
	}

	go func() {
		alive, st, err := ProbeAlive(context.Background(), all, func(done, aliveCnt int) bool {
			config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).
				Updates(map[string]interface{}{"probe_offset": done, "alive_count": aliveCnt})
			var state string
			config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).Pluck("status", &state)
			return state == "running" || state == "pending"
		})
		if err != nil {
			log.Printf("[存活探测] 任务 #%d 中止（任务已停止/取消）: %v", task.ID, err)
			return
		}
		sortIPsStable(alive)

		// 平台用的是 SkipTCPIfAlive：ICMP 已判活的主机不会再跑 TCP，
		// 所以两边的集合天然互斥，直接按"ICMP 判活 / TCP 补判"报，不用再算交集。
		if st.ICMPSkip {
			tasklog.AppendTS(task.ID, "[存活探测] 判定：ICMP 不可用（无权限，退化为纯 TCP），TCP 判活 %d，不可达 %d，耗时 %s",
				st.ByTCP, st.Unreach, st.Elapsed.Round(time.Second))
		} else {
			tasklog.AppendTS(task.ID, "[存活探测] 判定：ICMP 判活 %d（不再跑 TCP），TCP 补判 %d，不可达 %d，耗时 %s",
				st.ByICMP, st.ByTCP, st.Unreach, st.Elapsed.Round(time.Second))
		}
		// 复核结论必须写进日志：这张网"判死的主机里有多少其实活着"是判断
		// 探测速率是否合适唯一的一手数据，比任何经验参数都可靠。
		if st.SampledN > 0 {
			if st.SampledAlive == 0 {
				tasklog.AppendTS(task.ID, "[存活探测] 复核：抽查 %d 台判死主机全部确实无响应，首轮速率 %d 合适",
					st.SampledN, aliveProbeConc)
			} else {
				missRate := float64(st.SampledAlive) * 100 / float64(st.SampledN)
				tasklog.AppendTS(task.ID, "[存活探测] 复核：抽查 %d 台判死主机发现 %d 台其实存活（%.1f%%），已降速重扫 %d 轮，并发降到 %d",
					st.SampledN, st.SampledAlive, missRate, st.Passes, st.ConcUsed)
			}
		}

		if len(alive) == 0 {
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
		tasklog.AppendTS(task.ID, "[存活探测] 完成：%d/%d 存活（%.1f%%）；入队 %d 个 IP × %d 端口",
			len(alive), len(all), 100*float64(len(alive))/float64(len(all)), total, portCount)

		local := finishQueueActivation(&task, created, total, portCount)
		if local {
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
