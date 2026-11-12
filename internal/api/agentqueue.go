package api

// ===== IP 租约队列的 agent 通道（v0.0.27） =====
//
// 端点：
//   GET  /api/agent/nodes/:id/queue-tasks   该节点可消费的队列任务（含可领取标记）
//   POST /api/agent/tasks/:id/claim         领取一个单 IP 单元（:id=scan task id，body 带 nodeId）
//   POST /api/agent/tasks/:id/progress      心跳（既有端点扩展：body 带 ip 即续租+单元读数上报）
//
// 认证：claim 走 X-Node-Token（:id 是主任务 ID，不是分片 ID，AgentTaskAuth 不适用，
// 这里按 body.nodeId 校验节点凭据，与 AgentAuth 同口径）。

import (
	"errors"
	"net/http"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// queueTaskItem 队列任务引用（agent 领取循环的路标）。
type queueTaskItem struct {
	TaskID    uint   `json:"taskId"`
	TaskName  string `json:"taskName"`
	Claimable bool   `json:"claimable"` // 有 pending 单元才值得发起 claim（省一次往返 + 防空调）
	SessionID uint   `json:"sessionId"` // 该节点在该任务上的会话行 ID（agent 清理审计 Project 用）
}

// AgentQueueTasks 列出该节点可消费的在途队列任务。
func AgentQueueTasks(c *gin.Context) {
	nodeID, _ := strconv.Atoi(c.Param("id"))
	if nodeID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "节点 ID 无效"})
		return
	}
	refs := service.QueueTasksForNode(uint(nodeID))
	out := make([]queueTaskItem, 0, len(refs))
	for _, r := range refs {
		out = append(out, queueTaskItem{
			TaskID: r.TaskID, TaskName: r.TaskName,
			Claimable: service.PeekQueueClaimable(r.TaskID),
			SessionID: r.SessionID,
		})
	}
	c.JSON(http.StatusOK, gin.H{"tasks": out})
}

// AgentClaimUnit 原子领取扫描单元（v0.0.27 支持批量）。
// body: {"nodeId":n,"n":k}；k 省略=1。响应：
//
//	hasUnit=true → units:[{ip,probesTotal,sessionId},...]（旧字段 unit 仍返回第一个，兼容老 agent）
//	hasUnit=false → 队列空 / 任务不可领 / 已暂停（paused 标记让 agent 退避而不是退出）
func AgentClaimUnit(c *gin.Context) {
	taskID, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		NodeID uint `json:"nodeId"`
		N      int  `json:"n"` // 期望领取个数（服务端钳制 1..MaxClaimBatch）
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.NodeID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 nodeId"})
		return
	}
	// 节点凭据校验（与 AgentAuth 同口径）
	var node model.Node
	if err := config.DB.First(&node, req.NodeID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
		return
	}
	if tk := c.GetHeader("X-Node-Token"); tk == "" || node.Token == "" || node.Token != tk {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "节点凭据无效"})
		return
	}
	if req.N <= 0 {
		req.N = 1
	}

	units, err := service.ClaimForNodeBatch(uint(taskID), req.NodeID, node.Name, req.N)
	if err != nil && !errors.Is(err, service.ErrQueuePaused) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(units) == 0 {
		// 暂停与空队列都回 hasUnit=false；agent 侧按 paused 退避节奏
		c.JSON(http.StatusOK, gin.H{"hasUnit": false, "paused": errors.Is(err, service.ErrQueuePaused)})
		return
	}
	// 任务配置 + 节点参数（等价 AgentTaskDetail 的载荷，省一次往返）
	var task model.ScanTask
	config.DB.First(&task, taskID)
	list := make([]gin.H, 0, len(units))
	for _, u := range units {
		list = append(list, gin.H{
			"ip": u.IP, "probesTotal": u.ProbesTotal, "sessionId": u.SessionID,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"hasUnit":  true,
		"units":    list,
		"unit":     list[0], // 兼容只认 unit 的老 agent
		"task":     task,
		"nodeConf": gin.H{"timeout": node.Timeout, "concurrency": node.Concurrency},
		// cmd = 该任务的调控幂等状态（pause/limit）。agent 每轮 claim 都拿一次并对
		// **共享 limiter** 应用一遍——否则 limit 只能靠"恰好有单元在 tick"生效，
		// K 路并行时会出现"limit 已下发但 pps 不降"（v0.0.27 实测踩到）。
		"cmd": service.TaskCtrlSnapshot(uint(taskID)),
	})
}
