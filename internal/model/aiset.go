package model

import "time"

// AISettings AI 功能开关（单行表）
type AISettings struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	ServiceIdentify bool      `gorm:"column:service_identify;default:true" json:"serviceIdentify"`
	ScanMonitor     bool      `gorm:"column:scan_monitor;default:true" json:"scanMonitor"`
	UpdatedAt       time.Time `json:"updatedAt"`
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
