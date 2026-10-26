package api

import (
	"fmt"
	"net/http"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

// DisposalItem 资产处置项（待确认 web 资产 + 其 web 服务信息）
type DisposalItem struct {
	IP           string     `json:"ip"`
	Title        string     `json:"title"`
	Fingerprint  string     `json:"fingerprint"`
	WebPort      int        `json:"webPort"`
	StatusCode   string     `json:"statusCode"`
	ContentLength int       `json:"contentLength"`
	Category     string     `json:"category"` // new=新出现 / in_ledger=台账有 / changed=新变更
	InLedger     bool       `json:"inLedger"`
	Confirmed    bool       `json:"confirmed"`
	LedgerSystem string     `json:"ledgerSystem"`
	LedgerOwner  string     `json:"ledgerOwner"`
	LedgerDept   string     `json:"ledgerDept"`
	FirstSeen    *time.Time `json:"firstSeen"`
	LastSeen     *time.Time `json:"lastSeen"`
	CreatedAt    time.Time  `json:"createdAt"`
}

// listDisposalItems 待处置 web 资产（以扫描结果为准，台账只作参考）
// 分类规则：
//   new       台账无此 IP                       → 红
//   in_ledger 台账有、尚未按台账确认             → 绿
//   changed   台账有、已确认但标题变了           → 黄
// 已确认且标题未变的不再列出
func listDisposalItems() []DisposalItem {
	var svcs []model.Service
	// 只保留真正能访问的：有响应码或有页面标题（哪怕 404/502 也算可达）
	config.DB.Where("protocol ILIKE 'http%' OR service ILIKE '%http%'").
		Where("COALESCE(status_code,'') <> '' OR COALESCE(title,'') <> ''").
		Order("ip, port").Find(&svcs)

	var ledgers []model.Asset
	config.DB.Where("source = 'ledger'").Find(&ledgers)
	ledgerMap := make(map[string]model.Asset, len(ledgers))
	for _, a := range ledgers {
		ledgerMap[a.IP] = a
	}

	items := []DisposalItem{}
	seen := map[string]bool{}
	for _, svc := range svcs {
		if seen[svc.IP] {
			continue
		}
		seen[svc.IP] = true

		it := DisposalItem{
			IP: svc.IP, Title: svc.Title, Fingerprint: svc.Fingerprint,
			WebPort: svc.Port, StatusCode: svc.StatusCode, ContentLength: svc.ContentLength,
			FirstSeen: svc.FirstSeen, LastSeen: svc.LastSeen,
			Confirmed: svc.Confirmed,
		}
		if svc.FirstSeen != nil {
			it.CreatedAt = *svc.FirstSeen
		}

		ledger, hasLedger := ledgerMap[svc.IP]
		if hasLedger {
			it.InLedger = true
			it.LedgerSystem = ledger.SystemName
			it.LedgerOwner = ledger.Owner
			it.LedgerDept = ledger.Department

			switch {
			case ledger.ContentChanged:
				// 页面内容与确认时相比发生实质变化（SimHash 汉明距离超阈值）
				it.Category = "changed"
			case ledger.Status == "confirmed" || ledger.ConfirmedTitle != "" || ledger.ConfirmedSimhash != 0 || svc.Confirmed:
				continue // 已处置且页面未变，不用管
			default:
				it.Category = "in_ledger" // 台账有，待确认
			}
		} else {
			// 台账没有，但如果已处置（svc.Confirmed），不该再出现在待处置列表
			if svc.Confirmed {
				continue
			}
			it.Category = "new" // 台账没有
		}

		items = append(items, it)
	}
	return items
}

// ConfirmByLedger 按台账确认（把当前扫描到的标题记为台账基线）
func ConfirmByLedger(c *gin.Context) {
	var req struct {
		IPs []string `json:"ips"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IPs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要确认的资产"})
		return
	}
	ok := 0
	for _, ip := range req.IPs {
		var svc model.Service
		if err := config.DB.Where("ip = ? AND (protocol ILIKE 'http%' OR title <> '')", ip).
			Order("port").First(&svc).Error; err != nil {
			continue
		}
		// 记录确认时的页面指纹作为基线，同时清除变更标记
		config.DB.Model(&model.Asset{}).Where("ip = ? AND source = 'ledger'", ip).
			Updates(map[string]interface{}{
				"confirmed_title":    svc.Title,
				"confirmed_simhash":  svc.PageSimhash,
				"content_changed":    false,
			})
		config.DB.Model(&model.Service{}).Where("ip = ?", ip).Update("confirmed", true)
		// 无论台账导入还是扫描发现，处置确认后统一标记为 confirmed
		// 若 assets 表还没有这条记录（扫描直接发现 web 服务但未建资产），这里补一条
		var asset model.Asset
		if err := config.DB.Where("ip = ?", ip).First(&asset).Error; err != nil {
			asset = model.Asset{
				IP: ip, Source: "scan", Status: "confirmed",
			}
			config.DB.Create(&asset)
		} else {
			config.DB.Model(&model.Asset{}).Where("ip = ?", ip).
				Update("status", "confirmed")
		}
		ok++
	}
	c.JSON(http.StatusOK, gin.H{"confirmed": ok})
}

// ListDisposal 资产处置列表
func ListDisposal(c *gin.Context) {
	items := listDisposalItems()
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// ExportDisposal 导出未处理资产表格
func ExportDisposal(c *gin.Context) {
	items := listDisposalItems()
	f := excelize.NewFile()
	sheet := "资产处置"
	f.SetSheetName("Sheet1", sheet)
	headers := []string{"IP", "网站标题", "Web端口", "响应码", "指纹", "发现时间"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, h)
	}
	for r, it := range items {
		row := r + 2
		vals := []interface{}{it.IP, it.Title, it.WebPort, it.StatusCode, it.Fingerprint, it.CreatedAt.Format("2006-01-02 15:04")}
		for cidx, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(cidx+1, row)
			f.SetCellValue(sheet, cell, safeCell(v))
		}
	}
	filename := fmt.Sprintf("资产处置_%s.xlsx", time.Now().Format("20060102_150405"))
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", "attachment; filename="+filename)
	if err := f.Write(c.Writer); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// DeleteDisposal 删除资产处置项（按 IP 删除资产及其服务记录）
func DeleteDisposal(c *gin.Context) {
	ip := c.Param("ip")
	if ip == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "IP 不能为空"})
		return
	}
	if err := config.DB.Where("ip = ?", ip).Delete(&model.Asset{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	config.DB.Where("ip = ?", ip).Delete(&model.Service{})
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// BatchDeleteDisposal 批量删除资产处置项
func BatchDeleteDisposal(c *gin.Context) {
	var req struct {
		IPs []string `json:"ips"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IPs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要删除的记录"})
		return
	}
	config.DB.Where("ip IN ?", req.IPs).Delete(&model.Asset{})
	config.DB.Where("ip IN ?", req.IPs).Delete(&model.Service{})
	c.JSON(http.StatusOK, gin.H{"deleted": len(req.IPs)})
}

// ConfirmDisposal 确认处置（按 IP，带部门/负责人/系统名）
func ConfirmDisposal(c *gin.Context) {
	var req struct {
		IP         string `json:"ip"`
		SystemName string `json:"systemName"`
		Owner      string `json:"owner"`
		Department string `json:"department"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IP == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Owner == "" || req.Department == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须选择部门和负责人"})
		return
	}

	// 查/建资产记录
	var asset model.Asset
	if err := config.DB.Where("ip = ?", req.IP).First(&asset).Error; err != nil {
		asset = model.Asset{
			IP: req.IP, Source: "scan", Status: "confirmed",
			SystemName: req.SystemName, Owner: req.Owner, Department: req.Department,
		}
		config.DB.Create(&asset)
	} else {
		config.DB.Model(&model.Asset{}).Where("ip = ?", req.IP).Updates(map[string]interface{}{
			"status":       "confirmed",
			"system_name":  req.SystemName,
			"owner":        req.Owner,
			"department":   req.Department,
		})
	}

	// 标记所有服务已处置
	config.DB.Model(&model.Service{}).Where("ip = ?", req.IP).Update("confirmed", true)

	// 记录页面指纹基线
	var svc model.Service
	if err := config.DB.Where("ip = ? AND (protocol ILIKE 'http%' OR title <> '')", req.IP).
		Order("port").First(&svc).Error; err == nil {
		config.DB.Model(&model.Asset{}).Where("ip = ?", req.IP).Updates(map[string]interface{}{
			"confirmed_title":   svc.Title,
			"confirmed_simhash": svc.PageSimhash,
			"content_changed":   false,
		})
	}

	c.JSON(http.StatusOK, gin.H{"message": "已确认入库"})
}
