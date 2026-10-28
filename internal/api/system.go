package api

import (
	"encoding/json"
	"bytes"
	"os"
	"runtime"
	"net/http"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"
	"asset-platform/internal/version"

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

// ApplyUpdate 应用更新：交给 updater 边车执行（pull 镜像 + 重启 + 健康检查 + 回滚）
func ApplyUpdate(c *gin.Context) {
	var req struct {
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Version == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少目标版本号"})
		return
	}

	updaterURL := os.Getenv("UPDATER_URL")
	if updaterURL == "" {
		updaterURL = "http://updater:9999"
	}
	body, _ := json.Marshal(map[string]string{
		"version":    req.Version,
		"oldVersion": version.Version,
	})
	httpReq, err := http.NewRequest("POST", updaterURL+"/update", bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "更新服务不可达: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	var out struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
		Output  string `json:"output"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 || !out.OK {
		c.JSON(http.StatusInternalServerError, gin.H{"error": out.Message, "log": out.Output})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": out.Message, "log": out.Output})
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
// SystemVersion 当前系统版本信息
func SystemVersion(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"version":   version.Version,
		"buildTime": version.BuildTime,
		"commit":    version.Commit,
	})
}

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
