package service

import (
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

// DispatchTask 按节点并发能力拆分 IP 段并分配子任务
func DispatchTask(task *model.ScanTask) error {
	if task.NodeIDs == "" {
		// 用户没选节点时，默认走「本机节点」node-docker-1。
		// 服务端本机执行（进程内跑 TscanClient）和 node-docker-1 是同一台机器上的两套环境，
		// 统一走节点机制后，进度、日志、升级才有统一入口。
		var n model.Node
		if err := config.DB.Where("name = 'node-docker-1' AND status = 'online'").First(&n).Error; err == nil {
			task.NodeIDs = fmt.Sprint(n.ID)
			log.Printf("[dispatch] 任务 #%d 未指定节点，自动分配到本机节点 %s", task.ID, n.Name)
		} else {
			return nil // 没有本机节点时才退回服务端进程内执行
		}
	}
	// ========== 批量调度模式 ==========
	// 不再一次性把 IP 切给所有节点，而是 100 个 IP 一批
	// 哪个节点完成就给它下一批，更灵活，日志也显示调度过程
	ips := target.Resolve(strings.Split(task.IPRanges, ","), nil)
	batchSize := task.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	if len(ips) == 0 {
		return fmt.Errorf("目标展开后无有效 IP")
	}
	batchCount := (len(ips) + batchSize - 1) / batchSize
	tasklog.AppendTS(task.ID, "[调度] 任务 #%d 总目标 %d IP，批大小 %d，共 %d 批",
		task.ID, len(ips), batchSize, batchCount)
	// 立刻尝试下发第一批
	DispatchNextBatch(task.ID)
	return nil
}

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
	return config.DB.Model(&model.AgentTask{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": st, "progress": pr, "finished_at": now}).Error
}

// CheckTaskAllDone 所有子任务结束后收敛主任务状态
// 全部 done → done；有 failed → failed；有 cancelled（手动停止/故障转移）→ interrupted
func CheckTaskAllDone(taskID uint) {
	var pending, failed, cancelled int64
	config.DB.Model(&model.AgentTask{}).
		Where("task_id = ? AND status IN ?", taskID, []string{"pending", "running"}).Count(&pending)
	if pending > 0 {
		return
	}
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

	// 任务收敛后推送通知（异步，失败不影响主流程）
	var t model.ScanTask
	if err := config.DB.First(&t, taskID).Error; err == nil {
		label := map[string]string{"done": "完成", "failed": "失败", "interrupted": "中断"}[status]
		NotifyTaskDone(taskID, t.Name, label)
	}
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

// ==================== 批量调度 ====================

// chunkIPs 把 IP 切成批
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

// findIdleNode 找一个空闲节点（无 running 分片）
func findIdleNode(task *model.ScanTask) *model.Node {
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
	// 按并发能力排序（并发高的优先，尽量满载）
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Concurrency > nodes[j].Concurrency })
	for i := range nodes {
		// 该节点当前没有 running 分片
		var cnt int64
		config.DB.Model(&model.AgentTask{}).
			Where("node_id = ? AND task_id = ? AND status = 'running'", nodes[i].ID, task.ID).Count(&cnt)
		if cnt == 0 {
			return &nodes[i]
		}
	}
	return nil
}

// DispatchNextBatch 下发下一个未下发的批（事件驱动：节点完成时调用）
func DispatchNextBatch(taskID uint) {
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return
	}
	ips := target.Resolve(strings.Split(task.IPRanges, ","), nil)
	batchSize := task.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	batches := chunkIPs(ips, batchSize)

	// 已下发的最大批号
	var maxBatchNo int
	config.DB.Model(&model.AgentTask{}).Where("task_id = ?", taskID).
		Select("COALESCE(MAX(batch_no),0)").Scan(&maxBatchNo)

	if maxBatchNo >= len(batches) {
		// 全部下发完
		return
	}

	node := findIdleNode(&task)
	if node == nil {
		tasklog.AppendTS(taskID, "[调度] 第 %d/%d 批待下发，无空闲节点", maxBatchNo+1, len(batches))
		return
	}

	batch := batches[maxBatchNo]
	at := model.AgentTask{
		TaskID:     task.ID,
		NodeID:     node.ID,
		NodeName:   node.Name,
		IPRanges:   strings.Join(batch, ","),
		BatchNo:    maxBatchNo + 1,
		BatchTotal: len(batches),
	}
	config.DB.Create(&at)
	tasklog.AppendTS(taskID, "[调度] 节点 %s 下发第 %d/%d 批（%d IP，分片 #%d）",
		node.Name, at.BatchNo, at.BatchTotal, len(batch), at.ID)
	// 唤醒节点（SSE），否则 agent 不知道有新任务
	WakeAgent(node.ID)
}
