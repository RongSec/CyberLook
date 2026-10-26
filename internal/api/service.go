package api

import (
	"net/http"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"github.com/gin-gonic/gin"
)

// ListServices 应用服务列表
func ListServices(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 5000 {
		size = 20
	}
	keyword := c.Query("keyword")
	service := c.Query("service")
	ip := c.Query("ip")
	port := c.Query("port")
	protocol := c.Query("protocol")

	q := config.DB.Model(&model.Service{})
	// 只显示 web 服务（严格：协议为 http/https）
	if c.Query("webOnly") == "true" {
		// 只算真正能访问的 web 服务（有响应码或有标题）
		q = q.Where("protocol ILIKE 'http%'").
			Where("COALESCE(status_code,'') <> '' OR COALESCE(title,'') <> ''")
	}
	// 只显示已完成处置（已登记资产）的 web 服务
	if c.Query("confirmedOnly") == "true" {
		// 与首页「Web 资产」口径统一：按服务自身的 confirmed 标记
		q = q.Where("confirmed = true")
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("ip::text LIKE ? OR banner ILIKE ?", like, like)
	}
	if ip != "" {
		q = q.Where("ip::text LIKE ?", "%"+ip+"%")
	}
	if port != "" {
		q = q.Where("port = ?", port)
	}
	if protocol != "" {
		q = q.Where("protocol ILIKE ?", "%"+protocol+"%")
	}
	if service != "" {
		q = q.Where("service = ?", service)
	}
	var total int64
	q.Count(&total)
	var items []model.Service
	q.Order("ip, port").Offset((page - 1) * size).Limit(size).Find(&items)
	c.JSON(http.StatusOK, gin.H{"total": total, "items": items})
}

// DeleteService 删除应用服务
func DeleteService(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := config.DB.Delete(&model.Service{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// BatchDeleteServices 批量删除应用服务
func BatchDeleteServices(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要删除的记录"})
		return
	}
	if err := config.DB.Delete(&model.Service{}, req.IDs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": len(req.IDs)})
}
