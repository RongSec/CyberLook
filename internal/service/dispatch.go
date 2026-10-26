package service

import (
	"gorm.io/gorm"
	"strconv"
	"asset-platform/internal/scanner"
	"context"
	"sort"
	"log"
	"fmt"
	"strings"
	"sync"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/target"
	"asset-platform/internal/tasklog"
)

// ===== agent 唤醒通道（SSE 订阅） =====
var (
	wakeMu   sync.Mutex
	wakeSubs = map[uint][]chan string{}
)

// SubscribeAgent agent 订阅唤醒信号
func SubscribeAgent(nodeID uint) chan string {
	ch := make(chan string, 8)
	wakeMu.Lock()
	wakeSubs[nodeID] = append(wakeSubs[nodeID], ch)
	wakeMu.Unlock()
	return ch
}

// UnsubscribeAgent 取消订阅
func UnsubscribeAgent(nodeID uint, ch chan string) {
	wakeMu.Lock()
	defer wakeMu.Unlock()
	subs := wakeSubs[nodeID]
	for i, c := range subs {
		if c == ch {
			wakeSubs[nodeID] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
}

// WakeAgent 唤醒指定节点（有新任务时调用）
func WakeAgent(nodeID uint) {
	wakeMu.Lock()
	defer wakeMu.Unlock()
	for _, ch := range wakeSubs[nodeID] {
		select {
		case ch <- "new_task":
		default:
		}
	}
}

// nodeAlive 以心跳时间为准判断节点是否真的在线。
// 数据库里的 status 字段可能是历史值（节点掉线后不会立即回写），
// 只看 status 会把任务派给已经死掉的节点，然后静默失败。
const nodeAliveWindow = 2 * time.Minute

func nodeAlive(n *model.Node) bool {
	if n == nil || n.LastHeartbeat == nil {
		return false
	}
	return time.Since(*n.LastHeartbeat) < nodeAliveWindow
}

// DispatchTask 按节点并发能力拆分 IP 段并分配子任务
func DispatchTask(task *model.ScanTask) error {
	if task.NodeIDs == "" {
		// 下发前先做存活检测：以心跳时间为准，而不是数据库里可能过期的 status
		var nodes []model.Node
		config.DB.Find(&nodes)

		pickIdx := -1
		for i := range nodes {
			if nodes[i].Name == "node-docker-1" && nodeAlive(&nodes[i]) {
				pickIdx = i
				break
			}
		}
		if pickIdx < 0 {
			for i := range nodes {
				if !nodeAlive(&nodes[i]) {
					continue
				}
				if pickIdx < 0 || nodes[i].LastHeartbeat.After(*nodes[pickIdx].LastHeartbeat) {
					pickIdx = i
				}
			}
		}
		if pickIdx < 0 {
			return fmt.Errorf("没有可用的在线节点（所有节点心跳均超时），请先确认节点已启动并与中心连通")
		}
		task.NodeIDs = fmt.Sprint(nodes[pickIdx].ID)
		log.Printf("[dispatch] 任务 #%d 未指定节点，存活检测后分配到 %s", task.ID, nodes[pickIdx].Name)
	}

	ips := target.Resolve(strings.Split(task.IPRanges, ","), nil)
	if len(ips) == 0 {
		return fmt.Errorf("目标展开后无有效 IP")
	}
	config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).Update("total_ips", len(ips))
	tasklog.AppendTS(task.ID, "[调度] 任务 #%d 目标 %d 个 IP，进入流水线（边探测边扫描）", task.ID, len(ips))

	// 交给流水线：先探第一片，探完自动下发
	go DispatchNextBatch(task.ID)
	return nil
}

// portCountOf 估算本任务的端口数，用于在分片尚未上报时给出进度分母。
// 之前分母只累加已上报分片的 total，未开始的分片是 0，
// 一个分片扫完就变成 SUM(scanned)/SUM(total) = 100%，进度条瞬间满格。
func portCountOf(t *model.ScanTask) int {
	if strings.EqualFold(t.PortMode, "all") {
		return 65535
	}
	s := strings.TrimSpace(t.Ports)
	if s == "" {
		return 100 // Top100
	}
	n := 0
	for _, seg := range strings.Split(s, ",") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if i := strings.Index(seg, "-"); i > 0 {
			a, e1 := strconv.Atoi(strings.TrimSpace(seg[:i]))
			b, e2 := strconv.Atoi(strings.TrimSpace(seg[i+1:]))
			if e1 == nil && e2 == nil && a > 0 && b >= a {
				n += b - a + 1
			}
		} else if _, err := strconv.Atoi(seg); err == nil {
			n++
		}
	}
	if n == 0 {
		return 100
	}
	return n
}

