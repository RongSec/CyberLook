package api

import (
	"fmt"
	"log"
	"net/http"
	"os"
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
		Engine     string `json:"engine"` // builtin / tscan
		Finger     string `json:"finger"`
		Thread     int    `json:"thread"`
		Timeout    int    `json:"timeout"`
		NoPing     bool   `json:"noPing"`
		AliveProbe *bool  `json:"aliveProbe"` // 省略=开启存活探测
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
	aliveProbe := true
	if req.AliveProbe != nil {
		aliveProbe = *req.AliveProbe
	}
	in := service.TaskInput{
		Name: req.Name, IPRanges: req.IPRanges, PortMode: req.PortMode, Ports: req.Ports,
		AliveProbe: aliveProbe,
		Cron:       req.Cron, TemplateID: req.TemplateID, Modules: req.Modules, Engine: req.Engine, Finger: req.Finger,
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
		// 下发是异步的：DispatchTask 里要做存活探测（几百个 IP 要 5~15 秒），
		// 同步执行会把 HTTP 请求挂住，前端弹窗要等探测完才关得上。
		id := task.ID
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[task #%d] 下发异常: %v", id, r)
				}
			}()
			var t model.ScanTask
			if err := config.DB.First(&t, id).Error; err != nil {
				return
			}
			if err := service.DispatchTask(&t); err != nil {
				log.Printf("[task #%d] 下发失败: %v", id, err)
				tasklog.AppendTS(id, "[调度] 下发失败：%v", err)
				config.DB.Model(&model.ScanTask{}).Where("id = ?", id).
					Update("status", "failed")
				return
			}
			if t.NodeIDs != "" {
				config.DB.Model(&model.ScanTask{}).Where("id = ?", id).
					Updates(map[string]interface{}{"status": "running", "node_ids": t.NodeIDs})
			} else {
				_ = service.ExecuteTask(id)
			}
		}()
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
		EtaSeconds int64  `json:"etaSeconds"` // 预计剩余秒数（-1=已暂停）
		// Queue 队列概览（pending/leased/done/failed + 探针真值；非队列任务为 null）
		Queue    *gin.H `json:"queue"`
		Overview gin.H  `json:"overview"` // 概览：IP/存活/应用/违规
	}
	out := make([]taskWithProgress, len(tasks))
	for i := range tasks {
		t := tasks[i]
		// 队列任务：精确进度（Σprobes_done/Σprobes_total）+ EWMA ETA，不走分片 SUM
		if t.QueueMode {
			percent := -1
			var eta int64
			if t.Status == "running" || t.Status == "paused" {
				percent, eta = service.QueueProgress(t.ID)
			} else if t.Status == "done" {
				percent = 100
			}
			qs := service.QueueStatsOf(t.ID)
			q := gin.H{"pending": qs.Pending, "leased": qs.Leased, "done": qs.Done,
				"failed": qs.FailedUnit, "probesDone": qs.ProbesDone, "probesTotal": qs.ProbesTotal}
			// 卡死兜底：单元全终态但没人触发收敛（比如会话行没建过）
			if t.Status == "running" && qs.Pending == 0 && qs.Leased == 0 && qs.Done+qs.FailedUnit > 0 {
				service.CheckQueueDone(t.ID)
				config.DB.First(&t, t.ID)
				if t.Status == "done" {
					percent = 100
					eta = 0
				}
			}
			_, prog := tasklog.Get(t.ID)
			out[i] = taskWithProgress{
				ScanTask: t, Progress: prog, Percent: percent, EtaSeconds: eta, Queue: &q,
				Overview:   buildOverview(t.ID, t.TotalIPs, t.AliveCount),
				AgentTotal: qs.Done + qs.FailedUnit,
				AgentDone:  qs.Done,
				AgentNodes: strings.Join(queueNodeNames(t.ID), ", "),
			}
			continue
		}
		// 卡住的 running 任务自动收敛：分片都已结束但主任务没更新
		if t.Status == "running" && t.NodeIDs != "" {
			var active, shards int64
			config.DB.Model(&model.AgentTask{}).Where("task_id = ?", t.ID).Count(&shards)
			config.DB.Model(&model.AgentTask{}).
				Where("task_id = ? AND status IN ?", t.ID, []string{"pending", "running"}).Count(&active)
			// 还没有分片说明仍在探测阶段，不能收敛
			if shards > 0 && active == 0 {
				service.CheckTaskAllDone(t.ID)
				config.DB.First(&t, t.ID)
			}
		}
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
			Overview:   buildOverview(t.ID, t.TotalIPs, t.AliveCount),
			AgentTotal: totalShards,
			AgentDone:  doneShards,
			AgentNodes: strings.Join(names, ", "),
			EtaSeconds: etaSec,
		}
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "items": out})
}

