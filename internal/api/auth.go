package api

import (
	"net"
	"net/http"

	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// Login 登录
func Login(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	// 用 RemoteAddr 直接解析：c.ClientIP() 在本环境会阻塞
	clientIP := ""
	if host, _, e := net.SplitHostPort(c.Request.RemoteAddr); e == nil {
		clientIP = host
	}
	token, err := service.Login(req.Username, req.Password, clientIP)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token})
}

// Me 当前登录用户
func Me(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"username": c.GetString("username")})
}

// ChangePassword 修改密码
func ChangePassword(c *gin.Context) {
	var req struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if err := service.ChangePassword(c.GetString("username"), req.OldPassword, req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "密码已修改"})
}
