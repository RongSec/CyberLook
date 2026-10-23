package api

import (
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
	if status != "" {
		q = q.Where("status = ?", status)
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
	// 该 IP 有 web 服务则自动确认（标注即确认）
	var webCnt int64
	config.DB.Model(&model.Service{}).Where("ip = ? AND protocol ILIKE 'http%'", asset.IP).Count(&webCnt)
	if webCnt > 0 {
		updates["status"] = "confirmed"
	}
	if err := config.DB.Model(&model.Asset{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新", "confirmed": webCnt > 0})
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

	imported, updated, failed, errs := service.ImportLedger(rows)
	c.JSON(http.StatusOK, gin.H{
		"total":    len(rows),
		"imported": imported,
		"updated":  updated,
		"failed":   failed,
		"errors":   errs,
	})
}
