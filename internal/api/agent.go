package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
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
	// 已结束（取消/完成/被判中断/失败）：告知 agent 终止本机扫描进程。
	// 注意必须包含 stalled_ai：看门狗把分片判死并回收 IP 后，若不同时让节点停掉旧进程，
	// 节点上会留着僵尸扫描，和新派下来的分片抢资源 → 双双变慢 → 再次被判中断（死循环）。
	var at model.AgentTask
	if err := config.DB.First(&at, id).Error; err == nil &&
		(at.Status == "cancelled" || at.Status == "done" ||
			at.Status == "done_ai" || at.Status == "stalled_ai" || at.Status == "failed") {
		c.JSON(http.StatusOK, gin.H{"message": "cancelled", "cancelled": true,
			"cmd": service.TaskCtrlCmd{Action: "none"}})
		return
	}
	var req struct {
		Total    int    `json:"total"`
		Scanned  int    `json:"scanned"`
		Progress int    `json:"progress"`
		Message  string `json:"message"`
		Log      string `json:"log"`
		Eta      int    `json:"eta"`
		// ===== v0.0.27 IP 租约队列扩展（带 ip 即队列心跳）=====
		IP          string  `json:"ip"` // 当前 leased 单元；带即续租+落单元读数
		ProbesDone  int64   `json:"probesDone"`
		ProbesTotal int64   `json:"probesTotal"`
		Pps         float64 `json:"pps"`
		OpenFound   int64   `json:"openFound"`
		Identified  int64   `json:"identified"`
		Done        bool    `json:"unitDone"`   // true=单元完成（旧字段兼容）
		UnitStatus  string  `json:"unitStatus"` // "done"|"failed"，优先于 unitDone
	}
	_ = c.ShouldBindJSON(&req)
	// 队列会话心跳：续租 + 单元实时读数（probes_done/pps/open/identified），
	// unitStatus/unitDone 即该单元的完成落账。总进度聚合走 task_units（tasketa.go）。
	if req.IP != "" && at.TaskID > 0 {
		st := "progress"
		switch req.UnitStatus {
		case "done", "failed":
			st = req.UnitStatus
		default:
			if req.Done {
				st = "done"
			}
		}
		service.ReportUnit(at.TaskID, at.NodeID, req.IP, service.UnitReport{
			ProbesDone: req.ProbesDone, ProbesTotal: req.ProbesTotal,
			OpenFound: req.OpenFound, Identified: req.Identified, Pps: req.Pps,
			Status: st,
		})
	}
	// 原子抢占 pending → running，只有成功的那一次记日志（避免并发重复）
	now := time.Now()
	if res := config.DB.Model(&model.AgentTask{}).
		Where("id = ? AND status = 'pending'", id).
		Updates(map[string]interface{}{"status": "running", "started_at": now}); res.RowsAffected > 0 {
		ipCount := 0
		if at.IPRanges != "" {
			ipCount = len(strings.Split(at.IPRanges, ","))
		}
		if at.BatchNo > 0 { // 队列会话行（batch_no=0）的加入日志在 EnsureSession 已记
			tasklog.AppendTS(at.TaskID, "[节点 %s] 收到任务并开始执行（分片 #%d，%d 个 IP）",
				at.NodeName, at.ID, ipCount)
		}
	}
	// 兜底：老数据可能没有 started_at
	config.DB.Model(&model.AgentTask{}).
		Where("id = ? AND started_at IS NULL", id).
		Update("started_at", now)
	// 队列会话心跳（batch_no=0 且带 ip）不叠写分片式进度：会话 total/scanned
	// 由 NoteSessionUnit 在单元终报时精确累计（UpdateAgentProgress 是覆盖语义，
	// 会把已结算单元的累计值顶掉成"当前单元"值）。进度真值在 task_units。
	if !(at.BatchNo == 0 && req.IP != "") {
		_ = service.UpdateAgentProgress(uint(id), req.Total, req.Scanned, req.Progress, req.Eta, req.Message)
	}
	// 顺便缓存 agent 上报的终端日志
	if req.Log != "" && at.NodeID > 0 {
		updateNodeLog(at.NodeID, req.Log)
		updateShardLog(at.ID, req.Log)
	}
	// 控制指令随心跳响应投影（幂等状态：pause/resume/limit；agent 每次应用当前全量态）。
	// 取消仍走上面的 cancelled 字段（StopTask 把分片置 cancelled → 这里返回 cancelled:true）。
	c.JSON(http.StatusOK, gin.H{"message": "ok", "cancelled": false,
		"cmd": service.TaskCtrlSnapshot(at.TaskID)})
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
		updateNodeLog(at.NodeID, req.Log)
		updateShardLog(at.ID, req.Log)
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