// ===== 流水线调度：切片探测 + 待扫池 + 流式下发 =====
//
// 之前是「全量探测 → 切批 → 下发」，服务端探 7 万 IP 要 40 分钟，
// 这段时间所有节点全闲着。改成流水线后：
//
//	服务端：探一片(500) → 存活的进池 → 池满一批就发 → 继续探下一片
//	节点：  收到就扫，扫完领下一批
//
// 两边天然并行（探测在服务端进程、扫描在各节点），
// 总时长从「探测 + 扫描」变成「max(探测, 扫描)」，且首屏几秒就能看到分片下发。

const probeSliceSize = 500 // 每片探测多少个原始 IP

type taskPipeline struct {
	mu        sync.Mutex
	totalIPs  []string // 展开后的全部原始 IP
	offset    int      // 已探测到第几个
	alivePool []string // 已确认存活、还没下发的 IP
	probing   bool     // 是否有探测在进行
	dispatching bool   // 防止 DispatchNextBatch 重入
	// 落盘快照（避免持锁写文件）
	offsetSnapshot int
	poolSnapshot   []string
}

var pipelines sync.Map // taskID(uint) -> *taskPipeline

// ensurePipeline 懒加载任务流水线；首次会展开 IP 并读取已探测进度
func ensurePipeline(task *model.ScanTask) *taskPipeline {
	if v, ok := pipelines.Load(task.ID); ok {
		return v.(*taskPipeline)
	}
	ips := target.Resolve(strings.Split(task.IPRanges, ","), nil)
	p := &taskPipeline{totalIPs: ips, offset: task.ProbeOffset}
	// 优先用断点文件恢复：比 DB 的 offset 更完整（含待下发池），服务中断不丢存活 IP
	if off, pool, ok := loadPipelineFile(task.ID); ok {
		p.offset = off
		p.alivePool = pool
		log.Printf("[task #%d] 从断点文件恢复：offset=%d 待下发=%d", task.ID, off, len(pool))
	}
	if p.offset > len(ips) {
		p.offset = len(ips)
	}
	pipelines.Store(task.ID, p)
	log.Printf("[task #%d] 流水线初始化：总 %d 个 IP，已探测 %d", task.ID, len(ips), p.offset)
	return p
}

// CleanPipeline 任务重跑/删除时清空流水线状态
func CleanPipeline(taskID uint) {
	pipelines.Delete(taskID)
	removePipelineFile(taskID) // 断点一并清理
}

