package api

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/scanner"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterNode 节点注册（agent 一条命令自动加入）
func RegisterNode(c *gin.Context) {
	var req struct {
		Name    string `json:"name"`
		IP      string `json:"ip"`
		Token   string `json:"token"`
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name 必填"})
		return
	}
	// 一次性加入令牌校验
	// default-token 仅允许内网来源（容器节点用），且可用环境变量彻底关闭
	if req.Token == "default-token" {
		if os.Getenv("ALLOW_DEFAULT_TOKEN") == "0" {
			c.JSON(http.StatusForbidden, gin.H{"error": "default-token 已禁用，请使用一次性加入令牌"})
			return
		}
		if !isPrivateIP(c.ClientIP()) {
			log.Printf("[安全警告] 公网地址尝试使用 default-token 注册: %s", c.ClientIP())
			c.JSON(http.StatusForbidden, gin.H{"error": "default-token 仅限内网使用"})
			return
		}
	} else if !ConsumeJoinToken(req.Token) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "加入令牌无效、已过期或已被使用"})
		return
	}
	node, nodeToken, err := service.RegisterNode(req.Name, req.IP, req.Token, req.Version)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// token 只在注册响应里下发一次，agent 需持久化并随每个请求带上
	c.JSON(http.StatusOK, gin.H{
		"id": node.ID, "name": node.Name, "ip": node.IP,
		"status": node.Status, "token": nodeToken,
	})
}

// Heartbeat 节点心跳
func Heartbeat(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Version  string   `json:"version"`
		Projects []string `json:"projects"`
	}
	_ = c.ShouldBindJSON(&req)
	if err := service.Heartbeat(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 记录上报的版本 + config.db 的 project 列表
	if req.Version != "" {
		config.DB.Model(&model.Node{}).Where("id = ?", id).Update("agent_version", req.Version)
	}
	if req.Projects != nil {
		pj, _ := json.Marshal(req.Projects)
		config.DB.Model(&model.Node{}).Where("id = ?", id).Update("projects", string(pj))
	}
	// 被标记升级就下发指令；是否真正替换由 agent 比对 myVersion 决定，
	// 这样即使节点上报的版本号有误（曾被降级过），也能强制拉回最新二进制
	var node model.Node
	needUpgrade := false
	if err := config.DB.First(&node, id).Error; err == nil {
		latest := config.ComputeAgentVersion()
		// 自动比对版本：上报版本与服务端内置版本不一致就自动下发升级
		if req.Version != "" && req.Version != latest {
			needUpgrade = true
		}
		// 手动标记升级也触发
		if node.UpgradeReq {
			needUpgrade = true
			config.DB.Model(&node).Update("upgrade_requested", false)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"message":       "ok",
		"upgrade":       needUpgrade,
		"latestVersion": config.ComputeAgentVersion(),
	})
}

// UpgradeNode 标记节点升级（下次心跳时 agent 自更新）
func UpgradeNode(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var node model.Node
	if err := config.DB.First(&node, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
		return
	}
	if node.Status != "online" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "节点离线，无法更新"})
		return
	}
	config.DB.Model(&node).Update("upgrade_requested", true)
	c.JSON(http.StatusOK, gin.H{
		"message":       "已下发升级指令，节点将在下次心跳时自动更新",
		"latestVersion": config.ComputeAgentVersion(),
	})
}

// AgentVersionInfo 当前服务端内置的控制文件版本
func AgentVersionInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"latestVersion": config.ComputeAgentVersion()})
}

// ListNodes 节点列表
func ListNodes(c *gin.Context) {
	c.JSON(http.StatusOK, service.ListNodes())
}

// DeleteNode 删除节点
func DeleteNode(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteNode(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// GetNodeTasks agent 拉取分配给自己的待执行任务
func GetNodeTasks(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	c.JSON(http.StatusOK, service.GetNodeTasks(uint(id)))
}

// ReportNodeResults agent 上报扫描结果
func ReportNodeResults(c *gin.Context) {
	var req struct {
		TaskID  uint                 `json:"taskId"`
		Results []scanner.ScanResult `json:"results"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if err := service.ReportNodeResults(req.TaskID, req.Results); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已入库"})
}

// isPrivateIP 判断来源是否为内网地址（default-token 只允许内网使用）
func isPrivateIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// ListNodeProjects 列出节点 config.db 的 project（从心跳上报的缓存读）
func ListNodeProjects(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var n model.Node
	if err := config.DB.First(&n, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
		return
	}
	var projects []string
	if n.Projects != "" {
		_ = json.Unmarshal([]byte(n.Projects), &projects)
	}
	if projects == nil {
		projects = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"projects": projects})
}

// DeleteNodeProjects 下发清理指令（agent 收到后删 config.db 的 project）
func DeleteNodeProjects(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Projects []string `json:"projects"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Projects) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要删除的 project"})
		return
	}
	var n model.Node
	if err := config.DB.First(&n, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
		return
	}
	// 用 agent_tasks 下发清理指令（message 带类型标记）
	pj, _ := json.Marshal(req.Projects)
	at := model.AgentTask{
		TaskID:   0, // 0 = 非扫描任务
		NodeID:   n.ID,
		NodeName: n.Name,
		IPRanges: "",
		Message:  "cleanup:" + string(pj),
	}
	config.DB.Create(&at)
	// 唤醒节点
	service.WakeAgent(n.ID)
	c.JSON(http.StatusOK, gin.H{"message": "已下发清理指令", "projects": req.Projects})
}
