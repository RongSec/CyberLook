package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"github.com/gin-gonic/gin"
)

// IP 白名单（从数据库读取，带 30 秒缓存）
var (
	whitelistMu   sync.RWMutex
	whitelistNets []*net.IPNet
	whitelistIPs  []net.IP
	whitelistLoad time.Time
)

// reloadWhitelist 从 DB 重新加载白名单（30 秒缓存）
func reloadWhitelist() {
	whitelistMu.Lock()
	defer whitelistMu.Unlock()
	if time.Since(whitelistLoad) < 30*time.Second && whitelistNets != nil {
		return
	}

	var s model.SystemSettings
	config.DB.First(&s)
	raw := strings.TrimSpace(s.AllowedIPs)

	var nets []*net.IPNet
	var ips []net.IP
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ipNet, err := net.ParseCIDR(part); err == nil {
			nets = append(nets, ipNet)
		} else if ip := net.ParseIP(part); ip != nil {
			ips = append(ips, ip)
		}
	}
	whitelistNets = nets
	whitelistIPs = ips
	whitelistLoad = time.Now()
}

// InvalidateWhitelist 清除缓存（保存设置后调用）
func InvalidateWhitelist() {
	whitelistMu.Lock()
	defer whitelistMu.Unlock()
	whitelistLoad = time.Time{}
}

// IPWhitelist IP 白名单中间件
//
// 配置来源：system_settings.allowed_ips（系统配置页面可改）
// 空 = 不限制
//
// 默认放行：localhost、Docker 网桥（容器访问）
func IPWhitelist() gin.HandlerFunc {
	defaultNets := []*net.IPNet{
		mustCIDR("127.0.0.0/8"),
		mustCIDR("::1/128"),
		mustCIDR("172.16.0.0/12"),
		mustCIDR("fc00::/7"),
	}

	return func(c *gin.Context) {
		reloadWhitelist()

		whitelistMu.RLock()
		nets := whitelistNets
		ips := whitelistIPs
		whitelistMu.RUnlock()

		// 未配置 = 不限制
		if len(nets) == 0 && len(ips) == 0 {
			c.Next()
			return
		}

		ipStr := clientIP(c)
		ip := net.ParseIP(ipStr)
		if ip == nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "禁止访问", "message": "无法识别来源 IP",
			})
			return
		}

		// 默认放行（内部容器）
		for _, n := range defaultNets {
			if n.Contains(ip) {
				c.Next()
				return
			}
		}

		// 用户配置的白名单
		for _, n := range nets {
			if n.Contains(ip) {
				c.Next()
				return
			}
		}
		for _, ip2 := range ips {
			if ip2.Equal(ip) {
				c.Next()
				return
			}
		}

		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": "禁止访问", "message": "IP 不在白名单内",
		})
	}
}

func clientIP(c *gin.Context) string {
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := c.GetHeader("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err == nil {
		return host
	}
	return c.Request.RemoteAddr
}

func mustCIDR(s string) *net.IPNet {
	_, n, _ := net.ParseCIDR(s)
	return n
}
