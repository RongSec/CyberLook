package api

import (
	"sync"
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
		c.JSON(http.StatusOK, model.SystemSettings{GithubRepo: service.DefaultRepo})
		return
	}
	// 没配置过就回显默认仓库，前端不需要再猜
	if s.GithubRepo == "" {
		s.GithubRepo = service.DefaultRepo
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
		"allowed_ips":  req.AllowedIPs,
		"updated_at":   time.Now(),
	}
	if req.GithubToken != "" {
		updates["github_token"] = req.GithubToken
	}
	if err := config.DB.Model(&model.SystemSettings{}).Where("id = 1").Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	InvalidateWhitelist()
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

var updateMu sync.Mutex

// ApplyUpdate 应用更新：交给 updater 边车执行（流式返回日志）
func ApplyUpdate(c *gin.Context) {
	// 防止并发更新
	if !updateMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "已有更新正在进行，请稍后再试"})
		return
	}
	defer updateMu.Unlock()
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
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "更新服务不可达: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	// 流式透传 updater 的日志（每行立即 flush，前端实时可见）
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("X-Stream", "true")
	c.Status(resp.StatusCode)
	buf := make([]byte, 256)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			c.Writer.Write(buf[:n])
			c.Writer.Flush()
		}
		if err != nil {
			break
		}
	}
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

// SystemStats 系统资源占用（CPU/内存/运行中任务 + Go 运行时指标）
// 前 5 个字段供页脚展示（service.GetSystemStats 用 gopsutil 采 CPU/内存）；
// 后 4 个 Go 运行时字段供运维页使用，保留不删，避免老调用方拿不到字段。
func SystemStats(c *gin.Context) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	sys := service.GetSystemStats()
	c.JSON(http.StatusOK, gin.H{
		"cpuPercent":   sys.CPUPercent,
		"memUsedMB":    sys.MemUsedMB,
		"memTotalMB":   sys.MemTotalMB,
		"memPercent":   sys.MemPercent,
		"runningTasks": sys.RunningTasks,
		"goroutines":   runtime.NumGoroutine(),
		"heapAllocMB":  m.HeapAlloc / 1024 / 1024,
		"sysMB":        m.Sys / 1024 / 1024,
		"gcCount":      m.NumGC,
	})
}

// ===== 缓存清理 =====
// 随任务产生的中间数据（AI 会话、扫描快照、队列残留、日志、用量）会持续增长，
// 这里只做「看清占用 + 按需清理」，不做自动删，避免误伤历史。

var cacheCleanMu sync.Mutex

// CacheStats 各类缓存占用概览
func CacheStats(c *gin.Context) {
	c.JSON(http.StatusOK, service.CacheStat())
}

// CleanCache 清理指定类别
func CleanCache(c *gin.Context) {
	var req struct {
		Keys          []string `json:"keys"`
		RetentionDays int      `json:"retentionDays"`
		Compact       bool     `json:"compact"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Keys) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要清理的类别"})
		return
	}
	if req.RetentionDays < 0 || req.RetentionDays > 3650 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "保留天数不合法"})
		return
	}
	// VACUUM FULL 会短暂锁表，串行执行避免互相等锁
	if !cacheCleanMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "已有清理任务在执行，请稍候"})
		return
	}
	defer cacheCleanMu.Unlock()

	res := service.CleanCaches(req.Keys, req.RetentionDays, req.Compact)
	c.JSON(http.StatusOK, res)
}
