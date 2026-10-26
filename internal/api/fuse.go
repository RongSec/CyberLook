package api

import (
	"net/http"
	"time"

	"asset-platform/internal/config"

	"github.com/gin-gonic/gin"
)

// FuseConfig 熔断规则配置（存在 system_settings，单行记录）
type FuseConfig struct {
	ID                    int       `gorm:"primaryKey;column:id" json:"-"`
	FuseFailThreshold     int       `gorm:"column:fuse_fail_threshold;default:5" json:"fuseFailThreshold"`
	FuseDurationMin       int       `gorm:"column:fuse_duration_min;default:5" json:"fuseDurationMin"`
	NodeHeartbeatTimeoutSec int     `gorm:"column:node_heartbeat_timeout_sec;default:90" json:"nodeHeartbeatTimeoutSec"`
	UpdatedAt             time.Time `gorm:"column:updated_at" json:"-"`
}

func (FuseConfig) TableName() string { return "system_settings" }

// GetFuseConfig 读取熔断规则
func GetFuseConfig(c *gin.Context) {
	var f FuseConfig
	if err := config.DB.First(&f, 1).Error; err != nil {
		// 表里可能还没有记录，给默认值
		f = FuseConfig{ID: 1, FuseFailThreshold: 5, FuseDurationMin: 5}
	}
	if f.FuseFailThreshold <= 0 {
		f.FuseFailThreshold = 5
	}
	if f.FuseDurationMin <= 0 {
		f.FuseDurationMin = 5
	}
	if f.NodeHeartbeatTimeoutSec <= 0 {
		f.NodeHeartbeatTimeoutSec = 90
	}
	c.JSON(http.StatusOK, gin.H{
		"fuseFailThreshold":       f.FuseFailThreshold,
		"fuseDurationMin":         f.FuseDurationMin,
		"nodeHeartbeatTimeoutSec": f.NodeHeartbeatTimeoutSec,
	})
}

// SaveFuseConfig 保存熔断规则
func SaveFuseConfig(c *gin.Context) {
	var req struct {
		FuseFailThreshold       int `json:"fuseFailThreshold"`
		FuseDurationMin         int `json:"fuseDurationMin"`
		NodeHeartbeatTimeoutSec int `json:"nodeHeartbeatTimeoutSec"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.FuseFailThreshold < 1 || req.FuseFailThreshold > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "失败阈值需在 1-100 之间"})
		return
	}
	if req.FuseDurationMin < 1 || req.FuseDurationMin > 1440 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "熔断时长需在 1-1440 分钟之间"})
		return
	}

	if req.NodeHeartbeatTimeoutSec < 10 || req.NodeHeartbeatTimeoutSec > 3600 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "心跳超时需在 10-3600 秒之间"})
		return
	}
	config.DB.Exec(`INSERT INTO system_settings (id, fuse_fail_threshold, fuse_duration_min, node_heartbeat_timeout_sec)
		VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET fuse_fail_threshold = EXCLUDED.fuse_fail_threshold,
		fuse_duration_min = EXCLUDED.fuse_duration_min,
		node_heartbeat_timeout_sec = EXCLUDED.node_heartbeat_timeout_sec,
		updated_at = now()`,
		req.FuseFailThreshold, req.FuseDurationMin, req.NodeHeartbeatTimeoutSec)

	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}
