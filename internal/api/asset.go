package api

import (
	"strings"
	"fmt"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ListAssets 资产列表（分页 + 关键字 + 状态/来源筛选）
func ListAssets(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 5000 {
		size = 20
	}
	keyword := c.Query("keyword")
	status := c.Query("status")
	source := c.Query("source")
	ip := c.Query("ip")
	systemName := c.Query("systemName")
	owner := c.Query("owner")
	department := c.Query("department")
	hasWeb := c.Query("hasWeb")

	q := config.DB.Model(&model.Asset{})
	if hasWeb == "true" {
		q = q.Where("EXISTS (SELECT 1 FROM services s WHERE s.ip = assets.ip AND (s.protocol ILIKE '%http%' OR s.service ILIKE '%http%' OR s.title <> ''))")
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("ip::text LIKE ? OR system_name ILIKE ? OR owner ILIKE ? OR department ILIKE ?", like, like, like, like)
	}
	if ip != "" {
		q = q.Where("ip::text LIKE ?", "%"+ip+"%")
	}
	if systemName != "" {
		q = q.Where("system_name ILIKE ?", "%"+systemName+"%")
	}
	if owner != "" {
		q = q.Where("owner ILIKE ?", "%"+owner+"%")
	}
	if department != "" {
		q = q.Where("department = ?", department)
	}
	// 只在显式传入 status 时过滤；未传则返回全部，不做任何隐式筛选
	if status != "" {
		q = q.Where("status = ?", status)
	}

	// SPL 检索条件（前端 SearchBar 生成，JSON 数组）
	if cs := c.Query("conds"); cs != "" {
		var conds []SearchCond
		if json.Unmarshal([]byte(cs), &conds) == nil {
			rest := make([]SearchCond, 0, len(conds))
			for _, c := range conds {
				// hasWeb 是计算列（是否存在 web 服务），需转 EXISTS
				if c.Field == "hasWeb" {
					v := strings.ToLower(c.Str())
					yes := v == "true" || v == "1" || v == "是" || v == "有"
					ex := "EXISTS (SELECT 1 FROM services s WHERE s.ip = assets.ip AND (s.protocol ILIKE 'http%' OR COALESCE(s.title,'') <> ''))"
					if !yes {
						ex = "NOT " + ex
					}
					q = q.Where(ex)
					continue
				}
				rest = append(rest, c)
			}
			q = ApplySearchConds(q, rest, map[string]string{
				"ip": "ip", "port": "port", "owner": "owner", "department": "department",
				"systemName": "system_name", "status": "status", "source": "source",
			})
		}
	}
	if source != "" {
		q = q.Where("source = ?", source)
	}

	var total int64
	q.Count(&total)

	var assets []model.Asset
	q.Order("ip").Offset((page - 1) * size).Limit(size).Find(&assets)

	// 附带「是否有 web 服务」
	type assetWithWeb struct {
		model.Asset
		HasWeb bool `json:"hasWeb"`
	}
	items := make([]assetWithWeb, len(assets))
	for i, a := range assets {
		var cnt int64
		config.DB.Model(&model.Service{}).
			Where("ip = ? AND (protocol ILIKE '%http%' OR service ILIKE '%http%' OR title <> '' OR status_code <> '')", a.IP).
			Count(&cnt)
		items[i] = assetWithWeb{Asset: a, HasWeb: cnt > 0}
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "page": page, "size": size, "items": items})
}

// AssetPorts 某 IP 的端口列表（用于资产总览点击 IP 弹窗）
func AssetPorts(c *gin.Context) {
	ip := c.Param("ip")
	var svcs []model.Service
	config.DB.Where("ip = ?", ip).Order("port").Find(&svcs)
	c.JSON(http.StatusOK, svcs)
}