// UploadNodeLicense 上传节点 Tscan License（内容写入 config/License.lic）
func UploadNodeLicense(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Content string `json:"content"` // License 文件全文
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if len(req.Content) < 10 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "License 内容过短，请检查文件"})
		return
	}
	var n model.Node
	if err := config.DB.First(&n, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
		return
	}
	if err := config.DB.Model(&model.Node{}).Where("id = ?", id).
		Update("license", req.Content).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "License 已保存，节点下次心跳自动写入"})
}

// GetNodeLicense 节点拉取自己的 License（agent 心跳后调用）
func GetNodeLicense(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var n model.Node
	if err := config.DB.First(&n, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"content": n.License})
}

// GetNodeLicenseStatus 查询各节点 License 是否已配置（不返回内容）
func GetNodeLicenseStatus(c *gin.Context) {
	type row struct {
		ID     uint   `json:"id"`
		Name   string `json:"name"`
		HasLic bool   `json:"hasLicense"`
	}
	var nodes []model.Node
	config.DB.Select("id", "name", "license").Find(&nodes)
	out := make([]row, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, row{ID: n.ID, Name: n.Name, HasLic: n.License != ""})
	}
	c.JSON(http.StatusOK, out)
}

// updateNodeLog 只在日志正文真的变化时才更新时间戳。
// 扫描监控靠「日志多久没变化」判断卡死，agent 每 2 秒都会上报同一段日志，
// 若能无条件刷新时间戳，就永远判不出卡死。
func updateNodeLog(nodeID uint, logText string) {
	var n model.Node
	if err := config.DB.Select("id", "terminal_log").First(&n, nodeID).Error; err != nil {
		return
	}
	if n.TerminalLog == logText {
		return
	}
	config.DB.Model(&model.Node{}).Where("id = ?", nodeID).
		Updates(map[string]interface{}{"terminal_log": logText, "terminal_log_at": time.Now()})
}

// updateShardLog 从节点累积日志里切出「本分片」的部分存到 agent_tasks.terminal_log。
// agent 上报的是进程级累积日志（一次任务跑几百个分片都堆在一起），
// 直接看节点日志会把不同分片混在一起，AI 诊断也会误读。
func updateShardLog(shardID uint, logText string) {
	marker := fmt.Sprintf("[agent-task #%d]", shardID)
	lines := strings.Split(logText, "\n")
	keep := make([]string, 0, len(lines))
	for _, ln := range lines {
		if strings.Contains(ln, marker) {
			// 去掉前缀，终端里只留扫描器原始输出
			if i := strings.Index(ln, marker); i >= 0 {
				ln = strings.TrimSpace(ln[i+len(marker):])
			}
			keep = append(keep, ln)
		}
	}
	if len(keep) == 0 {
		return
	}
	shardLog := strings.Join(keep, "\n")
	if len(shardLog) > 200000 {
		shardLog = shardLog[len(shardLog)-200000:]
	}
	config.DB.Model(&model.AgentTask{}).Where("id = ?", shardID).
		Update("terminal_log", shardLog)

	// 日志里已经出现结束标志 → 立刻判完成，不用等 15 分钟停滞检测。
	// （tscan 没有独立完成信号，这是唯一可靠的"提前收尾"依据）
	if hasFinishMark(shardLog) {
		var at model.AgentTask
		if err := config.DB.First(&at, shardID).Error; err == nil && at.Status == "running" {
			go service.DiagnoseShard(at, "log-finish")
		}
	}
}

// hasFinishMark 判断日志里是否出现扫描结束的标志
func hasFinishMark(logText string) bool {
	for _, k := range []string{"扫描结束", "导出HTML文件成功", "回车退出", "全部完成"} {
		if strings.Contains(logText, k) {
			return true
		}
	}
	return false
}

// ShardLog 获取某个分片的终端回显（节点终端弹窗用）
func ShardLog(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var at model.AgentTask
	if err := config.DB.First(&at, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "分片不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": at.ID, "node": at.NodeName, "ipRanges": at.IPRanges, "log": at.TerminalLog})
}
