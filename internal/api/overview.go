package api

import (
	"net/http"
	"strconv"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"

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

	// 队列概览（租约队列任务专属）：pending/进行中/完成/隔离 + 探针真值进度
	var queue gin.H
	if t.QueueMode {
		qs := service.QueueStatsOf(uint(id))
		percent, eta := -1, int64(0)
		pps, conf, phase := 0.0, "low", qs.Phase
		if t.Status == "running" || t.Status == "paused" {
			snap := service.Progress(uint(id))
			percent, eta, pps = snap.Percent, snap.ETASeconds, snap.PPS
			conf, phase = string(snap.Confidence), snap.Phase
		} else if t.Status == "done" {
			percent, conf, phase = 100, "high", "done"
		}
		queue = gin.H{
			"pending": qs.Pending, "leased": qs.Leased, "done": qs.Done, "failed": qs.FailedUnit,
			"probesDone": qs.ProbesDone, "probesTotal": qs.ProbesTotal,
			"total": qs.Total, "phase": phase,
			"percent": percent, "etaSeconds": eta, "pps": pps, "etaConfidence": conf,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"taskId":        t.ID,
		"totalIps":      t.TotalIPs,
		"aliveIps":      t.AliveCount,
		"appsFound":     appsFound,
		"appsNew":       appsNew,
		"violations":    vioTotal,
		"violationsNew": vioNew,
		"queueMode":     t.QueueMode,
		"queue":         queue,
		"error":         t.Error,
	})
}

// ListScheduledTasks 定时任务列表（cron 非空的都算）
func ListScheduledTasks(c *gin.Context) {
	var tasks []model.ScanTask
	config.DB.Where("cron IS NOT NULL AND cron <> ''").Order("id DESC").Find(&tasks)

	type item struct {
		model.ScanTask
		LastRun  *model.TaskRun `json:"lastRun"`
		RunCount int64          `json:"runCount"`
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

	// 存活数就用探测阶段的命中数（alive_count），这才是「存活探测」的语义。
	// 不要改成 count(distinct ip)：那是「有服务发现的 IP 数」，会把存活但没开放端口的主机漏掉
	// （实测 alive_count=1349 vs 有服务的 IP=1271）。另外注意别把「服务条数」当 IP 数——
	// 一个 IP 平均有 11 个端口，服务条数是 IP 数的十倍多。
	if aliveIPs <= 0 {
		var aliveDedup int64
		config.DB.Model(&model.Service{}).Where("last_task_id = ?", taskID).
			Distinct("ip").Count(&aliveDedup)
		aliveIPs = int(aliveDedup)
	}

	return gin.H{
		"totalIps": totalIPs, "aliveIps": aliveIPs,
		"appsFound": appsFound, "appsNew": appsNew,
		"violations": vioTotal, "violationsNew": vioNew,
	}
}
