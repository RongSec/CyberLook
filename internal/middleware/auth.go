package middleware

import (
	"net/http"
	"strings"

	"asset-platform/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// Auth JWT 鉴权中间件
func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			// SSE 走 query token
			if qt := c.Query("token"); qt != "" && VerifyToken(qt) {
				c.Set("sseAuth", true)
				c.Next()
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
			return
		}
		tokenStr := strings.TrimPrefix(auth, "Bearer ")
		if !VerifyToken(tokenStr) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "登录已过期"})
			return
		}
		if claims, ok := parseClaims(tokenStr); ok {
			if uid, ok := claims["uid"].(float64); ok {
				c.Set("uid", uint(uid))
			}
			if uname, ok := claims["username"].(string); ok {
				c.Set("username", uname)
			}
		}
		c.Next()
	}
}

// VerifyToken 校验 JWT 是否有效
func VerifyToken(tokenStr string) bool {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		return []byte(config.JWTSecret()), nil
	})
	return err == nil && token.Valid
}

func parseClaims(tokenStr string) (jwt.MapClaims, bool) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		return []byte(config.JWTSecret()), nil
	})
	if err != nil || !token.Valid {
		return nil, false
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	return claims, ok
}
