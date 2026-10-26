package api

import (
	"net/http"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"github.com/gin-gonic/gin"
)

// AgentAuth 校验 X-Node-Token（按 :id 查节点）
// 过渡策略：未携带 token 时放行并记录告警，便于旧 agent 平滑升级；
// 设置 AGENT_AUTH_STRICT=1 后强制拒绝。
func AgentAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var n model.Node
		if err := config.DB.First(&n, id).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
			return
		}
		tk := c.GetHeader("X-Node-Token")
		if tk == "" {
			// 默认严格拒绝：必须携带节点 token（旧版 agent 请升级）
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "缺少节点凭据 X-Node-Token"})
			return
		}
		if n.Token == "" || n.Token != tk {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "节点凭据无效"})
			return
		}
		c.Next()
	}
}

// AgentTaskAuth 校验 X-Node-Token（:id 是 agent_task id，需反查所属节点）
func AgentTaskAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var at model.AgentTask
		if err := config.DB.First(&at, id).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "分片不存在"})
			return
		}
		var n model.Node
		if err := config.DB.First(&n, at.NodeID).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "节点不存在"})
			return
		}
		tk := c.GetHeader("X-Node-Token")
		if tk == "" {
			// 与 AgentAuth 一致：默认拒绝，不允许未认证调用
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "缺少节点凭据 X-Node-Token"})
			return
		}
		if n.Token == "" || n.Token != tk {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "节点凭据无效"})
			return
		}
		c.Next()
	}
}
