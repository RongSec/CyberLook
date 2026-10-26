package service

import (
	"log"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// StartTaskRun 任务开始时插入一条执行记录
func StartTaskRun(taskID uint) uint {
	now := time.Now()
	run := model.TaskRun{TaskID: taskID, Status: "running", StartedAt: &now}
	config.DB.Create(&run)
	return run.ID
}

// FinishTaskRun 任务结束时回填本次执行的概览数据
func FinishTaskRun(taskID uint, status string) {
	var run model.TaskRun
	if err := config.DB.Where("task_id = ? AND status = 'running'", taskID).
		Order("started_at DESC").First(&run).Error; err != nil {
		return
	}
	var t model.ScanTask
	_ = config.DB.First(&t, taskID).Error

	var appsFound, appsNew, vioTotal, vioNew int64
	config.DB.Model(&model.Service{}).Where("last_task_id = ?", taskID).Count(&appsFound)
	config.DB.Model(&model.Service{}).Where("task_id = ?", taskID).Count(&appsNew)
	config.DB.Model(&model.ComplianceResult{}).Where("last_task_id = ?", taskID).Count(&vioTotal)
	config.DB.Model(&model.ComplianceResult{}).Where("task_id = ?", taskID).Count(&vioNew)

	now := time.Now()
	dur := 0
	if run.StartedAt != nil {
		dur = int(now.Sub(*run.StartedAt).Seconds())
	}
	config.DB.Model(&run).Updates(map[string]interface{}{
		"status": status, "finished_at": now, "duration_sec": dur,
		"total_ips": t.TotalIPs, "alive_ips": t.AliveCount,
		"apps_found": appsFound, "apps_new": appsNew,
		"violations": vioTotal, "violations_new": vioNew,
	})
}

// CleanupStaleRuns 服务启动时把上一轮残留的 running 执行记录收尾，
// 否则崩溃后这些记录会永远显示「执行中」
func CleanupStaleRuns() {
	res := config.DB.Model(&model.TaskRun{}).
		Where("status = ?", "running").
		Updates(map[string]interface{}{
			"status":      "interrupted",
			"finished_at": time.Now(),
		})
	if res.RowsAffected > 0 {
		log.Printf("[taskruns] 启动清理：将 %d 条残留 running 记录标记为 interrupted", res.RowsAffected)
	}
}
