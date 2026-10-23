package api

import (
	"net/http"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ListSegments 网段列表
func ListSegments(c *gin.Context) {
	c.JSON(http.StatusOK, service.ListSegments())
}

// CreateSegment 新增网段
func CreateSegment(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		CIDR        string `json:"cidr"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.CIDR == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "网段必填"})
		return
	}
	if req.Name == "" {
		req.Name = "未命名"
	}
	if err := service.CreateSegment(req.Name, req.CIDR, req.Description); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已添加"})
}

// DeleteSegment 删除网段
func DeleteSegment(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteSegment(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ListExcludes 免扫描清单列表
func ListExcludes(c *gin.Context) {
	c.JSON(http.StatusOK, service.ListExcludeRanges())
}

// CreateExclude 新增免扫描范围
func CreateExclude(c *gin.Context) {
	var req struct {
		IPRange string `json:"ipRange"`
		Reason  string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IPRange == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "IP 范围必填"})
		return
	}
	if err := service.CreateExcludeRange(req.IPRange, req.Reason); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已添加"})
}

// DeleteExclude 删除免扫描范围
func DeleteExclude(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteExcludeRange(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// UpdateSegment 编辑网段（名称可空，自动填「未命名」）
func UpdateSegment(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Name        string `json:"name"`
		CIDR        string `json:"cidr"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.CIDR == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "网段必填"})
		return
	}
	if req.Name == "" {
		req.Name = "未命名"
	}
	err := config.DB.Model(&model.IPSegment{}).Where("id = ?", id).Updates(map[string]interface{}{
		"name": req.Name, "cidr": req.CIDR, "description": req.Description,
	}).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

// UpdateExclude 编辑免扫描范围
func UpdateExclude(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		IPRange string `json:"ipRange"`
		Reason  string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IPRange == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "IP 范围必填"})
		return
	}
	err := config.DB.Model(&model.ExcludeRange{}).Where("id = ?", id).Updates(map[string]interface{}{
		"ip_range": req.IPRange, "reason": req.Reason,
	}).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}
