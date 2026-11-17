package api

import (
	"net/http"
	"strings"

	"asset-platform/internal/target"

	"github.com/gin-gonic/gin"
)

// PreviewTargets 解析预览：把输入的段展开成 IP，并标出被免扫描排除的部分
func PreviewTargets(c *gin.Context) {
	var req struct {
		Targets  []string `json:"targets"`
		Excludes []string `json:"excludes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	// 免扫描匹配器：与扫描时用的是同一套（含 *.*.*.1 这类通配），
	// 否则会出现"预览说有效、实际扫描被排除"的不一致。
	ex := target.NewExcluder(req.Excludes)

	type segView struct {
		Input       string   `json:"input"`
		Count       int      `json:"count"`
		Failed      bool     `json:"failed"`
		First       string   `json:"first"`
		Last        string   `json:"last"`
		IPs         []string `json:"ips"`         // 有效 IP（每段最多 120 个，供预览）
		ExcludedIPs []string `json:"excludedIps"` // 该段被排除的 IP（全量，通常很少）
	}

	seen := map[string]bool{}
	total, kept, excludedCount := 0, 0, 0
	items := make([]segView, 0, len(req.Targets))

	for _, raw := range req.Targets {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		ips := target.Expand(t)
		// 判断是否没展开（原样返回且带 / 或 -）
		failed := len(ips) == 1 && ips[0] == t &&
			(strings.Contains(t, "/") || strings.Contains(t, "-"))

		sv := segView{Input: t, Count: len(ips), Failed: failed}
		if len(ips) > 0 {
			sv.First = ips[0]
			sv.Last = ips[len(ips)-1]
		}
		sv.IPs = []string{}
		sv.ExcludedIPs = []string{}

		for _, ip := range ips {
			if seen[ip] {
				continue
			}
			seen[ip] = true
			total++
			if ex.Contains(ip) {
				excludedCount++
				sv.ExcludedIPs = append(sv.ExcludedIPs, ip)
			} else {
				kept++
				if len(sv.IPs) < 120 {
					sv.IPs = append(sv.IPs, ip)
				}
			}
		}
		items = append(items, sv)
	}

	c.JSON(http.StatusOK, gin.H{
		"total":    total,
		"kept":     kept,
		"excluded": excludedCount,
		"items":    items,
	})
}
