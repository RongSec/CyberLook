package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/middleware"
	"asset-platform/internal/model"
	"asset-platform/internal/scheduler"
	"asset-platform/internal/service"
	"asset-platform/internal/tasklog"

	"github.com/gin-gonic/gin"
)

var percentRe = regexp.MustCompile(`(\d+)%`)

// parsePercent 从进度条文本解析百分比（-1 表示无明确进度）
func parsePercent(s string) int {
	m := percentRe.FindStringSubmatch(s)
	if len(m) > 1 {
		n, _ := strconv.Atoi(m[1])
		if n >= 0 && n <= 100 {
			return n
		}
	}
	return -1
}

// CreateTask 创建扫描任务
func CreateTask(c *gin.Context) {
	var req struct {
		Name       string `json:"name"`
		IPRanges   string `json:"ipRanges"`
		PortMode   string `json:"portMode"`
		Ports      string `json:"ports"`
		Cron       string `json:"cron"`
		TemplateID *uint  `json:"templateId"`
		Modules    string `json:"modules"`
		Finger     string `json:"finger"`
		Thread     int    `json:"thread"`
		Timeout    int    `json:"timeout"`
		NoPing     bool   `json:"noPing"`
		BatchSize  int    `json:"batchSize"`
		NoSmart    bool   `json:"noSmart"`
		ExtraArgs  string `json:"extraArgs"`
		NodeIDs    string `json:"nodeIds"`
		Run        bool   `json:"run"` // true=创建并执行；false=仅保存为待执行
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Name == "" || req.IPRanges == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务名和 IP 范围必填"})
		return
	}
	in := service.TaskInput{
		Name: req.Name, IPRanges: req.IPRanges, PortMode: req.PortMode, Ports: req.Ports,
		Cron: req.Cron, TemplateID: req.TemplateID, Modules: req.Modules, Finger: req.Finger,
		Thread: req.Thread, Timeout: req.Timeout, NoPing: req.NoPing, NoSmart: req.NoSmart,
		ExtraArgs: req.ExtraArgs, NodeIDs: req.NodeIDs, BatchSize: req.BatchSize,
	}
	// 选了模板则用模板参数填充未显式指定的项
	if req.TemplateID != nil {
		if tpl, err := service.GetTemplate(*req.TemplateID); err == nil {
			if req.Modules == "" {
				in.Modules = tpl.Modules
			}
			if req.Finger == "" {
				in.Finger = tpl.Finger
			}
			if req.Ports == "" {
				in.Ports = tpl.Ports
			}
			if req.PortMode == "" {
				in.PortMode = tpl.PortMode
			}
			if req.Thread == 0 {
				in.Thread = tpl.Thread
			}
			if req.Timeout == 0 {
				in.Timeout = tpl.Timeout
			}
			if req.ExtraArgs == "" {
				in.ExtraArgs = tpl.ExtraArgs
			}
		}
	}
	task, err := service.CreateTask(in)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 仅「创建并执行」时才下发；只创建则保持 pending
	if req.Run {
		// nodeIds 为空时 DispatchTask 会自动指派给本机节点 node-docker-1，所以要调用后判断
		if err := service.DispatchTask(task); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "分配失败：" + err.Error()})
			return
		}
		if task.NodeIDs != "" {
			config.DB.Model(task).Updates(map[string]interface{}{"status": "running", "node_ids": task.NodeIDs})
		} else {
			go func() { _ = service.ExecuteTask(task.ID) }()
		}
	}
	scheduler.Reload()
	c.JSON(http.StatusOK, task)
}

// ListTasks 任务列表
func ListTasks(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	tasks, total := service.ListTasks(page, size)

	// 附带实时进度 + 分片统计
	type taskWithProgress struct {
		model.ScanTask
		Progress   string `json:"progress"`
		Percent    int    `json:"percent"`
		AgentTotal int64  `json:"agentTotal"` // 分片总数
		AgentDone  int64  `json:"agentDone"`  // 已完成分片
		AgentNodes string `json:"agentNodes"` // 参与的节点名，逗号分隔
		EtaSeconds int64  `json:"etaSeconds"` // 预计剩余秒数（取各分片最大值）
	}
	out := make([]taskWithProgress, len(tasks))
	for i := range tasks {
		// 卡住的 running 任务自动收敛：分片都已结束但主任务没更新
		if tasks[i].Status == "running" && tasks[i].NodeIDs != "" {
			var active int64
			config.DB.Model(&model.AgentTask{}).
				Where("task_id = ? AND status IN ?", tasks[i].ID, []string{"pending", "running"}).Count(&active)
			if active == 0 {
				service.CheckTaskAllDone(tasks[i].ID)
				config.DB.First(&tasks[i], tasks[i].ID)
			}
		}
		t := tasks[i]
		_, prog := tasklog.Get(t.ID)
		var totalShards, doneShards int64
		config.DB.Model(&model.AgentTask{}).Where("task_id = ?", t.ID).Count(&totalShards)
		config.DB.Model(&model.AgentTask{}).Where("task_id = ? AND status = 'done'", t.ID).Count(&doneShards)
		var names []string
		config.DB.Model(&model.AgentTask{}).Where("task_id = ?", t.ID).Distinct().Pluck("node_name", &names)
		percent := parsePercent(prog)
		var etaSec int64
		// 分布式任务：按各分片 IP 数加权算总进度（本地日志的 percent 不适用）
		if t.NodeIDs != "" && totalShards > 0 {
			var agg struct {
				Total int64
				Done  int64
				Eta   int64
			}
			// 排除 cancelled：故障转移会把原分片的 IP 重新分配给其他节点，
			// 原分片仍留在表里，若参与 SUM 会导致总端口/总进度翻倍
			config.DB.Model(&model.AgentTask{}).
				Where("task_id = ? AND status <> 'cancelled'", t.ID).
				Select("COALESCE(SUM(total),0) AS total, COALESCE(SUM(scanned),0) AS done, COALESCE(MAX(eta_seconds),0) AS eta").Scan(&agg)
			if agg.Total > 0 {
				percent = int(agg.Done * 100 / agg.Total)
				if percent > 100 {
					percent = 100
				}
			}
			etaSec = agg.Eta
		}
		out[i] = taskWithProgress{
			ScanTask:   t,
			Progress:   prog,
			Percent:    percent,
			AgentTotal: totalShards,
			AgentDone:  doneShards,
			AgentNodes: strings.Join(names, ", "),
			EtaSeconds: etaSec,
		}
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "items": out})
}

