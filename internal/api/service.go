package api

import (
	"encoding/json"
	"strings"
	"net/http"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"github.com/gin-gonic/gin"
)

// ListServices 应用服务列表
func ListServices(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 5000 {
		size = 20
	}
	keyword := c.Query("keyword")
	service := c.Query("service")
	ip := c.Query("ip")
	port := c.Query("port")
	protocol := c.Query("protocol")

	q := config.DB.Model(&model.Service{})

	// SPL 检索条件（SearchBar 生成）
	if cs := c.Query("conds"); cs != "" {
		var conds []SearchCond
		if json.Unmarshal([]byte(cs), &conds) == nil {
			rest := make([]SearchCond, 0, len(conds))
			for _, c := range conds {
				vals := c.values()
				if len(vals) == 0 {
					continue
				}
				v := vals[0]
				// 部门/负责人来自 assets 表，services 无此列 → 用 EXISTS 关联
				switch c.Field {
				case "department":
					q = q.Where("EXISTS (SELECT 1 FROM assets a WHERE a.ip = services.ip AND a.department::text ILIKE ?)", "%"+v+"%")
					continue
				case "owner":
					q = q.Where("EXISTS (SELECT 1 FROM assets a WHERE a.ip = services.ip AND a.owner::text ILIKE ?)", "%"+v+"%")
					continue
				}
				rest = append(rest, c)
			}
			q = ApplySearchConds(q, rest, map[string]string{
				"ip": "ip", "port": "port", "service": "service", "protocol": "protocol",
				"title": "title", "statusCode": "status_code", "banner": "banner",
				"contentLength": "content_length", "finger": "fingerprint",
				"lastSeen": "last_seen", "firstSeen": "first_seen",
			})
		}
	}
	// 只显示 web 服务（严格：协议为 http/https）
	if c.Query("webOnly") == "true" {
		// 只算真正能访问的 web 服务（有响应码或有标题）
		q = q.Where("protocol ILIKE 'http%'").
			Where("COALESCE(status_code,'') <> '' OR COALESCE(title,'') <> ''")
	}
	// 显式筛选：待处置
	if c.Query("onlyPending") == "true" {
		q = q.Where("confirmed = false")
	}
	// 只显示已完成处置（已登记资产）的 web 服务
	if c.Query("confirmedOnly") == "true" {
		// 与首页「Web 资产」口径统一：按服务自身的 confirmed 标记
		q = q.Where("confirmed = true")
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		// 覆盖所有可见列：IP / 端口 / 服务 / 协议 / 标题 / 响应码 / Banner
		q = q.Where(`ip::text LIKE ? OR port::text LIKE ? OR service ILIKE ? OR protocol ILIKE ?
			OR title ILIKE ? OR status_code::text LIKE ? OR banner ILIKE ?`,
			like, like, like, like, like, like, like)
	}
	// 响应码多选（逗号分隔，如 200,301,403）
	if sc := c.Query("statusCodes"); sc != "" {
		codes := strings.Split(sc, ",")
		q = q.Where("status_code::text IN ?", codes)
	}
	if ip != "" {
		q = q.Where("ip::text LIKE ?", "%"+ip+"%")
	}
	if port != "" {
		q = q.Where("port = ?", port)
	}
	if protocol != "" {
		q = q.Where("protocol ILIKE ?", "%"+protocol+"%")
	}
	if service != "" {
		q = q.Where("service = ?", service)
	}
	var total int64
	q.Count(&total)
	var items []model.Service
	q.Order("ip, port").Offset((page - 1) * size).Limit(size).Find(&items)

	// 附带所属部门/负责人/系统名（来自 assets）
	type svcWithOwner struct {
		model.Service
		Department string `json:"department"`
		Owner      string `json:"owner"`
		SystemName string `json:"systemName"`
	}
	ids := make([]uint, 0, len(items))
	for _, it := range items {
		if it.AssetID != nil {
			ids = append(ids, *it.AssetID)
		}
	}
	am := map[uint]model.Asset{}
	if len(ids) > 0 {
		var as []model.Asset
		config.DB.Where("id IN ?", ids).Find(&as)
		for _, a := range as {
			am[a.ID] = a
		}
	}
	out := make([]svcWithOwner, len(items))
	for i, it := range items {
		d := svcWithOwner{Service: it}
		if it.AssetID != nil {
			if a, ok := am[*it.AssetID]; ok {
				d.Department = a.Department
				d.Owner = a.Owner
				d.SystemName = a.SystemName
			}
		}
		out[i] = d
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "items": out})
}

// DeleteService 删除应用服务
func DeleteService(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := config.DB.Delete(&model.Service{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// BatchDeleteServices 批量删除应用服务
func BatchDeleteServices(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要删除的记录"})
		return
	}
	if err := config.DB.Delete(&model.Service{}, req.IDs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": len(req.IDs)})
}