// DispatchNextBatch 推进任务：优先把池子里的 IP 发出去，池子不够就探下一片。
// 会在三个时机被调用：任务启动、节点完成一批、探测完成一片。
func DispatchNextBatch(taskID uint) {
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return
	}
	// 只有 running 状态才推进：paused/stopped/done 不派发、不探测
	if task.Status != "running" {
		return
	}

	p := ensurePipeline(&task)

	p.mu.Lock()
	if p.dispatching {
		p.mu.Unlock()
		return // 已有一次调度在跑，避免并发重复下发
	}
	p.dispatching = true
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.dispatching = false
		p.mu.Unlock()
	}()

	batchSize := task.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}

	// ---- 1. 池子里够一批就尽量铺满所有空闲节点（本轮内轮询，避免同一节点连吃多批） ----
	usedNodes := map[uint]bool{}
	for {
		p.mu.Lock()
		poolLen := len(p.alivePool)
		allProbed := p.offset >= len(p.totalIPs)
		p.mu.Unlock()

		if poolLen <= 0 {
			break
		}
		// 探测没结束时，池子攒到「最小批次」就先发，不必等满一批。
		// 稀疏网段（实测 172.16.48.0/24 只有 2 个存活）按整批攒会导致
		// 探几十片都发不出去一个分片，用户看不到任何扫描进展。
		minBatch := batchSize / 5
		if minBatch < 10 {
			minBatch = 10
		}
		if poolLen < minBatch && !allProbed {
			break
		}

		node := findIdleNodeExcluding(&task, usedNodes)
		if node == nil {
			break
		}
		usedNodes[node.ID] = true

		p.mu.Lock()
		n := batchSize
		if n > len(p.alivePool) {
			n = len(p.alivePool)
		}
		batch := append([]string(nil), p.alivePool[:n]...)
		p.alivePool = p.alivePool[n:]
		p.offsetSnapshot = p.offset
		p.poolSnapshot = append([]string(nil), p.alivePool...)
		p.mu.Unlock()
		// 落盘：下发后池子变小，必须同步，否则重启会重复下发同一批 IP
		if err := savePipelineFile(taskID, p.offsetSnapshot, p.poolSnapshot); err != nil {
			log.Printf("[task #%d] 断点落盘失败: %v", taskID, err)
		}

		var maxBatchNo int
		config.DB.Model(&model.AgentTask{}).Where("task_id = ?", taskID).
			Select("COALESCE(MAX(batch_no),0)").Scan(&maxBatchNo)

		at := model.AgentTask{
			TaskID:     task.ID,
			NodeID:     node.ID,
			NodeName:   node.Name,
			IPRanges:   strings.Join(batch, ","),
			BatchNo:    maxBatchNo + 1,
			BatchTotal: 0, // 总批数动态变化，前端按已完成数展示
			// 预估端口总数：agent 上报真实值后会覆盖，但分母从一开始就该是完整的，
			// 否则未开始的分片 total=0，会让 SUM(scanned)/SUM(total) 提前达到 100%
			Total: len(batch) * portCountOf(&task),
		}
		config.DB.Create(&at)
		tasklog.AppendTS(taskID, "[调度] 节点 %s 下发第 %d 批（%d IP × %d 端口 = %d，分片 #%d）",
			node.Name, at.BatchNo, len(batch), portCountOf(&task), at.Total, at.ID)
		WakeAgent(node.ID)
	}

	// ---- 2. 池子不够 -> 探下一片 ----
	p.mu.Lock()
	// 持续探测：不等池子用完，探测速度慢于扫描，提前把存活 IP 攒到池子里
	// 这样扫描节点永远不会等 IP（用户要求：存活探测不必等端口扫描）
	needProbe := p.offset < len(p.totalIPs) && !p.probing
	finished := p.offset >= len(p.totalIPs) && len(p.alivePool) == 0
	emptyButNotProbed := p.offset == 0 && len(p.alivePool) == 0 && len(p.totalIPs) == 0
	p.mu.Unlock()

	if needProbe {
		go probeNextSlice(taskID)
		return
	}

	// ---- 3. 探测全结束、池子也空了 -> 判断任务是否收敛 ----
	if finished && !emptyButNotProbed {
		var agg struct {
			Total int64
			Done  int64
		}
		config.DB.Model(&model.AgentTask{}).
			Where("task_id = ? AND status <> 'cancelled'", taskID).
			Select("COALESCE(SUM(total),0) AS total, COALESCE(SUM(scanned),0) AS done").Scan(&agg)
		if agg.Total > 0 {
			tasklog.AppendTS(taskID, "[进度] 分母 = 各分片端口数之和 = %d，已扫 %d（%d%%）",
				agg.Total, agg.Done, agg.Done*100/agg.Total)
		}
		var active int64
		config.DB.Model(&model.AgentTask{}).
			Where("task_id = ? AND status IN ?", taskID, []string{"pending", "running"}).
			Count(&active)
		if active == 0 {
			var doneCount int64
			config.DB.Model(&model.AgentTask{}).Where("task_id = ?", taskID).Count(&doneCount)
			if doneCount == 0 && p.offset >= len(p.totalIPs) && len(p.alivePool) == 0 && !p.probing {
				tasklog.AppendTS(taskID, "[存活] 全部 %d 个 IP 均不可达，任务结束（不下发扫描）", len(p.totalIPs))
				config.DB.Model(&model.ScanTask{}).Where("id = ?", taskID).
					Updates(map[string]interface{}{"status": "done", "finished_at": time.Now()})
				CleanPipeline(taskID)
				return
			}
			CheckTaskAllDone(taskID)
		}
	}
}

