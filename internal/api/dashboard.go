package api

import (
	"net/http"

	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// Dashboard 首页仪表盘汇总
func Dashboard(c *gin.Context) {
	c.JSON(http.StatusOK, service.GetDashboardSummary())
}

// IPHistory 某 IP 的历史态势
func IPHistory(c *gin.Context) {
	ip := c.Param("ip")
	c.JSON(http.StatusOK, service.GetIPHistory(ip, 10))
}

// ServiceTrend 服务数趋势
func ServiceTrend(c *gin.Context) {
	// 支持近 7 天 / 30 天 / 365 天
	limit := 7
	switch c.DefaultQuery("range", "7d") {
	case "30d":
		limit = 30
	case "365d":
		limit = 365
	case "90d":
		limit = 90
	}
	c.JSON(http.StatusOK, service.GetServiceTrend(limit))
}
