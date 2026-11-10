package service

import (
	"log"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/tasklog"
)

// TaskLogEntry 任务日志（持久化）
type TaskLogEntry struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	TaskID    uint      `gorm:"column:task_id;index" json:"taskId"`
	Line      string    `gorm:"column:line" json:"line"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
}

func (TaskLogEntry) TableName() string { return "task_logs" }

const taskLogKeepLines = 5000

// InitTaskLogStore 把任务日志接到数据库（服务启动时调用一次）。
// 之前 tasklog 只放内存，容器一重启历史日志全丢，任务详情页空白。
func InitTaskLogStore() {
	tasklog.SetStore(writeTaskLogs, loadTaskLogs)
}

func writeTaskLogs(entries []tasklog.Entry) {
	if config.DB == nil || len(entries) == 0 {
		return
	}
	rows := make([]TaskLogEntry, 0, len(entries))
	now := time.Now()
	for _, e := range entries {
		rows = append(rows, TaskLogEntry{TaskID: e.TaskID, Line: e.Line, CreatedAt: now})
	}
	if err := config.DB.CreateInBatches(rows, 200).Error; err != nil {
		log.Printf("[tasklog] 落盘失败: %v", err)
	}
}

func loadTaskLogs(taskID uint) []string {
	if config.DB == nil {
		return nil
	}
	// 倒序取最近 N 行，再反转成正序
	var rows []TaskLogEntry
	config.DB.Where("task_id = ?", taskID).Order("id desc").Limit(taskLogKeepLines).Find(&rows)
	if len(rows) == 0 {
		return nil
	}
	lines := make([]string, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		lines = append(lines, rows[i].Line)
	}
	return lines
}

// CleanOldTaskLogs 清理 N 天前的任务日志（防止表无限增长）
func CleanOldTaskLogs(days int) int64 {
	if config.DB == nil || days <= 0 {
		return 0
	}
	return config.DB.Where("created_at < ?", time.Now().AddDate(0, 0, -days)).
		Delete(&TaskLogEntry{}).RowsAffected
}

// DeleteTaskLogs 删除任务时一并清掉它的日志
func DeleteTaskLogs(taskID uint) {
	if config.DB == nil {
		return
	}
	config.DB.Where("task_id = ?", taskID).Delete(&TaskLogEntry{})
	tasklog.Clear(taskID)
}