// probeNextSlice 探测下一片 IP，存活结果塞进池子后继续调度
func probeNextSlice(taskID uint) {
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return
	}
	p := ensurePipeline(&task)

	p.mu.Lock()
	if p.probing || p.offset >= len(p.totalIPs) {
		p.mu.Unlock()
		return
	}
	p.probing = true
	start := p.offset
	end := start + probeSliceSize
	if end > len(p.totalIPs) {
		end = len(p.totalIPs)
	}
	slice := append([]string(nil), p.totalIPs[start:end]...)
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.probing = false
		p.mu.Unlock()
	}()

	probeStart := time.Now()
	results := scanner.NewAliveCheck().Probe(context.Background(), slice, nil)
	var alive []string
	for _, r := range results {
		if r.Alive {
			alive = append(alive, r.IP)
		}
	}

	p.mu.Lock()
	p.alivePool = append(p.alivePool, alive...)
	p.offset = end
	// 必须在这里就把 probing 置回 false：
	// 下面会同步调用 DispatchNextBatch，它判断「要不要探下一片」时读的就是 p.probing。
	// 如果等 defer 再重置，那次调用会看到 probing=true 而跳过，之后又没人再触发，
	// 整条流水线就永久卡在第一片（实测 9 万 IP 停在 offset=500 不动）。
	p.probing = false
	poolLen := len(p.alivePool)
	snapOffset := p.offset
	snapPool := append([]string(nil), p.alivePool...)
	p.mu.Unlock()

	// 落盘断点：offset + 待下发池，服务中断后可续探/续发
	if err := savePipelineFile(taskID, snapOffset, snapPool); err != nil {
		log.Printf("[task #%d] 断点落盘失败: %v", taskID, err)
	}

	// 进度落库，服务重启后可续探；同时累加存活数（任务概览用）
	config.DB.Model(&model.ScanTask{}).Where("id = ?", taskID).
		Updates(map[string]interface{}{
			"probe_offset": end,
			"alive_count":  gorm.Expr("alive_count + ?", len(alive)),
		})

	tasklog.AppendTS(taskID, "[存活] 第 %d-%d 个（%d 个 IP）探测完成：存活 %d，待扫 %d，耗时 %v",
		start+1, end, len(slice), len(alive), poolLen, time.Since(probeStart).Round(time.Millisecond))

	DispatchNextBatch(taskID)
}

func chunkIPs(ips []string, size int) [][]string {
	if size <= 0 {
		size = 100
	}
	var out [][]string
	for i := 0; i < len(ips); i += size {
		end := i + size
		if end > len(ips) {
			end = len(ips)
		}
		out = append(out, ips[i:end])
	}
	return out
}

// findIdleNode 找一个空闲节点（无 pending/running 分片）
func findIdleNode(task *model.ScanTask) *model.Node {
	return findIdleNodeExcluding(task, nil)
}

// findIdleNodeExcluding 同上，但跳过 exclude 里的节点（用于同一轮里做轮询，
// 避免最快的节点扫完立刻又抢到下一批，其他节点全程闲着）
func findIdleNodeExcluding(task *model.ScanTask, exclude map[uint]bool) *model.Node {
	var nodes []model.Node
	if task.NodeIDs == "all" {
		config.DB.Where("status = 'online'").Find(&nodes)
	} else {
		var ids []uint
		for _, s := range strings.Split(task.NodeIDs, ",") {
			s = strings.TrimSpace(s)
			var id uint
			if _, err := fmt.Sscanf(s, "%d", &id); err == nil && id > 0 {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			config.DB.Where("status = 'online'").Find(&nodes)
		} else {
			config.DB.Where("id IN ? AND status = 'online'", ids).Find(&nodes)
		}
	}
	// 过滤掉心跳已超时的节点，避免把分片派给死节点后静默失败
	if len(nodes) > 0 {
		alive := make([]model.Node, 0, len(nodes))
		for i := range nodes {
			if nodeAlive(&nodes[i]) {
				alive = append(alive, nodes[i])
			}
		}
		nodes = alive
	}
	if len(nodes) == 0 {
		return nil
	}
	// 按并发能力排序（并发高的优先，尽量满载）
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Concurrency > nodes[j].Concurrency })
	for i := range nodes {
		if exclude != nil && exclude[nodes[i].ID] {
			continue
		}
		if IsNodeFused(nodes[i].ID) {
			continue // 熔断期内不派发
		}
		// 该节点当前没有未完成的分片
		var cnt int64
		config.DB.Model(&model.AgentTask{}).
			Where("node_id = ? AND task_id = ? AND status IN ?", nodes[i].ID, task.ID,
			[]string{"pending", "running"}).Count(&cnt)
		if cnt == 0 {
			return &nodes[i]
		}
	}
	return nil
}

