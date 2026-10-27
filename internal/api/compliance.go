package api

import (
	"net/http"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ListRules 合规规则列表
func ListRules(c *gin.Context) {
	c.JSON(http.StatusOK, service.ListRules())
}

// UpsertRule 新增/更新规则
func UpsertRule(c *gin.Context) {
	var r model.ComplianceRule
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if err := service.UpsertRule(&r); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// DeleteRule 删除规则
func DeleteRule(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteRule(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ListWhitelist 堡垒机白名单
func ListWhitelist(c *gin.Context) {
	c.JSON(http.StatusOK, service.ListWhitelist())
}

// AddWhitelist 添加白名单
func AddWhitelist(c *gin.Context) {
	var req struct {
		IP   string `json:"ip"`
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IP == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "IP 必填"})
		return
	}
	if err := service.AddWhitelist(req.IP, req.Note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已添加"})
}

// DeleteWhitelist 删除白名单
func DeleteWhitelist(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteWhitelist(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ListComplianceResults 合规结果列表
func ListComplianceResults(c *gin.Context) {
	status := c.Query("status")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	keyword := c.Query("keyword")
	results, total := service.ListComplianceResults(status, keyword, page, size)

	// 补服务名（合规结果只存了 ip+port，展示时需要知道是什么服务）
	type row struct {
		model.ComplianceResult
		Service string `json:"service"`
	}
	out := make([]row, 0, len(results))
	for _, r := range results {
		var svc model.Service
		var name string
		if err := config.DB.Where("ip = ? AND port = ?", r.IP, r.Port).First(&svc).Error; err == nil {
			name = svc.Service
			if name == "" {
				name = svc.Protocol
			}
		}
		out = append(out, row{ComplianceResult: r, Service: name})
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "items": out})
}

// RunComplianceCheck 一键检测
func RunComplianceCheck(c *gin.Context) {
	newV, fixed, err := service.RunComplianceCheck()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"newViolation": newV, "fixed": fixed, "message": "检测完成"})
}

// DeleteComplianceResult 删除单条合规检测结果
func DeleteComplianceResult(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := config.DB.Delete(&model.ComplianceResult{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// BatchDeleteComplianceResults 批量删除合规检测结果
func BatchDeleteComplianceResults(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要删除的记录"})
		return
	}
	if err := config.DB.Delete(&model.ComplianceResult{}, req.IDs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": len(req.IDs)})
}
