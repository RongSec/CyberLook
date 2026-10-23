package api

import (
	"net/http"
	"strconv"

	"asset-platform/internal/model"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ListTemplates 模板列表
func ListTemplates(c *gin.Context) {
	c.JSON(http.StatusOK, service.ListTemplates())
}

// UpsertTemplate 新增/更新模板
func UpsertTemplate(c *gin.Context) {
	var t model.ScanTemplate
	if err := c.ShouldBindJSON(&t); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if t.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "模板名必填"})
		return
	}
	if err := service.UpsertTemplate(&t); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// DeleteTemplate 删除模板
func DeleteTemplate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteTemplate(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}
