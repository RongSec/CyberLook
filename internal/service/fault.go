package service

import (
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// ===== 熔断状态（内存） =====
// 连续失败达到阈值后熔断该节点，窗口内不再派发；到期自动恢复。
// 属于「自动退避 + 自动重发」：熔断期间的分片会被收回，重新派给其他节点。
// 熔断规则从 system_settings 读，可在节点页面调整
func getFuseConfig() (failThreshold int, duration time.Duration) {
	failThreshold = 5
	duration = 5 * time.Minute
	var row struct {
		FuseFailThreshold int
		FuseDurationMin   int
	}
	config.DB.Raw("SELECT fuse_fail_threshold, fuse_duration_min FROM system_settings WHERE id=1").Scan(&row)
	if row.FuseFailThreshold > 0 {
		failThreshold = row.FuseFailThreshold
	}
	if row.FuseDurationMin > 0 {
		duration = time.Duration(row.FuseDurationMin) * time.Minute
	}
	return
}

type fuseState struct {
	FailCount  int
	FusedUntil time.Time
}

var (
	fuseMu sync.Mutex
	fuses  = map[uint]*fuseState{} // nodeID -> 状态
)

// RecordNodeFailure 记一次节点任务失败，达到阈值则熔断
func RecordNodeFailure(nodeID uint, nodeName string, taskID uint, reason string) {
	fuseMu.Lock()
	f := fuses[nodeID]
	if f == nil {
		f = &fuseState{}
		fuses[nodeID] = f
	}
	f.FailCount++
	cnt := f.FailCount
	fused := false
	threshold, fuseDur := getFuseConfig()
	if cnt >= threshold {
		f.FusedUntil = time.Now().Add(fuseDur)
		f.FailCount = 0
		fused = true
	}
	fuseMu.Unlock()

	RecordFault("shard_failed", nodeID, nodeName, taskID, reason)

	if fused {
		threshold, fuseDur := getFuseConfig()
		RecordFault("node_fused", nodeID, nodeName, taskID,
			"连续失败达到阈值 "+strconv.Itoa(threshold)+"，熔断 "+fuseDur.String()+"，期间不再派发新任务")
		log.Printf("[熔断] 节点 %s (#%d) 连续失败 %d 次，熔断至 %s", nodeName, nodeID, cnt,
			time.Now().Add(fuseDur).Format("15:04:05"))
	}
}

// RecordNodeSuccess 成功一次即清零连续失败计数
func RecordNodeSuccess(nodeID uint) {
	fuseMu.Lock()
	if f := fuses[nodeID]; f != nil {
		f.FailCount = 0
	}
	fuseMu.Unlock()
}

// IsNodeFused 判断节点是否处于熔断窗口内
func IsNodeFused(nodeID uint) bool {
	fuseMu.Lock()
	defer fuseMu.Unlock()
	f := fuses[nodeID]
	if f == nil {
		return false
	}
	if !f.FusedUntil.IsZero() && time.Now().Before(f.FusedUntil) {
		return true
	}
	// 熔断已到期，重置状态
	if !f.FusedUntil.IsZero() {
		f.FusedUntil = time.Time{}
		f.FailCount = 0
	}
	return false
}

// ===== 故障记录 =====

// RecordFault 写一条故障记录；通知开关打开时同步推送
func RecordFault(kind string, nodeID uint, nodeName string, taskID uint, message string) {
	now := time.Now()
	f := model.Fault{
		Kind: kind, NodeID: nodeID, NodeName: nodeName,
		TaskID: taskID, Message: message, CreatedAt: &now,
	}
	if err := config.DB.Create(&f).Error; err != nil {
		log.Printf("[fault] 写入失败: %v", err)
		return
	}

	var s NotifySettings
	if err := config.DB.First(&s, 1).Error; err == nil && s.OnFault {
		title := map[string]string{
			"server_restart": "服务端中断",
			"node_offline":   "节点离线",
			"node_fused":     "节点熔断",
			"shard_failed":   "分片失败",
		}[kind]
		if title == "" {
			title = "故障告警"
		}
		go Broadcast(title, message)
	}
}

// ListFaults 故障列表
func ListFaults(kind string, page, size int) ([]model.Fault, int64) {
	var total int64
	q := config.DB.Model(&model.Fault{})
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	q.Count(&total)
	var rows []model.Fault
	q.Order("created_at DESC").Offset((page - 1) * size).Limit(size).Find(&rows)
	return rows, total
}

// ===== 孤儿分片自动重派 =====

// RescheduleOrphanShards 巡检一次：节点离线/熔断时，把它名下未完成的分片收回，
// IP 塞回存活池，由流水线重新派发给其他节点。返回收回的分片数。
func RescheduleOrphanShards() int {
	var nodes []model.Node
	config.DB.Find(&nodes)
	dead := map[uint]bool{}
	for i := range nodes {
		if !nodeAlive(&nodes[i]) || IsNodeFused(nodes[i].ID) {
			dead[nodes[i].ID] = true
		}
	}
	if len(dead) == 0 {
		return 0
	}

	var deadIDs []uint
	for k := range dead {
		deadIDs = append(deadIDs, k)
	}

	var shards []model.AgentTask
	config.DB.Where("node_id IN ? AND status IN ?", deadIDs, []string{"pending", "running"}).
		Find(&shards)
	if len(shards) == 0 {
		return 0
	}

	for _, s := range shards {
		ips := splitIPs(s.IPRanges)
		// 把这个分片的 IP 塞回该任务的存活池
		if v, ok := pipelines.Load(s.TaskID); ok {
			p := v.(*taskPipeline)
			p.mu.Lock()
			p.alivePool = append(p.alivePool, ips...)
			snapOffset := p.offset
			snapPool := append([]string(nil), p.alivePool...)
			p.mu.Unlock()
			_ = savePipelineFile(s.TaskID, snapOffset, snapPool)
		}
		config.DB.Model(&model.AgentTask{}).Where("id = ?", s.ID).
			Updates(map[string]interface{}{
				"status":      "cancelled",
				"message":     "节点离线/熔断，IP 已收回重新派发",
				"finished_at": time.Now(),
			})
		RecordFault("node_offline", s.NodeID, s.NodeName, s.TaskID,
			"分片 #"+strconv.Itoa(int(s.ID))+" 所在节点不可用，已收回 "+strconv.Itoa(len(ips))+" 个 IP 重新派发")
		go DispatchNextBatch(s.TaskID)
	}
	return len(shards)
}

func splitIPs(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// GetFaultsRetentionDays 读取故障保留天数（默认 30）
func GetFaultsRetentionDays() int {
	days := 30
	var row struct{ FaultsRetentionDays int }
	config.DB.Raw("SELECT faults_retention_days FROM system_settings WHERE id=1").Scan(&row)
	if row.FaultsRetentionDays > 0 {
		days = row.FaultsRetentionDays
	}
	return days
}

// CleanupOldFaults 清理超过保留期的故障记录
func CleanupOldFaults() int64 {
	days := GetFaultsRetentionDays()
	res := config.DB.Where("created_at < now() - (? * interval '1 day')", days).Delete(&model.Fault{})
	if res.RowsAffected > 0 {
		log.Printf("[fault] 清理 %d 条超过 %d 天的故障记录", res.RowsAffected, days)
	}
	return res.RowsAffected
}

// StartFaultsCleanup 启动每日清理任务（每天 03:00 跑一次）
func StartFaultsCleanup() {
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location())
			if !next.After(now) {
				next = next.Add(24 * time.Hour)
			}
			time.Sleep(next.Sub(now))
			CleanupOldFaults()
		}
	}()
}
