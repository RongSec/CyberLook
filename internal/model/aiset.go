package model

import "time"

// AISettings AI 功能开关（单行表）
type AISettings struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	ServiceIdentify bool      `gorm:"column:service_identify;default:true" json:"serviceIdentify"`
	ScanMonitor     bool      `gorm:"column:scan_monitor;default:true" json:"scanMonitor"`
	// JudgePrompt / IdentifyPrompt 两个 AI Agent 的自定义提示词。
	// 空 = 用内置默认（见 service/aiprompt.go）。存库而不是存文件，是为了让
	// LLM 配置页能直接改、改完立即生效，不需要重启。
	JudgePrompt    string `gorm:"column:judge_prompt;type:text" json:"judgePrompt"`
	IdentifyPrompt string `gorm:"column:identify_prompt;type:text" json:"identifyPrompt"`
	// Parallel 识别并发数（同时进行的识别任务数）。默认 3。
	// 只影响识别 worker，不影响扫描；实际并发还受整机 CPU 预算闸门约束。
	Parallel  int       `gorm:"column:parallel;default:3" json:"parallel"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (AISettings) TableName() string { return "ai_settings" }

// BackupSettings 备份设置（单行表）
type BackupSettings struct {
	ID       uint      `gorm:"primaryKey" json:"id"`
	Enabled  bool      `gorm:"column:enabled;default:true" json:"enabled"`
	Hour     int       `gorm:"column:hour;default:2" json:"hour"`           // 每天几点执行（0-23）
	KeepDays int       `gorm:"column:keep_days;default:7" json:"keepDays"`  // 保留天数，超期自动删
	UpdatedAt time.Time `json:"updatedAt"`
}

func (BackupSettings) TableName() string { return "backup_settings" }
