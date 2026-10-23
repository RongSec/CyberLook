package api

import (
	"net/http"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// GetNotifySettings 读取通知配置
func GetNotifySettings(c *gin.Context) {
	var s service.NotifySettings
	if err := config.DB.First(&s, 1).Error; err != nil {
		c.JSON(http.StatusOK, service.NotifySettings{EmailPort: 465, EmailSSL: true, OnTaskDone: true, OnNewAsset: true, OnViolation: true})
		return
	}
	c.JSON(http.StatusOK, s)
}

// SaveNotifySettings 保存通知配置
func SaveNotifySettings(c *gin.Context) {
	var req service.NotifySettings
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	// 密码留空表示不修改（前端不回显）
	var old service.NotifySettings
	config.DB.First(&old, 1)

	updates := map[string]interface{}{
		"wecom_webhook":    req.WecomWebhook,
		"dingtalk_webhook": req.DingtalkWebhook,
		"feishu_webhook":   req.FeishuWebhook,
		"generic_webhook":  req.GenericWebhook,
		"email_smtp":       req.EmailSMTP,
		"email_port":       req.EmailPort,
		"email_ssl":        req.EmailSSL,
		"email_user":       req.EmailUser,
		"email_to":         req.EmailTo,
		"on_task_done":     req.OnTaskDone,
		"on_new_asset":     req.OnNewAsset,
		"on_violation":     req.OnViolation,
	}
	if req.EmailPass != "" {
		updates["email_pass"] = req.EmailPass
	} else if old.EmailPass != "" {
		updates["email_pass"] = old.EmailPass
	}
	if err := config.DB.Model(&service.NotifySettings{}).Where("id = 1").Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// TestNotify 发送测试通知
func TestNotify(c *gin.Context) {
	results := service.Broadcast(
		"灯塔 · 测试通知",
		"这是一条测试消息，收到说明通知渠道配置正确。\n时间："+time.Now().Format("2006-01-02 15:04:05"),
	)
	c.JSON(http.StatusOK, gin.H{"results": results})
}
