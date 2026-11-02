package api

import (
	"net/http"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/fingerprint"
	"github.com/gin-gonic/gin"
)

// IdentifyFingerprint 对单个服务做三层指纹识别（调试/测试用）
// POST /api/fingerprint/identify  {ip, port, protocol}
func IdentifyFingerprint(c *gin.Context) {
	var req struct {
		IP       string `json:"ip"`
		Port     int    `json:"port"`
		Protocol string `json:"protocol"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IP == "" || req.Port == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需要 ip 和 port"})
		return
	}
	if req.Protocol == "" {
		req.Protocol = "HTTP"
	}

	r := fingerprint.Identify(req.Protocol, req.IP, req.Port)
	c.JSON(http.StatusOK, gin.H{
		"ip":          req.IP,
		"port":        req.Port,
		"protocol":    req.Protocol,
		"title":       r.Title,
		"metaDesc":    r.MetaDesc,
		"fingerprint": r.Fingerprint,
		"framework":   r.Framework,
		"server":      r.Server,
		"layer":       r.Layer,
		"confidence":  r.Confidence,
		"evidence":    r.Evidence,
	})
}

// BatchIdentifyFingerprint 批量识别（对 DB 里的异常 web 服务）
// POST /api/fingerprint/batch  {limit, onlyAbnormal}
func BatchIdentifyFingerprint(c *gin.Context) {
	var req struct {
		Limit        int  `json:"limit"`
		OnlyAbnormal bool `json:"onlyAbnormal"`
		Offset       int  `json:"offset"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 20
	}

	// 查询待识别服务
	q := `SELECT ip::text, port, protocol FROM services WHERE protocol ILIKE 'http%'`
	if req.OnlyAbnormal {
		q += ` AND (title = '' OR title IS NULL OR title = 'Not Found' OR title = 'Login'
		       OR title = 'Loading...' OR fingerprint LIKE '%HSTS%')`
	}
	q += ` ORDER BY ip, port LIMIT ` + strconv.Itoa(req.Limit) + ` OFFSET ` + strconv.Itoa(req.Offset)

	rows, err := config.DB.Raw(q).Rows()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	type item struct {
		IP          string `json:"ip"`
		Port        int    `json:"port"`
		Protocol    string `json:"protocol"`
		Title       string `json:"title"`
		Fingerprint string `json:"fingerprint"`
		Layer       string `json:"layer"`
		Confidence  int    `json:"confidence"`
	}
	var results []item

	for rows.Next() {
		var ip, protocol string
		var port int
		if err := rows.Scan(&ip, &port, &protocol); err != nil {
			continue
		}
		r := fingerprint.Identify(protocol, ip, port)
		results = append(results, item{
			IP: ip, Port: port, Protocol: protocol,
			Title: r.Title, Fingerprint: r.Fingerprint,
			Layer: r.Layer, Confidence: r.Confidence,
		})
	}

	c.JSON(http.StatusOK, gin.H{"count": len(results), "items": results})
}