// RunTask 立即执行任务（异步）
// UpdateTask 修改任务内容（仅非执行中可修改）
func UpdateTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var task model.ScanTask
	if err := config.DB.First(&task, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	if task.Status == "running" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务执行中，请先停止后再修改"})
		return
	}
	var req struct {
		Name      string `json:"name"`
		IPRanges  string `json:"ipRanges"`
		Modules   string `json:"modules"`
		PortMode  string `json:"portMode"`
		Ports     string `json:"ports"`
		Thread    int    `json:"thread"`
		Timeout   int    `json:"timeout"`
		Finger    string `json:"finger"`
		NoPing    bool   `json:"noPing"`
		NoSmart   bool   `json:"noSmart"`
		ExtraArgs string `json:"extraArgs"`
		Cron      string `json:"cron"`
		NodeIDs   string `json:"nodeIds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" || req.IPRanges == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务名和扫描范围必填"})
		return
	}
	if err := config.DB.Model(&task).Updates(map[string]interface{}{
		"name": req.Name, "ip_ranges": req.IPRanges, "modules": req.Modules,
		"port_mode": req.PortMode, "ports": req.Ports, "thread": req.Thread,
		"timeout": req.Timeout, "finger": req.Finger, "no_ping": req.NoPing,
		"no_smart": req.NoSmart, "extra_args": req.ExtraArgs, "cron": req.Cron,
		"node_ids": req.NodeIDs,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	scheduler.Reload()
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

func RunTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var task model.ScanTask
	if err := config.DB.First(&task, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	// 重跑视为全新开始：清掉上一轮的分片记录，避免旧进度/旧分片干扰统计
	config.DB.Where("task_id = ?", task.ID).Delete(&model.AgentTask{})

	// 尝试分发到节点。nodeIds 为空时 DispatchTask 会自动指派给本机节点 node-docker-1，
	// 所以要调用之后再看 task.NodeIDs 是否被填充
	if err := service.DispatchTask(&task); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "分配失败：" + err.Error()})
		return
	}
	if task.NodeIDs != "" {
		config.DB.Model(&task).Update("status", "running")
		c.JSON(http.StatusOK, gin.H{"message": "已按并发分片，分发给节点执行"})
		return
	}
	go func() {
		_ = service.ExecuteTask(uint(id))
	}()
	c.JSON(http.StatusOK, gin.H{"message": "任务已提交执行"})
}

// StopTask 停止正在运行的任务
func StopTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.StopTask(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	tasklog.Append(uint(id), "任务已被手动停止")
	c.JSON(http.StatusOK, gin.H{"message": "已停止"})
}

// DeleteTask 删除任务
func DeleteTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteTask(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	scheduler.Reload()
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// BatchDeleteTasks 批量删除任务（含报告文件）
func BatchDeleteTasks(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择任务"})
		return
	}
	if err := service.BatchDeleteTasks(req.IDs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	scheduler.Reload()
	c.JSON(http.StatusOK, gin.H{"message": "已删除", "count": len(req.IDs)})
}

// TaskLog 任务实时日志（终端输出）
func TaskLog(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	lines, progress := tasklog.Get(uint(id))
	c.JSON(http.StatusOK, gin.H{"lines": lines, "progress": progress})
}

// TaskLogStream SSE 实时推送任务日志（原生终端风格，含进度条）
func TaskLogStream(c *gin.Context) {
	if !middleware.VerifyToken(c.Query("token")) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.Flush()

	sent := 0
	lastProgress := ""
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
			lines, progress := tasklog.Get(uint(id))
			for _, line := range lines[sent:] {
				fmt.Fprintf(c.Writer, "event: line\ndata: %s\n\n", line)
			}
			sent = len(lines)
			if progress != lastProgress {
				fmt.Fprintf(c.Writer, "event: progress\ndata: %s\n\n", progress)
				lastProgress = progress
			}
			c.Writer.Flush()
		}
	}
}