// queueNodeNames 队列任务的参与节点名（会话行 distinct）。
func queueNodeNames(taskID uint) []string {
	var names []string
	config.DB.Model(&model.AgentTask{}).
		Where("task_id = ? AND batch_no = 0", taskID).Distinct().Pluck("node_name", &names)
	return names
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
		Engine    string `json:"engine"`
		PortMode  string `json:"portMode"`
		Ports     string `json:"ports"`
		Thread    int    `json:"thread"`
		Timeout   int    `json:"timeout"`
		BatchSize int    `json:"batchSize"`
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
		"engine":    req.Engine,
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
	// 有断点（任务被中断/停止）则续跑，不清零探测进度
	// 只有全新执行（无断点）才重置，避免概览存活数累加翻倍
	if _, _, hasBP := loadPipelineFileForAPI(task.ID); hasBP && task.Status == "stopped" {
		// 续跑：保留 probe_offset / alive_count / 池子
	} else {
		service.CleanPipeline(task.ID) // 重跑：清空流水线状态
		config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).
			Updates(map[string]interface{}{"probe_offset": 0, "alive_count": 0})
	}
	service.StartTaskRun(task.ID) // 记录本次执行（定时任务详情用）

	// 尝试分发到节点。nodeIds 为空时 DispatchTask 会自动指派给本机节点 node-docker-1，
	// 所以要调用之后再看 task.NodeIDs 是否被填充
	if err := service.DispatchTask(&task); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "分配失败：" + err.Error()})
		return
	}
	if task.NodeIDs != "" {
		// 把自动分配的结果落库，前端才能显示「派给了哪个节点」
		config.DB.Model(&task).Updates(map[string]interface{}{"status": "running", "node_ids": task.NodeIDs})
		c.JSON(http.StatusOK, gin.H{"message": "已按并发分片，分发给节点执行"})
		return
	}
	go func() {
		_ = service.ExecuteTask(uint(id))
	}()
	c.JSON(http.StatusOK, gin.H{"message": "任务已提交执行"})
}

// TaskControl 任务实时调控（用户拍板：取消 + 实时进度 + 动态调速）。
// body: {"action":"pause|resume|limit|cancel","concurrency":10-8000,"timeoutMs":100-30000}
// 语义：
//   - pause/resume/limit 写**内存调控态**（service.TaskCtrl），不改任务配置、不动调度状态机；
//     agent 执行的任务由该任务下一条 /progress 心跳响应投影（cmd 字段）带下去，
//     本地执行的任务由 ExecuteTask 轮询器进程内直调（都 ≤2s 生效）。
//   - cancel 收敛进既有 StopTask 链路（分片置 cancelled → 心跳 cancelled:true），
//     不新增状态。
//   - 进程重启后调控态回到默认（执行中、按任务参数速率）——可接受：重启本身即打断扫描。
func TaskControl(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var task model.ScanTask
	if err := config.DB.First(&task, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	if task.Status != "running" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只有运行中的任务可以调控"})
		return
	}
	var req struct {
		Action      string `json:"action"`
		Concurrency int    `json:"concurrency"`
		TimeoutMs   int    `json:"timeoutMs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	switch req.Action {
	case "pause":
		service.SetTaskPause(uint(id), true)
		tasklog.AppendTS(uint(id), "[调控] 扫描已暂停（节点保持占用，进度冻结；许可清零，在途探针自然结束后归零）")
	case "resume":
		service.SetTaskPause(uint(id), false)
		tasklog.AppendTS(uint(id), "[调控] 扫描已继续")
	case "limit":
		if req.Concurrency < 10 || req.Concurrency > 8000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "并发需在 10-8000 之间"})
			return
		}
		if req.TimeoutMs < 100 || req.TimeoutMs > 30000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "超时需在 100-30000ms 之间"})
			return
		}
		service.SetTaskLimit(uint(id), req.Concurrency, req.TimeoutMs)
		tasklog.AppendTS(uint(id), "[调控] 并发=%d 超时=%dms 已下发（下个心跳 ≤2s 生效）", req.Concurrency, req.TimeoutMs)
	case "cancel":
		if err := service.StopTask(uint(id)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		tasklog.Append(uint(id), "任务已被调控取消")
		c.JSON(http.StatusOK, gin.H{"message": "已取消"})
		return
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "action 仅支持 pause/resume/limit/cancel"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已下发", "cmd": service.TaskCtrlSnapshot(uint(id))})
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

// loadPipelineFileForAPI 判断任务是否有断点文件（续跑用）
func loadPipelineFileForAPI(taskID uint) (offset int, pool []string, ok bool) {
	// service.loadPipelineFile 是内部函数，这里复用导出的判断
	// 通过探查文件是否存在
	path := "data/pipeline/" + strconv.Itoa(int(taskID)) + ".json"
	if _, err := os.Stat(path); err != nil {
		return 0, nil, false
	}
	return 0, nil, true
}

// PauseTask 暂停任务
func PauseTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.PauseTask(uint(id)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已暂停"})
}

// ResumeTask 继续任务
func ResumeTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.ResumeTask(uint(id)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已继续"})
}

// RestartTask 重跑任务
func RestartTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.RestartTask(uint(id)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已重跑"})
}
