package api

import (
	"strings"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"
	"asset-platform/internal/tasklog"

	"github.com/gin-gonic/gin"
)

// AgentStream agent 长连接（SSE）：服务端有任务时推送唤醒，避免 agent 空轮询
func AgentStream(c *gin.Context) {
	nodeID, _ := strconv.Atoi(c.Param("id"))
	if nodeID == 0 {
		c.Status(http.StatusBadRequest)
		return
	}
	ch := service.SubscribeAgent(uint(nodeID))
	defer service.UnsubscribeAgent(uint(nodeID), ch)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	fmt.Fprintf(c.Writer, "event: connected\ndata: ok\n\n")
	c.Writer.Flush()

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case msg := <-ch:
			fmt.Fprintf(c.Writer, "event: wake\ndata: %s\n\n", msg)
			c.Writer.Flush()
		case <-ticker.C:
			fmt.Fprintf(c.Writer, ": ping\n\n")
			c.Writer.Flush()
		case <-c.Request.Context().Done():
			return
		}
	}
}

// AgentTasks agent 拉取分配给自己的待执行子任务
func AgentTasks(c *gin.Context) {
	nodeID, _ := strconv.Atoi(c.Param("id"))
	var list []model.AgentTask
	config.DB.Where("node_id = ? AND status = 'pending'", nodeID).Order("id").Find(&list)
	c.JSON(http.StatusOK, list)
}

// AgentProgress agent 上报进度（仅在执行任务期间调用）
func AgentProgress(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	// 已被取消：直接告知 agent 终止
	var at model.AgentTask
	if err := config.DB.First(&at, id).Error; err == nil && at.Status == "cancelled" {
		c.JSON(http.StatusOK, gin.H{"message": "cancelled", "cancelled": true})
		return
	}
	var req struct {
		Total    int    `json:"total"`
		Scanned  int    `json:"scanned"`
		Progress int    `json:"progress"`
		Message  string `json:"message"`
		Log      string `json:"log"`
		Eta      int    `json:"eta"`
	}
	_ = c.ShouldBindJSON(&req)
	// 原子抢占 pending → running，只有成功的那一次记日志（避免并发重复）
	now := time.Now()
	if res := config.DB.Model(&model.AgentTask{}).
		Where("id = ? AND status = 'pending'", id).
		Updates(map[string]interface{}{"status": "running", "started_at": now}); res.RowsAffected > 0 {
		tasklog.AppendTS(at.TaskID, "[节点 %s] 收到任务并开始执行（分片 #%d，%d 个 IP）",
			at.NodeName, at.ID, at.Total)
	}
	// 兜底：老数据可能没有 started_at
	config.DB.Model(&model.AgentTask{}).
		Where("id = ? AND started_at IS NULL", id).
		Update("started_at", now)
	_ = service.UpdateAgentProgress(uint(id), req.Total, req.Scanned, req.Progress, req.Eta, req.Message)
	// 顺便缓存 agent 上报的终端日志
	if req.Log != "" && at.NodeID > 0 {
		config.DB.Model(&model.Node{}).Where("id = ?", at.NodeID).Update("terminal_log", req.Log)
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok", "cancelled": false})
}

// AgentFinish agent 完成子任务
func AgentFinish(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		OK  bool   `json:"ok"`
		Log string `json:"log"`
	}
	_ = c.ShouldBindJSON(&req)
	// 保存最终终端日志（避免 terminal_log 停留在最后一次 withLog=true 的时刻）
	var at model.AgentTask
	_ = config.DB.First(&at, id).Error
	if req.Log != "" && at.NodeID > 0 {
		config.DB.Model(&model.Node{}).Where("id = ?", at.NodeID).Update("terminal_log", req.Log)
	}
	_ = service.FinishAgentTask(uint(id), req.OK)
	// 事件驱动：节点完成就下发下一批
	if at.TaskID > 0 {
		go service.DispatchNextBatch(at.TaskID)
	}
	if at.TaskID > 0 {
		if req.OK {
			tasklog.AppendTS(at.TaskID, "[节点 %s] 分片 #%d 完成（%.1f 秒）",
				at.NodeName, at.ID, time.Since(at.CreatedAt).Seconds())
		} else {
			tasklog.AppendTS(at.TaskID, "[节点 %s] 分片 #%d 失败", at.NodeName, at.ID)
		}
		service.CheckTaskAllDone(at.TaskID)
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// AgentTaskDetail agent 获取子任务详情（含任务配置）
func AgentTaskDetail(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var at model.AgentTask
	if err := config.DB.First(&at, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "子任务不存在"})
		return
	}
	var task model.ScanTask
	config.DB.First(&task, at.TaskID)

	var node model.Node
	config.DB.First(&node, at.NodeID)

	c.JSON(http.StatusOK, gin.H{
		"agentTask": at,
		"task":      task,
		"nodeConf":  gin.H{"timeout": node.Timeout, "concurrency": node.Concurrency},
	})
}

