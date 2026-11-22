package api

import (
	"net/http"
	"strconv"
	"strings"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/scanner"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// AIDiagnoseShard 手动触发分片 AI 诊断（节点进度里的「AI诊断」按钮）
func AIDiagnoseShard(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var at model.AgentTask
	if err := config.DB.First(&at, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "分片不存在"})
		return
	}
	if at.Status != "running" && at.Status != "pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该分片已结束（" + at.Status + "），无需诊断"})
		return
	}
	verdict, note, err := service.DiagnoseShard(at, "manual")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	go service.DispatchNextBatch(at.TaskID)
	label := map[string]string{service.ShardAIDone: "AI完成", service.ShardAIStalled: "AI中断"}[verdict]
	c.JSON(http.StatusOK, gin.H{"verdict": verdict, "label": label, "note": note})
}

// ShardDiagnoseLog 分片诊断记录：结论 + 说明 + 时间 + 该分片相关的监控/诊断日志。
// 原来诊断结果只写进 agent_tasks，界面上点了「AI诊断」只有一个 toast，之后再也看不到。
func ShardDiagnoseLog(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var at model.AgentTask
	if err := config.DB.First(&at, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "分片不存在"})
		return
	}
	// 任务日志里跟这个分片有关的行（[扫描监控] / [AI诊断] 都带「分片 #id」）
	var logs []service.TaskLogEntry
	config.DB.Where("task_id = ? AND line LIKE ?", at.TaskID, "%分片 #"+strconv.Itoa(int(at.ID))+"%").
		Order("id desc").Limit(30).Find(&logs)

	c.JSON(http.StatusOK, gin.H{
		"id":       at.ID,
		"node":     at.NodeName,
		"status":   at.Status,
		"message":  at.Message,
		"scanned":  at.Scanned,
		"total":    at.Total,
		"ipRanges": at.IPRanges,
		"verdict":  at.AIVerdict,
		"note":     at.AIVerdictNote,
		"at":       at.AIVerdictAt,
		"logs":     logs,
	})
}

// GetAISettings AI 功能开关
func GetAISettings(c *gin.Context) {
	var s model.AISettings
	if err := config.DB.First(&s, 1).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"serviceIdentify": true, "scanMonitor": true})
		return
	}
	c.JSON(http.StatusOK, s)
}

// UpdateAISettings 更新 AI 功能开关
func UpdateAISettings(c *gin.Context) {
	var req struct {
		ServiceIdentify *bool `json:"serviceIdentify"`
		ScanMonitor     *bool `json:"scanMonitor"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	updates := map[string]interface{}{}
	if req.ServiceIdentify != nil {
		updates["service_identify"] = *req.ServiceIdentify
	}
	if req.ScanMonitor != nil {
		updates["scan_monitor"] = *req.ScanMonitor
	}
	if len(updates) > 0 {
		config.DB.Model(&model.AISettings{}).Where("id = 1").Updates(updates)
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// GetBackupSettings 备份设置
func GetBackupSettings(c *gin.Context) {
	var s model.BackupSettings
	if err := config.DB.First(&s, 1).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"enabled": true, "hour": 2, "keepDays": 7})
		return
	}
	c.JSON(http.StatusOK, s)
}

// UpdateBackupSettings 更新备份设置
func UpdateBackupSettings(c *gin.Context) {
	var req struct {
		Enabled  *bool `json:"enabled"`
		Hour     *int  `json:"hour"`
		KeepDays *int  `json:"keepDays"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	updates := map[string]interface{}{}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.Hour != nil {
		if *req.Hour < 0 || *req.Hour > 23 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "小时需在 0-23 之间"})
			return
		}
		updates["hour"] = *req.Hour
	}
	if req.KeepDays != nil {
		if *req.KeepDays < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "保留天数至少 1 天"})
			return
		}
		updates["keep_days"] = *req.KeepDays
	}
	if len(updates) > 0 {
		config.DB.Model(&model.BackupSettings{}).Where("id = 1").Updates(updates)
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// ListBackups 备份列表
func ListBackups(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"items": service.ListBackups(), "dir": service.BackupDirPath()})
}