// UpdateAssetOwner 在资产总览标注系统/负责人/部门（同步确认该 IP 的待确认 web 资产）
func UpdateAssetOwner(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		SystemName string `json:"systemName"`
		Owner      string `json:"owner"`
		Department string `json:"department"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var asset model.Asset
	if err := config.DB.First(&asset, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "资产不存在"})
		return
	}
	updates := map[string]interface{}{
		"system_name": req.SystemName,
		"owner":       req.Owner,
		"department":  req.Department,
	}
	// 标注即确认：无论该 IP 是否有 web 服务，标注了归属就算人工确认
	updates["status"] = "confirmed"
	if err := config.DB.Model(&model.Asset{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新", "confirmed": true})
}

// BatchDeleteAssets 批量删除资产
func BatchDeleteAssets(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择资产"})
		return
	}
	if err := service.BatchDeleteAssets(req.IDs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除", "count": len(req.IDs)})
}

// DownloadLedgerTemplate 下载台账模板
func DownloadLedgerTemplate(c *gin.Context) {
	f, err := service.GenerateTemplate()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer f.Close()
	c.Header("Content-Disposition", "attachment; filename=asset_ledger_template.xlsx")
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	if err := f.Write(c.Writer); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// ImportLedger 导入台账（上传 Excel）
func ImportLedger(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请上传 Excel 文件（字段名 file）"})
		return
	}
	const maxUpload = 50 << 20 // 50MB，防止超大文件把内存打满
	if file.Size > maxUpload {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "文件超过 50MB，请拆分后导入"})
		return
	}
	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取文件失败: " + err.Error()})
		return
	}
	defer src.Close()
	data, err := io.ReadAll(src)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取文件失败: " + err.Error()})
		return
	}

	rows, err := service.ParseLedgerFile(data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(rows) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未解析到有效数据行"})
		return
	}

	imported, updated, conflict, failed, errs := service.ImportLedger(rows)
	c.JSON(http.StatusOK, gin.H{
		"total":    len(rows),
		"imported": imported,
		"updated":  updated,
		"conflict": conflict,
		"failed":   failed,
		"errors":   errs,
		"message": fmt.Sprintf("新增 %d、更新 %d、冲突待处理 %d、失败 %d",
			imported, updated, conflict, failed),
	})
}

// BatchUpdateAssets 批量修改负责人 / 部门（字段为空则不改）
func BatchUpdateAssets(c *gin.Context) {
	var req struct {
		IDs        []uint `json:"ids"`
		Owner      string `json:"owner"`
		Department string `json:"department"`
		SystemName string `json:"systemName"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择资产"})
		return
	}
	updates := map[string]interface{}{}
	if req.Owner != "" {
		updates["owner"] = req.Owner
	}
	if req.Department != "" {
		updates["department"] = req.Department
	}
	if req.SystemName != "" {
		updates["system_name"] = req.SystemName
	}
	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要修改的字段"})
		return
	}
	if err := config.DB.Model(&model.Asset{}).Where("id IN ?", req.IDs).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新", "count": len(req.IDs)})
}

// ListImportConflicts 列出待处理的重复导入冲突
func ListImportConflicts(c *gin.Context) {
	var list []model.ImportConflict
	config.DB.Order("created_at desc").Find(&list)
	c.JSON(http.StatusOK, gin.H{"items": list, "total": len(list)})
}

// ResolveImportConflict 按旧/按新处理单条冲突
func ResolveImportConflict(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Choice  string `json:"choice"`  // old / new
		System  string `json:"system"`  // 可选：编辑后的系统名
		Owner   string `json:"owner"`   // 可选：编辑后的责任人
		Dept    string `json:"department"` // 可选：编辑后的部门
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Choice != "old" && req.Choice != "new") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "choice 必须是 old 或 new"})
		return
	}
	var cf model.ImportConflict
	if err := config.DB.First(&cf, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "冲突记录不存在"})
		return
	}
	if req.Choice == "new" {
		// 优先用前端编辑后的值，没传则用冲突记录里的新值
		sys, own, dept := cf.NewSystem, cf.NewOwner, cf.NewDept
		if req.System != "" {
			sys = req.System
		}
		if req.Owner != "" {
			own = service.NormalizeOwners(req.Owner)
		}
		if req.Dept != "" {
			dept = req.Dept
		}
		if own != "" {
			service.RegisterOwners(dept, own)
		}
		// 应用归属：状态回到 pending，等重新确认
		config.DB.Model(&model.Asset{}).Where("id = ?", cf.AssetID).Updates(map[string]interface{}{
			"system_name": sys,
			"owner":       own,
			"department":  dept,
			"source":      "ledger",
			"status":      "pending",
		})
	}
	// choice=old：什么都不做，已确认的旧值保留
	config.DB.Delete(&cf)
	c.JSON(http.StatusOK, gin.H{"message": "已处理", "choice": req.Choice})
}

// ResolveAllImportConflicts 批量处理（全部按旧 / 全部按新）
func ResolveAllImportConflicts(c *gin.Context) {
	var req struct {
		Choice string `json:"choice"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Choice != "old" && req.Choice != "new") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "choice 必须是 old 或 new"})
		return
	}
	var list []model.ImportConflict
	config.DB.Find(&list)
	if len(list) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "没有待处理冲突", "count": 0})
		return
	}
	if req.Choice == "new" {
		for _, cf := range list {
			config.DB.Model(&model.Asset{}).Where("id = ?", cf.AssetID).Updates(map[string]interface{}{
				"system_name": cf.NewSystem,
				"owner":       cf.NewOwner,
				"department":  cf.NewDept,
				"source":      "ledger",
				"status":      "pending",
			})
		}
	}
	config.DB.Where("1 = 1").Delete(&model.ImportConflict{})
	c.JSON(http.StatusOK, gin.H{"message": "已批量处理", "count": len(list), "choice": req.Choice})
}
