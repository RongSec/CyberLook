package api

import (
	"net/http"
	"strconv"

	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ListDepartments 部门列表
func ListDepartments(c *gin.Context) {
	c.JSON(http.StatusOK, service.ListDepartments())
}

// CreateDepartment 新增部门
func CreateDepartment(c *gin.Context) {
	var req struct {
		Name     string `json:"name"`
		ParentID *uint  `json:"parentId"`
		Owners   string `json:"owners"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "部门名必填"})
		return
	}
	if err := service.CreateDepartment(req.Name, req.ParentID, req.Owners); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已添加"})
}

// DeleteDepartment 删除部门
func DeleteDepartment(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteDepartment(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ConfirmAsset 确认待确认资产（部门/负责人必填）
func ConfirmAsset(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		SystemName string `json:"systemName"`
		Owner      string `json:"owner"`
		Department string `json:"department"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Department == "" || req.Owner == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须选择部门和负责人"})
		return
	}
	if err := service.ConfirmAsset(uint(id), req.SystemName, req.Owner, req.Department); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已确认入库"})
}
