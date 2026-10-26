package api

import (
	"time"
	"net/http"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"github.com/gin-gonic/gin"
)

// TaskOverview 任务概览：本次扫描的 IP 数 / 存活数 / 发现应用 / 违规
func TaskOverview(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var t model.ScanTask
	if err := config.DB.First(&t, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}

	// 找本次执行的时间窗口（避免多次执行叠加）
	startAt := time.Time{}
	var run model.TaskRun
	if err := config.DB.Where("task_id = ?", id).Order("started_at DESC").First(&run).Error; err == nil && run.StartedAt != nil {
		startAt = *run.StartedAt
	}

	var appsFound, appsNew, vioTotal, vioNew int64
	q := config.DB.Model(&model.Service{}).Where("last_task_id = ?", id)
	if !startAt.IsZero() {
		q = q.Where("last_seen >= ?", startAt) // 只算本次执行扫到的
	}
	q.Count(&appsFound)

	qn := config.DB.Model(&model.Service{}).Where("task_id = ?", id)
	if !startAt.IsZero() {
		qn = qn.Where("first_seen >= ?", startAt)
	}
	qn.Count(&appsNew)

	qv := config.DB.Model(&model.ComplianceResult{}).Where("last_task_id = ?", id)
	if !startAt.IsZero() {
		qv = qv.Where("last_seen >= ?", startAt)
	}
	qv.Count(&vioTotal)

	qvn := config.DB.Model(&model.ComplianceResult{}).Where("task_id = ?", id)
	if !startAt.IsZero() {
		qvn = qvn.Where("detected_at >= ?", startAt)
	}
	qvn.Count(&vioNew)

	c.JSON(http.StatusOK, gin.H{
		"taskId":        t.ID,
		"totalIps":      t.TotalIPs,
		"aliveIps":      t.AliveCount,
		"appsFound":     appsFound,
		"appsNew":       appsNew,
		"violations":    vioTotal,
		"violationsNew": vioNew,
	})
}

// ListScheduledTasks 定时任务列表（cron 非空的都算）
func ListScheduledTasks(c *gin.Context) {
	var tasks []model.ScanTask
	config.DB.Where("cron IS NOT NULL AND cron <> ''").Order("id DESC").Find(&tasks)

	type item struct {
		model.ScanTask
		LastRun *model.TaskRun `json:"lastRun"`
		RunCount int64         `json:"runCount"`
	}
	out := make([]item, len(tasks))
	for i, t := range tasks {
		out[i].ScanTask = t
		var run model.TaskRun
		if err := config.DB.Where("task_id = ?", t.ID).Order("started_at DESC").First(&run).Error; err == nil {
			out[i].LastRun = &run
		}
		config.DB.Model(&model.TaskRun{}).Where("task_id = ?", t.ID).Count(&out[i].RunCount)
	}
	c.JSON(http.StatusOK, out)
}

// ListTaskRuns 某个任务的执行历史
func ListTaskRuns(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var runs []model.TaskRun
	config.DB.Where("task_id = ?", id).Order("started_at DESC").Limit(100).Find(&runs)
	if runs == nil {
		runs = []model.TaskRun{}
	}
	c.JSON(http.StatusOK, runs)
}

// buildOverview 任务概览（列表页批量调用）
// 只统计「最近一次执行」时间窗口内的数据，避免多次重跑叠加
func buildOverview(taskID uint, totalIPs, aliveIPs int) gin.H {
	startAt := time.Time{}
	var run model.TaskRun
	if err := config.DB.Where("task_id = ?", taskID).Order("started_at DESC").First(&run).Error; err == nil && run.StartedAt != nil {
		startAt = *run.StartedAt
	}

	var appsFound, appsNew, vioTotal, vioNew int64
	q := config.DB.Model(&model.Service{}).Where("last_task_id = ?", taskID)
	if !startAt.IsZero() {
		q = q.Where("last_seen >= ?", startAt)
	}
	q.Count(&appsFound)

	qn := config.DB.Model(&model.Service{}).Where("task_id = ?", taskID)
	if !startAt.IsZero() {
		qn = qn.Where("first_seen >= ?", startAt)
	}
	qn.Count(&appsNew)

	qv := config.DB.Model(&model.ComplianceResult{}).Where("last_task_id = ?", taskID)
	if !startAt.IsZero() {
		qv = qv.Where("last_seen >= ?", startAt)
	}
	qv.Count(&vioTotal)

	qvn := config.DB.Model(&model.ComplianceResult{}).Where("task_id = ?", taskID)
	if !startAt.IsZero() {
		qvn = qvn.Where("detected_at >= ?", startAt)
	}
	qvn.Count(&vioNew)

	return gin.H{
		"totalIps": totalIPs, "aliveIps": aliveIPs,
		"appsFound": appsFound, "appsNew": appsNew,
		"violations": vioTotal, "violationsNew": vioNew,
	}
}