// countAliveNodesForTask 本次任务可用的存活节点数（用于均分批次）
func countAliveNodesForTask(task *model.ScanTask) int {
	var nodes []model.Node
	if task.NodeIDs == "" || task.NodeIDs == "all" {
		config.DB.Find(&nodes)
	} else {
		var ids []uint
		for _, s := range strings.Split(task.NodeIDs, ",") {
			s = strings.TrimSpace(s)
			var id uint
			if _, err := fmt.Sscanf(s, "%d", &id); err == nil && id > 0 {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return 0
		}
		config.DB.Where("id IN ?", ids).Find(&nodes)
	}
	n := 0
	for i := range nodes {
		if nodeAlive(&nodes[i]) {
			n++
		}
	}
	return n
}

// DispatchNextBatch 下发下一个未下发的批（事件驱动：节点完成时调用）

// CheckTaskAllDone 所有子任务结束后收敛主任务状态
// 全部 done → done；有 failed → failed；有 cancelled（手动停止/故障转移）→ interrupted
func CheckTaskAllDone(taskID uint) {
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return
	}

	// ===== 判定一：存活探测必须全部完成 =====
	// 流水线是「边探测边扫描」，稀疏网段下前几个分片很快跑完，
	// 但后面还有几万个 IP 没探。只看「没有活跃分片」会把任务提前判 done。
	if v, ok := pipelines.Load(taskID); ok {
		p := v.(*taskPipeline)
		p.mu.Lock()
		probing, offset, total, pool := p.probing, p.offset, len(p.totalIPs), len(p.alivePool)
		p.mu.Unlock()
		if probing || offset < total {
			return // 还在探测
		}
		// ===== 判定二：存活 IP 必须全部下发 =====
		if pool > 0 {
			return
		}
	} else {
		// 服务重启后内存流水线丢失，用落库的进度兜底。
		// TotalIPs=0 是老数据/迁移前的任务，不能据此判定探测完成。
		if task.TotalIPs > 0 && task.ProbeOffset < task.TotalIPs {
			return
		}
	}

	// ===== 判定三：所有分片必须已回收 =====
	var shardCount, pending int64
	config.DB.Model(&model.AgentTask{}).Where("task_id = ?", taskID).Count(&shardCount)
	if shardCount == 0 {
		return // 一个分片都没有：要么还没开始，要么全部不可达（由调度侧单独处理）
	}
	config.DB.Model(&model.AgentTask{}).
		Where("task_id = ? AND status IN ?", taskID, []string{"pending", "running"}).Count(&pending)
	if pending > 0 {
		return
	}

	var failed, cancelled int64
	config.DB.Model(&model.AgentTask{}).Where("task_id = ? AND status = 'failed'", taskID).Count(&failed)
	config.DB.Model(&model.AgentTask{}).Where("task_id = ? AND status = 'cancelled'", taskID).Count(&cancelled)

	status := "done"
	switch {
	case failed > 0:
		status = "failed"
	case cancelled > 0:
		status = "interrupted"
	}
	config.DB.Model(&model.ScanTask{}).Where("id = ?", taskID).
		Updates(map[string]interface{}{"status": status, "finished_at": time.Now()})
	FinishTaskRun(taskID, status)

	var t model.ScanTask
	if err := config.DB.First(&t, taskID).Error; err == nil {
		label := map[string]string{"done": "完成", "failed": "失败", "interrupted": "中断"}[status]
		NotifyTaskDone(taskID, t.Name, label)
	}
}

// NodeHealth 节点健康检查（心跳时间 + 在线状态）

// StopAgentTask 停止某个分片，不再重新分配（agent 下次心跳/进度上报时收到 cancelled 并 kill 扫描进程）
func StopAgentTask(id uint) error {
	var at model.AgentTask
	if err := config.DB.First(&at, id).Error; err != nil {
		return err
	}
	now := time.Now()
	err := config.DB.Model(&at).Updates(map[string]interface{}{
		"status": "cancelled", "message": "已手动停止", "finished_at": now,
	}).Error
	if err == nil {
		tasklog.AppendTS(at.TaskID, "[节点 %s] 分片 #%d 已被手动停止，剩余 IP 不再扫描", at.NodeName, at.ID)
		CheckTaskAllDone(at.TaskID)
	}
	return err
}

