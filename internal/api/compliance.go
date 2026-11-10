package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ListRules 合规规则列表
func ListRules(c *gin.Context) {
	c.JSON(http.StatusOK, service.ListRules())
}

// UpsertRule 新增/更新规则
func UpsertRule(c *gin.Context) {
	var r model.ComplianceRule
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if err := service.UpsertRule(&r); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// DeleteRule 删除规则
func DeleteRule(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteRule(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ListWhitelist 堡垒机白名单
func ListWhitelist(c *gin.Context) {
	c.JSON(http.StatusOK, service.ListWhitelist())
}

// AddWhitelist 添加白名单
func AddWhitelist(c *gin.Context) {
	var req struct {
		IP   string `json:"ip"`
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IP == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "IP 必填"})
		return
	}
	if err := service.AddWhitelist(req.IP, req.Note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已添加"})
}

// DeleteWhitelist 删除白名单
func DeleteWhitelist(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteWhitelist(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ListComplianceResults 合规结果列表
// 关联 assets（部门/负责人/系统）、compliance_rules（规则名）、services（服务名），
// 支持 SPL 检索条件（conds），字段名与前端 SearchBar 的 fields 对齐
func ListComplianceResults(c *gin.Context) {
	status := c.Query("status")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	keyword := c.Query("keyword")
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}

	q := config.DB.Table("compliance_results AS cr").
		Joins("LEFT JOIN assets a ON a.ip = cr.ip").
		Joins("LEFT JOIN compliance_rules r ON r.id = cr.rule_id").
		Joins("LEFT JOIN services s ON s.ip = cr.ip AND s.port = cr.port")
	if status != "" {
		q = q.Where("cr.status = ?", status)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where(`cr.ip::text LIKE ? OR cr.port::text LIKE ? OR r.name ILIKE ?
			OR a.system_name ILIKE ? OR a.owner ILIKE ? OR a.department ILIKE ?`,
			like, like, like, like, like, like)
	}
	if cs := c.Query("conds"); cs != "" {
		var conds []SearchCond
		if json.Unmarshal([]byte(cs), &conds) == nil {
			q = ApplySearchConds(q, conds, map[string]string{
				// ip 列是 inet，::text 会带 /32 掩码（10.24.48.1/32），精确匹配会永远失配，
				// 所以用 host() 取出纯地址
				"ip":         "host(cr.ip)",
				"port":       "cr.port",
				"status":     "cr.status",
				"ruleName":   "COALESCE(NULLIF(cr.label, ''), r.name, '')",
				"service":    "s.service",
				"systemName": "a.system_name",
				"owner":      "a.owner",
				"department": "a.department",
				"detectedAt": "cr.detected_at",
				"lastSeen":   "cr.last_seen",
			})
		}
	}

	var total int64
	q.Count(&total)

	type row struct {
		model.ComplianceResult
		Service    string `json:"service"`
		RuleName   string `json:"ruleName"`
		SystemName string `json:"systemName"`
		Owner      string `json:"owner"`
		Department string `json:"department"`
	}
	var rows = make([]row, 0)
	q.Select(`cr.*,
		COALESCE(NULLIF(cr.label, ''), r.name, '') AS rule_name,
		COALESCE(s.service, '') AS service,
		COALESCE(a.system_name, '') AS system_name,
		COALESCE(a.owner, '') AS owner,
		COALESCE(a.department, '') AS department`).
		Order("cr.id desc").Offset((page - 1) * size).Limit(size).Scan(&rows)
	c.JSON(http.StatusOK, gin.H{"total": total, "items": rows, "page": page, "size": size})
}

// RunComplianceCheck 一键检测
// 只有这个入口会写库。手动检测默认关闭白名单（ignoreWhitelist 省略即 true），
// 并且会对每个候选端口实连一次，确认仍然开着才判违规。
func RunComplianceCheck(c *gin.Context) {
	var req struct {
		IDs             []uint `json:"ids"`
		IgnoreWhitelist *bool  `json:"ignoreWhitelist"`
	}
	_ = c.ShouldBindJSON(&req)
	ignore := true
	if req.IgnoreWhitelist != nil {
		ignore = *req.IgnoreWhitelist
	}
	sum, err := service.RunComplianceCheck(req.IDs, ignore)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if sum.Checked == 0 {
		c.JSON(http.StatusOK, gin.H{"checked": 0, "message": "没有需要检测的端口（检查规则与扫描结果）"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"checked":      sum.Checked,
		"newViolation": sum.NewViolation,
		"confirmed":    sum.Confirmed,
		"reopened":     sum.Reopened,
		"fixed":        sum.Fixed,
		"uncertain":    sum.Uncertain,
		"message":      "检测完成",
	})
}

// UpdateComplianceResult 手工编辑一条检测结果：自定义标签 / 修正状态
// 标签只覆盖展示名，不动 rule_id，所以检测匹配逻辑不受影响。
func UpdateComplianceResult(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Label  *string `json:"label"`
		Status *string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	updates := map[string]interface{}{}
	if req.Label != nil {
		updates["label"] = strings.TrimSpace(*req.Label)
	}
	if req.Status != nil {
		s := *req.Status
		switch s {
		case "suspected", "violation":
			updates["status"] = s
			updates["fixed_at"] = nil
		case "fixed":
			updates["status"] = s
			updates["fixed_at"] = time.Now()
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "状态只能是 suspected / violation / fixed"})
			return
		}
	}
	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有需要更新的字段"})
		return
	}
	if err := config.DB.Model(&model.ComplianceResult{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// DeleteComplianceResult 删除单条合规检测结果
func DeleteComplianceResult(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := config.DB.Delete(&model.ComplianceResult{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// BatchDeleteComplianceResults 批量删除合规检测结果
func BatchDeleteComplianceResults(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要删除的记录"})
		return
	}
	if err := config.DB.Delete(&model.ComplianceResult{}, req.IDs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": len(req.IDs)})
}

// AIIdentifyService 手动触发单个服务的 AI 识别（用于验证识别链路）
// POST /api/services/ai-identify  {"ip":"10.24.20.195","port":18036}
func AIIdentifyService(c *gin.Context) {
	var req struct {
		IP   string `json:"ip"`
		Port int    `json:"port"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IP == "" || req.Port == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ip / port 必填"})
		return
	}
	var svc model.Service
	if err := config.DB.Where("ip = ? AND port = ?", req.IP, req.Port).First(&svc).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "服务不存在"})
		return
	}
	title, fp, stages, hops := service.IdentifyService(svc)
	// 手动触发的识别也要写回（之前只返回结果不落库，用户看到"AI 分析了但没写回"）
	if service.TitleClear(title) && title != svc.Title {
		upd := map[string]interface{}{"title": title, "title_source": "ai"}
		if svc.Title != "" {
			upd["prev_title"] = svc.Title // 供前端显示「新变更」
		}
		if fp != "" && svc.Fingerprint == "" {
			upd["fingerprint"] = fp
		}
		config.DB.Model(&model.Service{}).Where("id = ?", svc.ID).Updates(upd)
		svc.Title = title
	}
	// 手动识别也归档到会话，AI 助手页才能看到这次分析的完整链路
	service.RecordIdentConversation(svc.LastTaskID, svc, title, stages, service.TitleClear(title), hops)
	c.JSON(http.StatusOK, gin.H{
		"before":      svc.Title,
		"after":       title,
		"fingerprint": fp,
		"clear":       service.TitleClear(title),
		"stages":      stages,
		"hops":        len(hops),
	})
}
