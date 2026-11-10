package api

import (
	"net/http"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
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

// ReidentifyServices 批量重新识别历史上"标题不清晰"的 web 服务。
// AI 识别只在服务入库时触发，老数据没有人去覆盖，这个接口用来补历史。
// POST /api/services/reidentify?limit=100
func ReidentifyServices(c *gin.Context) {
	limit := 100
	if v, err := strconv.Atoi(c.DefaultQuery("limit", "100")); err == nil && v > 0 && v <= 1000 {
		limit = v
	}
	var svcs []model.Service
	config.DB.Where("protocol ILIKE 'http%'").Order("last_seen desc").Limit(5000).Find(&svcs)
	queued := 0
	for _, s := range svcs {
		if service.TitleClear(s.Title) {
			continue
		}
		service.EnqueueAIIdentify(s.LastTaskID, s.IP, s.Port)
		queued++
		if queued >= limit {
			break
		}
	}
	c.JSON(http.StatusOK, gin.H{"queued": queued, "scanned": len(svcs), "message": "已入队，后台识别中（结果会写回并记 prev_title）"})
}

// ReidentifyServices 批量重新识别历史上"标题不清晰"的 web 服务。