// CancelAgentTask 取消某节点子任务（故障转移），IP 重新分配给其他在线节点
func CancelAgentTask(id uint) error {
	var at model.AgentTask
	if err := config.DB.First(&at, id).Error; err != nil {
		return err
	}
	now := time.Now()
	config.DB.Model(&at).Updates(map[string]interface{}{
		"status": "cancelled", "message": "已取消并重新分配", "finished_at": now,
	})
	return redispatch(at)
}

// redispatch 把取消子任务的 IP 分给其他在线节点
func redispatch(at model.AgentTask) error {
	var nodes []model.Node
	config.DB.Where("status = 'online' AND id <> ?", at.NodeID).Find(&nodes)
	if len(nodes) == 0 {
		return fmt.Errorf("没有其他在线节点可接管")
	}
	ips := strings.Split(at.IPRanges, ",")
	per := len(ips) / len(nodes)
	if per == 0 {
		per = 1
	}
	for i, n := range nodes {
		start := i * per
		end := start + per
		if i == len(nodes)-1 {
			end = len(ips)
		}
		if start >= len(ips) {
			break
		}
		chunk := ips[start:end]
		if len(chunk) == 0 {
			continue
		}
		nt := model.AgentTask{
			TaskID: at.TaskID, NodeID: n.ID, NodeName: n.Name,
			IPRanges: strings.Join(chunk, ","), Status: "pending", Total: len(chunk),
		}
		config.DB.Create(&nt)
		WakeAgent(n.ID)
	}
	return nil
}

// UpdateAgentProgress agent 上报进度
func UpdateAgentProgress(id uint, total, scanned, progress, eta int, message string) error {
	up := map[string]interface{}{"message": message}
	// agent 上报的 total/scanned 是端口维度，覆盖分片初始的 IP 数（进度/ETA 都以端口为准）
	if total > 0 {
		up["total"] = total
	}
	if scanned >= 0 {
		up["scanned"] = scanned
	}
	if progress > 0 {
		up["progress"] = progress
	}
	if eta > 0 {
		up["eta_seconds"] = eta
	}
	// 只更新未结束的分片，避免迟到的进度请求把 done/failed/cancelled 改回 running
	return config.DB.Model(&model.AgentTask{}).
		Where("id = ? AND status NOT IN ?", id, []string{"done", "failed", "cancelled"}).
		Updates(up).Error
}

// FinishAgentTask agent 完成任务
func FinishAgentTask(id uint, ok bool) error {
	now := time.Now()
	st, pr := "done", 100
	if !ok {
		st, pr = "failed", 0
	}

	// 已取消的分片保持 cancelled：
	// agent 收到取消指令会 kill 扫描进程，退出码非 0，agent 会报「失败」，
	// 若直接覆盖，用户「手动停止」会被显示成「执行失败」。
	var cur model.AgentTask
	if err := config.DB.First(&cur, id).Error; err == nil {
		if cur.Status == "cancelled" {
			return config.DB.Model(&model.AgentTask{}).Where("id = ?", id).
				Updates(map[string]interface{}{"finished_at": now}).Error
		}
		// 驱动熔断：成功清零连续失败计数，失败累加
		if st == "done" {
			RecordNodeSuccess(cur.NodeID)
		} else if st == "failed" {
			RecordNodeFailure(cur.NodeID, cur.NodeName, cur.TaskID,
				"分片 #"+strconv.Itoa(int(id))+" 执行失败")
		}
	}

	return config.DB.Model(&model.AgentTask{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": st, "progress": pr, "finished_at": now}).Error
}

// NodeHealth 节点健康检查（心跳时间 + 在线状态）
func NodeHealth(id uint) map[string]interface{} {
	var n model.Node
	if err := config.DB.First(&n, id).Error; err != nil {
		return map[string]interface{}{"healthy": false, "reason": "节点不存在"}
	}
	healthy := false
	latency := int64(-1)
	if n.LastHeartbeat != nil {
		latency = int64(time.Since(*n.LastHeartbeat).Seconds())
		healthy = latency <= 60
	}
	return map[string]interface{}{
		"healthy": healthy, "status": n.Status, "lastHeartbeat": n.LastHeartbeat,
		"latencySeconds": latency, "timeout": n.Timeout, "concurrency": n.Concurrency,
	}
}