// RunBackupNow 立即备份
func RunBackupNow(c *gin.Context) {
	name, size, err := service.RunBackup("手动")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "备份完成", "name": name, "size": size})
}

// DeleteBackup 删除备份
func DeleteBackup(c *gin.Context) {
	if err := service.DeleteBackup(c.Param("name")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// RestoreBackup 从备份恢复
func RestoreBackup(c *gin.Context) {
	safety, err := service.RestoreBackup(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "safetyBackup": safety})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已恢复。恢复前已自动备份当前数据：" + safety, "safetyBackup": safety})
}

// ReidentifyServices 批量重新识别 web 服务，用来补历史漏识别。
//
// 背景：AI 识别原来只在"引擎说 needAi"时触发，实测 2848 条 HTTP 服务里
// 有 2186 条从未识别，标题停在 `Redirection……`/`HTTP`/空 这类占位值。
//
// POST /api/services/reidentify?scope=placeholder&limit=200&ip=198.18.8.
//
//	scope=placeholder（默认）只挑占位标题；scope=unchecked 挑从未识别过的；
//	scope=all 全部 HTTP 服务（谨慎，逐条要抓包 + 可能渲染）
func ReidentifyServices(c *gin.Context) {
	limit := 100
	if v, err := strconv.Atoi(c.DefaultQuery("limit", "100")); err == nil && v > 0 && v <= 2000 {
		limit = v
	}
	scope := c.DefaultQuery("scope", "placeholder")
	ipFilter := strings.TrimSpace(c.Query("ip"))

	q := config.DB.Model(&model.Service{}).Where("protocol ILIKE 'http%'")
	switch scope {
	case "unchecked":
		q = q.Where("ai_checked_at IS NULL")
	case "all":
		// 不加条件
	}
	// 不再用 SQL 拼"占位标题"的匹配条件：中文「重定向中...」这类变体
	// 一改就得改 SQL，永远追不上。SQL 只负责粗筛，判定交给 PlaceholderTitle。
	if ipFilter != "" {
		q = q.Where("ip::text LIKE ?", ipFilter+"%")
	}
	var svcs []model.Service
	q.Order("last_seen DESC NULLS LAST").Limit(3000).Find(&svcs)

	queued := 0
	for _, s := range svcs {
		if queued >= limit {
			break
		}
		switch scope {
		case "all":
			// 全量：不筛标题
		case "unchecked":
			if s.AiCheckedAt != nil {
				continue
			}
		default: // placeholder
			if !scanner.PlaceholderTitle(s.Title, s.StatusCode) {
				continue
			}
		}
		service.EnqueueAIIdentify(s.LastTaskID, s.IP, s.Port)
		queued++
	}
	c.JSON(http.StatusOK, gin.H{
		"queued": queued, "candidates": len(svcs), "scope": scope,
		"message": "已入队，后台识别中（结果写回 title 并记 prev_title）",
	})
}

// AIIdentifyStats 识别队列与吞吐快照（GET /api/services/ai-stats）
//
// 为什么需要这个接口：积压对用户完全不可见，只能靠"服务没名字"去猜。
// 这里直接把队列长度、库里待识别条数、最近一小时吞吐、四步平均耗时、
// 渲染采用率一次给全，前端可以据此显示"识别中 3,214 条 · 约 6 小时"。
func AIIdentifyStats(c *gin.Context) {
	c.JSON(http.StatusOK, service.AIQueueStats())
}

// GetAIPrompts AI Agent 提示词（GET /api/ai/prompts）
//
// 两个角色：judge（研判页面是否异常、要不要渲染）、identify（识别系统名）。
// 一起返回默认值，前端可以直接做「恢复默认」和「已修改」标记。
func GetAIPrompts(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"items": service.AllPrompts()})
}

// UpdateAIPrompt 保存某个角色的提示词（PUT /api/ai/prompts）
// body: {"role":"judge","text":"..."}，text 为空表示恢复默认
func UpdateAIPrompt(c *gin.Context) {
	var req struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if err := service.SetPrompt(req.Role, req.Text); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存", "items": service.AllPrompts()})
}