// TaskAgentTasks 任务下的子任务进度（前端进度弹窗）
func TaskAgentTasks(c *gin.Context) {
	tid, _ := strconv.Atoi(c.Param("id"))
	var list []model.AgentTask
	config.DB.Where("task_id = ?", tid).Order("id").Find(&list)
	c.JSON(http.StatusOK, list)
}

// DispatchTask 手动触发分配
func DispatchTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var task model.ScanTask
	if err := config.DB.First(&task, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	if err := service.DispatchTask(&task); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已分配"})
}

// CancelAgentTask 故障转移：取消该节点子任务并重新分配
func CancelAgentTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.CancelAgentTask(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已取消并重新分配"})
}

// UpdateNodeConfig 更新节点扫描参数（超时 / 并发）
func UpdateNodeConfig(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Timeout     int `json:"timeout"`
		Concurrency int `json:"concurrency"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Timeout <= 0 {
		req.Timeout = 3000
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 1000
	}
	config.DB.Model(&model.Node{}).Where("id = ?", id).
		Updates(map[string]interface{}{"timeout": req.Timeout, "concurrency": req.Concurrency})
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

// NodeHealthCheck 节点健康检查
func NodeHealthCheck(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	c.JSON(http.StatusOK, service.NodeHealth(uint(id)))
}

// NodeTerminal 节点终端（返回该节点最近的任务日志尾部）
func NodeTerminal(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var node model.Node
	if err := config.DB.First(&node, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
		return
	}
	// 有 agent 上报的真实日志就优先返回
	var lines []string
	if strings.TrimSpace(node.TerminalLog) != "" {
		lines = strings.Split(node.TerminalLog, "\n")
	}
	// 补充任务摘要作为头部信息
	var tasks []model.AgentTask
	config.DB.Where("node_id = ?", id).Order("id desc").Limit(5).Find(&tasks)
	head := []string{
		fmt.Sprintf("[node] %s (%s) 状态=%s 并发=%d 超时=%dms 最近心跳=%s",
			node.Name, node.IP, node.Status, node.Concurrency, node.Timeout, node.LastHeartbeat),
	}
	for _, t := range tasks {
		head = append(head, fmt.Sprintf("[agent-task #%d] task=%d status=%s progress=%d%% %d/%d %s",
			t.ID, t.TaskID, t.Status, t.Progress, t.Scanned, t.Total, t.Message))
	}
	head = append(head, "──────── agent 实时日志 ────────")
	lines = append(head, lines...)
	if len(node.TerminalLog) == 0 {
		lines = append(lines, "(agent 尚未上报日志，下一次进度同步后会显示)")
	}
	c.JSON(http.StatusOK, gin.H{"node": node.Name, "lines": lines, "hasLog": node.TerminalLog != ""})
}

// StopAgentTask 停止某个节点的分片扫描（不重新分配）
func StopAgentTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.StopAgentTask(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已停止"})
}
