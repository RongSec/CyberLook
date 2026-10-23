package api

import (
	"os"
	"runtime"
	"net/http"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// GetSystemSettings 读系统设置
func GetSystemSettings(c *gin.Context) {
	var s model.SystemSettings
	if err := config.DB.First(&s, 1).Error; err != nil {
		c.JSON(http.StatusOK, model.SystemSettings{})
		return
	}
	c.JSON(http.StatusOK, s)
}

// SaveSystemSettings 保存系统设置
func SaveSystemSettings(c *gin.Context) {
	var req model.SystemSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	updates := map[string]interface{}{
		"github_repo":  req.GithubRepo,
		"proxy":        req.Proxy,
		"port":         req.Port,
		"updated_at":   time.Now(),
	}
	if req.GithubToken != "" {
		updates["github_token"] = req.GithubToken
	}
	if err := config.DB.Model(&model.SystemSettings{}).Where("id = 1").Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// CheckUpdate 检查 GitHub Release
func CheckUpdate(c *gin.Context) {
	info, err := service.CheckUpdate()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, info)
}

// ApplyUpdate 应用更新（下载 + 备份 + 替换 + 重启）
func ApplyUpdate(c *gin.Context) {
	var req struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.DownloadURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少下载地址"})
		return
	}
	if err := service.ApplyUpdate(req.DownloadURL); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 异步重启（让响应先返回），由 Docker restart:always 自动拉起
	go func() {
		time.Sleep(1 * time.Second)
		os.Exit(0)
	}()
	c.JSON(http.StatusOK, gin.H{"message": "已更新，正在重启"})
}

// RollbackUpdate 回滚到最近备份
func RollbackUpdate(c *gin.Context) {
	name, err := service.Rollback()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	go func() {
		time.Sleep(1 * time.Second)
		os.Exit(0)
	}()
	c.JSON(http.StatusOK, gin.H{"message": "已回滚到 " + name + "，正在重启"})
}

// SystemStats 系统资源占用（CPU/内存/磁盘）
func SystemStats(c *gin.Context) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	c.JSON(http.StatusOK, gin.H{
		"goroutines":  runtime.NumGoroutine(),
		"heapAllocMB": m.HeapAlloc / 1024 / 1024,
		"sysMB":       m.Sys / 1024 / 1024,
		"gcCount":     m.NumGC,
	})
}
