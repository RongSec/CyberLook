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
		})
	}
	c.JSON(http.StatusOK, gin.H{"tasks": out})
}

// AgentClaimUnit 原子领取一个单 IP 扫描单元。
// body: {"nodeId":n}；响应 hasUnit=false（队列空/任务不可领/已暂停）或单元+任务配置+会话。
func AgentClaimUnit(c *gin.Context) {
	taskID, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		NodeID uint `json:"nodeId"`
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

	u, err := service.ClaimForNode(uint(taskID), req.NodeID, node.Name)
	if err != nil && !errors.Is(err, service.ErrQueuePaused) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if u == nil {
		// 暂停与空队列都回 hasUnit=false；agent 侧按 paused 退避节奏
		c.JSON(http.StatusOK, gin.H{"hasUnit": false, "paused": errors.Is(err, service.ErrQueuePaused)})
		return
	}
	// 任务配置 + 节点参数（等价 AgentTaskDetail 的载荷，省一次往返）
	var task model.ScanTask
	config.DB.First(&task, taskID)
	c.JSON(http.StatusOK, gin.H{
		"hasUnit": true,
		"unit": gin.H{
			"ip": u.IP, "probesTotal": u.ProbesTotal, "sessionId": u.SessionID,
		},
		"task":     task,
		"nodeConf": gin.H{"timeout": node.Timeout, "concurrency": node.Concurrency},
	})
}
