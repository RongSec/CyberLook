package api

import (
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// validSegmentCIDR 校验网段/地址写法：允许 CIDR（10.0.0.0/24）、单 IP、范围（10.0.0.1-20）、
// 通配（10.0.0.*）。之前这里完全不校验，随便写个 "not-a-cidr" 也能入库，
// 等到扫描/预览时才发现解析不出来，数据里留一堆垃圾行。
func validSegmentCIDR(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// 通配：每段是数字或 *
	if strings.Contains(s, "*") {
		parts := strings.Split(s, ".")
		if len(parts) != 4 {
			return false
		}
		for _, p := range parts {
			if p == "*" {
				continue
			}
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 || n > 255 {
				return false
			}
		}
		return true
	}
	// CIDR
	if strings.Contains(s, "/") {
		_, err := netip.ParsePrefix(s)
		return err == nil
	}
	// 范围 a-b（末段）
	if i := strings.LastIndex(s, "-"); i > 0 {
		a, b := strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
		if _, err := netip.ParseAddr(a); err != nil {
			return false
		}
		n, err := strconv.Atoi(b)
		return err == nil && n >= 0 && n <= 255
	}
	// 单 IP
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	// 域名/机房描述不放这里，但为兼容历史输入，允许「字母开头」的机房别名不拦
	return false
}

// parseSegID 解析路径里的 id；非法时直接回 400，避免 id=0 静默更新/删除 0 行却返回成功
func parseSegID(c *gin.Context, verb string) (uint, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的编号"})
		return 0, false
	}
	return uint(id), true
}

// ListSegments 网段列表
func ListSegments(c *gin.Context) {
	c.JSON(http.StatusOK, service.ListSegments())
}

// CreateSegment 新增网段
func CreateSegment(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		CIDR        string `json:"cidr"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.CIDR == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "网段必填"})
		return
	}
	if !validSegmentCIDR(req.CIDR) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "网段格式不对，示例：198.18.1.0/24 或 198.18.1.10 或 198.18.1.1-20"})
		return
	}
	if req.Name == "" {
		req.Name = "未命名"
	}
	if err := service.CreateSegment(req.Name, req.CIDR, req.Description); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已添加"})
}

// DeleteSegment 删除网段
func DeleteSegment(c *gin.Context) {
	id, ok := parseSegID(c, "删除")
	if !ok {
		return
	}
	var n int64
	config.DB.Model(&model.IPSegment{}).Where("id = ?", id).Count(&n)
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "网段不存在"})
		return
	}
	if err := service.DeleteSegment(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ListExcludes 免扫描清单列表
func ListExcludes(c *gin.Context) {
	c.JSON(http.StatusOK, service.ListExcludeRanges())
}

// CreateExclude 新增免扫描范围
func CreateExclude(c *gin.Context) {
	var req struct {
		IPRange string `json:"ipRange"`
		Reason  string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IPRange == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "IP 范围必填"})
		return
	}
	if err := service.CreateExcludeRange(req.IPRange, req.Reason); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 新增排除范围 = 这个范围不该出现在资产里：把存量数据清掉
	a, sv := service.PurgeExcludedData([]string{req.IPRange})
	c.JSON(http.StatusOK, gin.H{"message": "已添加", "purgedAssets": a, "purgedServices": sv})
}

// DeleteExclude 删除免扫描范围
func DeleteExclude(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteExcludeRange(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// UpdateSegment 编辑网段（名称可空，自动填「未命名」）
func UpdateSegment(c *gin.Context) {
	id, ok := parseSegID(c, "编辑")
	if !ok {
		return
	}
	var req struct {
		Name        string `json:"name"`
		CIDR        string `json:"cidr"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.CIDR == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "网段必填"})
		return
	}
	if !validSegmentCIDR(req.CIDR) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "网段格式不对，示例：198.18.1.0/24 或 198.18.1.10 或 198.18.1.1-20"})
		return
	}
	if req.Name == "" {
		req.Name = "未命名"
	}
	res := config.DB.Model(&model.IPSegment{}).Where("id = ?", id).Updates(map[string]interface{}{
		"name": req.Name, "cidr": req.CIDR, "description": req.Description,
	})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": res.Error.Error()})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "网段不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

// UpdateExclude 编辑免扫描范围
func UpdateExclude(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		IPRange string `json:"ipRange"`
		Reason  string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IPRange == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "IP 范围必填"})
		return
	}
	err := config.DB.Model(&model.ExcludeRange{}).Where("id = ?", id).Updates(map[string]interface{}{
		"ip_range": req.IPRange, "reason": req.Reason,
	}).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a, sv := service.PurgeExcludedData([]string{req.IPRange})
	c.JSON(http.StatusOK, gin.H{"message": "已更新", "purgedAssets": a, "purgedServices": sv})
}
