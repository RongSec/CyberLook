package api

import (
	"path/filepath"
	"strings"
	"net/http"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ListPageSnapshots 页面快照列表
func ListPageSnapshots(c *gin.Context) {
	var items []model.PageSnapshot
	config.DB.Order("captured_at desc").Limit(200).Find(&items)
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// CaptureSnapshots 立刻对当前库内所有 web 服务截图
func CaptureSnapshots(c *gin.Context) {
	ok, fail, err := service.CaptureWebServices()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "截图完成", "success": ok, "failed": fail})
}

// GetSnapshotSettings 获取截图设置
func GetSnapshotSettings(c *gin.Context) {
	c.JSON(http.StatusOK, service.GetSnapshotSettings())
}

// SaveSnapshotSettings 保存截图设置
func SaveSnapshotSettings(c *gin.Context) {
	var s service.SnapshotSettings
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if s.IntervalDays < 1 {
		s.IntervalDays = 7
	}
	if s.RetentionDays < 1 {
		s.RetentionDays = 30
	}
	service.SetSnapshotSettings(s)
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// SnapshotImage 返回截图图片
func SnapshotImage(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var s model.PageSnapshot
	if err := config.DB.First(&s, id).Error; err != nil || s.ImagePath == "" {
		c.Status(http.StatusNotFound)
		return
	}
	// 防路径遍历：只允许访问 SnapshotDir 下的文件
	absPath := filepath.Clean(s.ImagePath)
	absSnap, _ := filepath.Abs(service.SnapshotDir)
	absFile, _ := filepath.Abs(absPath)
	if !strings.HasPrefix(absFile, absSnap) {
		c.JSON(http.StatusForbidden, gin.H{"error": "非法文件路径"})
		return
	}
	c.File(absPath)
}

// DeleteSnapshots 删除截图（支持批量 ids）
func DeleteSnapshots(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择快照"})
		return
	}
	for _, id := range req.IDs {
		var s model.PageSnapshot
		if err := config.DB.First(&s, id).Error; err == nil {
			service.DeleteSnapshotFile(s.ImagePath)
			config.DB.Delete(&model.PageSnapshot{}, id)
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除", "count": len(req.IDs)})
}

// GetSnapshotProgress 截图进度（前端轮询显示进度条）
func GetSnapshotProgress(c *gin.Context) {
	c.JSON(http.StatusOK, service.GetSnapshotProgress())
}
