package api

import (
	"fmt"
	"net/http"
	"strings"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"github.com/gin-gonic/gin"
)

// BatchCreateSegments 批量创建 IP 网段（每行：CIDR,描述）
//
// 格式示例：
//   10.0.0.0/24,生产网段
//   10.0.1.0/24,测试网段
//   10.0.2.0/24 - 隔离区
//
// 请求：{ "input": "...", "name": "可选，批量名称" }
func BatchCreateSegments(c *gin.Context) {
	var req struct {
		Input string `json:"input"`
		Name  string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Input) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需要 input"})
		return
	}

	lines := strings.Split(req.Input, "\n")
	type result struct {
		Line    int    `json:"line"`
		CIDR    string `json:"cidr"`
		Desc    string `json:"desc"`
		OK      bool   `json:"ok"`
		Error   string `json:"error,omitempty"`
	}
	var results []result
	created := 0

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// 解析：CIDR,描述 或 CIDR - 描述
		cidr, desc := parseCIDRLine(line)
		if cidr == "" {
			results = append(results, result{Line: i + 1, OK: false, Error: "无法解析 CIDR"})
			continue
		}
		if desc == "" {
			results = append(results, result{Line: i + 1, CIDR: cidr, OK: false, Error: "缺少描述（必须填写原因）"})
			continue
		}

		// 检查重复
		var existing model.IPSegment
		if err := config.DB.Where("cidr = ?", cidr).First(&existing).Error; err == nil {
			results = append(results, result{Line: i + 1, CIDR: cidr, Desc: desc, OK: false, Error: "已存在"})
			continue
		}

		name := req.Name
		if name == "" {
			name = desc
		}
		seg := model.IPSegment{Name: name, CIDR: cidr, Description: desc}
		if err := config.DB.Create(&seg).Error; err != nil {
			results = append(results, result{Line: i + 1, CIDR: cidr, Desc: desc, OK: false, Error: err.Error()})
		} else {
			results = append(results, result{Line: i + 1, CIDR: cidr, Desc: desc, OK: true})
			created++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"total":   len(results),
		"created": created,
		"failed":  len(results) - created,
		"results": results,
	})
}

// BatchCreateExcludes 批量创建免扫描范围（每行：IP范围,原因）
//
// 格式示例：
//   10.0.0.1-10.0.0.50,服务器区不扫
//   192.168.1.0/24,测试网段
//
// 请求：{ "input": "..." }
func BatchCreateExcludes(c *gin.Context) {
	var req struct {
		Input string `json:"input"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Input) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需要 input"})
		return
	}

	lines := strings.Split(req.Input, "\n")
	type result struct {
		Line   int    `json:"line"`
		Range  string `json:"range"`
		Reason string `json:"reason"`
		OK     bool   `json:"ok"`
		Error  string `json:"error,omitempty"`
	}
	var results []result
	created := 0

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// 解析：范围,原因 或 范围 - 原因
		ipRange, reason := parseCIDRLine(line)
		if ipRange == "" {
			results = append(results, result{Line: i + 1, OK: false, Error: "无法解析 IP 范围"})
			continue
		}
		if reason == "" {
			results = append(results, result{Line: i + 1, Range: ipRange, OK: false, Error: "缺少原因（必须填写）"})
			continue
		}

		ex := model.ExcludeRange{IPRange: ipRange, Reason: reason}
		if err := config.DB.Create(&ex).Error; err != nil {
			results = append(results, result{Line: i + 1, Range: ipRange, Reason: reason, OK: false, Error: err.Error()})
		} else {
			results = append(results, result{Line: i + 1, Range: ipRange, Reason: reason, OK: true})
			created++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"total":   len(results),
		"created": created,
		"failed":  len(results) - created,
		"results": results,
	})
}

// parseCIDRLine 解析一行：CIDR,描述 或 CIDR - 描述
func parseCIDRLine(line string) (string, string) {
	// 先试逗号
	if idx := strings.Index(line, ","); idx > 0 {
		return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:])
	}
	// 再试 " - "
	if idx := strings.Index(line, " - "); idx > 0 {
		return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+3:])
	}
	// 再试 "  "（两个空格）
	if idx := strings.Index(line, "  "); idx > 0 {
		return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+2:])
	}
	// 只有 CIDR，没有描述
	return strings.TrimSpace(line), ""
}

var _ = fmt.Sprintf
