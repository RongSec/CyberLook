package api

import (
	"strconv"
	"strings"
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
	WebPort      int        `json:"webPort"`   // 主端口（有标题/有响应的那条）
	WebPorts     []int      `json:"webPorts"`
	PrevTitle       string `json:"prevTitle"`
	PrevFingerprint string `json:"prevFingerprint"`
	PrevStatusCode  string `json:"prevStatusCode"`
	PrevContentLength int  `json:"prevContentLength"`
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
//   new       台账/资产里都没有这个 IP              → 红
//   in_ledger 这个 IP 已确认过（台账导入或有确认痕迹）→ 绿，只需确认这次新出现的端口
//   changed   该端口确认过、但标题/内容变了          → 黄
// 已确认且标题未变的不再列出
func listDisposalItems() []DisposalItem {
	var svcs []model.Service
	// 只保留真正能访问的：有响应码或有页面标题（哪怕 404/502 也算可达）
	config.DB.Where("protocol ILIKE 'http%' OR service ILIKE '%http%'").
		// "有 web 服务就要处置"：只要有响应码、标题或响应体长度任一就算可达
		Where("COALESCE(status_code,'') <> '' OR COALESCE(title,'') <> '' OR COALESCE(content_length,0) > 0").
		Order("ip, port").Find(&svcs)

	// assets.ip 是唯一索引（一个 IP 一行），来源两类：
	//   ledger = 台账导入；scan = 扫描时自动建的主机记录（非 web 服务默认 confirmed，web 服务 pending）
	// 之前只把 source=ledger 当"台账有"，导致「已确认过的 IP 上又扫出新端口」被误判成新出现。
	var assets []model.Asset
	config.DB.Find(&assets)
	assetMap := make(map[string]model.Asset, len(assets))
	for _, a := range assets {
		assetMap[a.IP] = a
	}

	// 每个 web 端口一行，端口之间是平行关系（同一 IP 的多个 web 端口 = 同一业务的不同页面）
	items := []DisposalItem{}
	for _, svc := range svcs {
		ports := []int{svc.Port}

		it := DisposalItem{
			IP: svc.IP, Title: svc.Title, Fingerprint: svc.Fingerprint,
			WebPort: svc.Port, WebPorts: ports, StatusCode: svc.StatusCode, ContentLength: svc.ContentLength,
			PrevTitle: svc.PrevTitle, PrevFingerprint: svc.PrevFingerprint,
			PrevStatusCode: svc.PrevStatusCode, PrevContentLength: svc.PrevContentLength,
			FirstSeen: svc.FirstSeen, LastSeen: svc.LastSeen,
			Confirmed: svc.Confirmed,
		}
		if svc.FirstSeen != nil {
			it.CreatedAt = *svc.FirstSeen
		}

		asset, hasAsset := assetMap[svc.IP]
		// 人工确认痕迹：确认过页面标题/内容指纹，或填过归属。只有这种才代表"这个 IP 我们知道是谁的"
		hasTrace := asset.ConfirmedTitle != "" || asset.ConfirmedSimhash != 0 ||
			strings.TrimSpace(asset.Owner) != "" || strings.TrimSpace(asset.SystemName) != ""

		switch {
		case !hasAsset:
			// 台账和资产里都没有 → 真·新出现；已处置过的不再打扰
			if svc.Confirmed {
				continue
			}
			it.Category = "new"

		case asset.Source == "ledger":
			// ==== 台账导入：原有口径，保持不动 ====
			it.InLedger = true
			it.LedgerSystem = asset.SystemName
			it.LedgerOwner = asset.Owner
			it.LedgerDept = asset.Department

			// 只有「这个端口自己确认过」才有基线可比。资产级 status/confirmed_title 是 IP 口径的，
			// 拿去比较端口会把「已确认 IP 上新出现的端口」误判成变更（尤其是标题为空时永远对不上）。
			if !svc.Confirmed {
				it.Category = "in_ledger" // 台账有，端口待确认
				break
			}
			if asset.ContentChanged || strings.TrimSpace(svc.ConfirmedTitle) != strings.TrimSpace(svc.Title) {
				it.Category = "changed" // 页面内容变化，或标题与确认时不符
				break
			}
			continue // 已确认且没变化，不再打扰

		case hasTrace:
			// ==== 扫描建的资产，但这个 IP 已经人工确认过 ====
			// 端口级判断：这个端口自己确认过才算"有基线可比"，否则只是这个 IP 上多开了一个口
			it.InLedger = true
			it.LedgerSystem = asset.SystemName
			it.LedgerOwner = asset.Owner
			it.LedgerDept = asset.Department

			if !svc.Confirmed {
				it.Category = "in_ledger" // IP 已知，新端口 → 台账有，直接确认归属即可
				break
			}
			if asset.ContentChanged || strings.TrimSpace(svc.ConfirmedTitle) != strings.TrimSpace(svc.Title) {
				it.Category = "changed"
				break
			}
			continue // 该端口确认过且没变化，不打扰

		default:
			// ==== 扫描自动建的主机记录，没有任何归属/确认信息 ====
			// 这种"发现过"不含任何人工判断，仍按新出现处理（否则新出现分类会被淹没）；
			// 也不显示台账字段，避免出现"（台账未填系统名）"的误导提示
			if svc.Confirmed {
				continue
			}
			it.Category = "new"
		}

		items = append(items, it)
	}
	return items
}

// confirmPorts 确认一个 IP：所有服务标记已处置，并把**每个 web 端口当前的标题**记成该端口的基线。
// 基线必须按端口记，否则多端口 IP 里除一个端口外，其余端口永远对不上基线 → 永久"新变更"。
// 注意：标题为空时基线就记空串（空对空 = 没变化），不要再回退到 IP 级基线，否则空标题端口永远确认不掉。
func confirmPorts(ip string) {
	config.DB.Model(&model.Service{}).Where("ip = ?", ip).Update("confirmed", true)

	var svcs []model.Service
	config.DB.Where("ip = ? AND (protocol ILIKE 'http%' OR service ILIKE '%http%')", ip).Find(&svcs)
	for _, s := range svcs {
		config.DB.Model(&model.Service{}).Where("id = ?", s.ID).
			Update("confirmed_title", strings.TrimSpace(s.Title))
	}
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
		hasSvc := config.DB.Where("ip = ? AND (protocol ILIKE 'http%' OR title <> '')", ip).
			Order("port").First(&svc).Error == nil

		// 基线不再限制 source：扫描建的资产同样要写，否则确认完立刻又变"新变更"，永远确认不掉
		updates := map[string]interface{}{"status": "confirmed", "content_changed": false}
		if hasSvc {
			updates["confirmed_title"] = svc.Title
			updates["confirmed_simhash"] = svc.PageSimhash
		}
		config.DB.Model(&model.Asset{}).Where("ip = ?", ip).Updates(updates)

		// 该 IP 所有服务标记已处置 + 每个 web 端口写自己的标题基线
		confirmPorts(ip)

		// 若 assets 表还没有这条记录（扫描直接发现 web 服务但未建资产），这里补一条
		var asset model.Asset
		if err := config.DB.Where("ip = ?", ip).First(&asset).Error; err != nil {
			asset = model.Asset{
				IP: ip, Source: "scan", Status: "confirmed",
			}
			config.DB.Create(&asset)
		}
		ok++
	}
	c.JSON(http.StatusOK, gin.H{"confirmed": ok})
}

// ListDisposal 资产处置列表
func ListDisposal(c *gin.Context) {
	items := listDisposalItems()
	// 响应码过滤（逗号分隔），后端过滤避免前端漏判
	if sc := c.Query("statusCodes"); sc != "" {
		want := map[string]bool{}
		for _, x := range strings.Split(sc, ",") {
			if t := strings.TrimSpace(x); t != "" {
				want[t] = true
			}
		}
		if len(want) > 0 {
			filtered := items[:0]
			for _, it := range items {
				if want[it.StatusCode] {
					filtered = append(filtered, it)
				}
			}
			items = filtered
		}
	}
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
		portStr := strconv.Itoa(it.WebPort)
		if len(it.WebPorts) > 0 {
			ps := make([]string, 0, len(it.WebPorts))
			for _, p := range it.WebPorts {
				ps = append(ps, strconv.Itoa(p))
			}
			portStr = strings.Join(ps, ",")
		}
		vals := []interface{}{it.IP, it.Title, portStr, it.StatusCode, it.Fingerprint, it.CreatedAt.Format("2006-01-02 15:04")}
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
		IPs  []string `json:"ips"`
		Keys []string `json:"keys"` // ip:port —— 精确到端口删除（前端勾选精确到行后走这个）
	}
	if err := c.ShouldBindJSON(&req); err != nil || (len(req.IPs) == 0 && len(req.Keys) == 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要删除的记录"})
		return
	}

	// 精确到端口：只删选中的端口，不再连坐同 IP 的其它端口
	if len(req.Keys) > 0 {
		deleted := 0
		ips := map[string]struct{}{}
		for _, k := range req.Keys {
			parts := strings.SplitN(strings.TrimSpace(k), ":", 2)
			if len(parts) != 2 {
				continue
			}
			port, err := strconv.Atoi(parts[1])
			if err != nil || port <= 0 {
				continue
			}
			if r := config.DB.Where("ip = ? AND port = ?", parts[0], port).Delete(&model.Service{}); r.Error == nil {
				deleted += int(r.RowsAffected)
				ips[parts[0]] = struct{}{}
			}
		}
		// 该 IP 已无任何服务记录时，清掉扫描建的资产行；台账导入(source=ledger)的资产不动，避免误删台账
		for ip := range ips {
			var left int64
			config.DB.Model(&model.Service{}).Where("ip = ?", ip).Count(&left)
			if left == 0 {
				config.DB.Where("ip = ? AND source <> 'ledger'", ip).Delete(&model.Asset{})
			}
		}
		c.JSON(http.StatusOK, gin.H{"deleted": deleted})
		return
	}

	// 兼容旧调用：按 IP 整体删除
	config.DB.Where("ip IN ?", req.IPs).Delete(&model.Asset{})
	config.DB.Where("ip IN ?", req.IPs).Delete(&model.Service{})
	c.JSON(http.StatusOK, gin.H{"deleted": len(req.IPs)})
}

// ConfirmDisposal 确认处置（按 IP，带部门/负责人/系统名）
func ConfirmDisposal(c *gin.Context) {
	var req struct {
		IP         string   `json:"ip"`
		IPs        []string `json:"ips"` // 批量处置（与 ip 二选一）
		SystemName string   `json:"systemName"`
		Owner      string   `json:"owner"`
		Department string   `json:"department"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	// 批量：逐条复用单条逻辑
	if len(req.IPs) > 0 {
		if req.Owner == "" || req.Department == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "必须选择部门和负责人"})
			return
		}
		ok, skip := 0, 0
		for _, one := range req.IPs {
			ip := strings.TrimSpace(one)
			if ip == "" {
				continue
			}
			if err := confirmOneIP(ip, req.SystemName, req.Owner, req.Department); err != nil {
				skip++
				continue
			}
			ok++
		}
		c.JSON(http.StatusOK, gin.H{"message": "已确认 " + strconv.Itoa(ok) + " 个资产", "ok": ok, "skip": skip})
		return
	}
	if req.IP == "" {
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

	// 标记所有服务已处置，并按端口记录确认基线
	confirmPorts(req.IP)

	// 记录页面指纹基线（IP 级，内容变更判定用）
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

// confirmOneIP 确认单个 IP 的归属（批量处置复用）
func confirmOneIP(ip, systemName, owner, department string) error {
	var asset model.Asset
	if err := config.DB.Where("ip = ?", ip).First(&asset).Error; err != nil {
		a := model.Asset{
			IP: ip, Source: "scan", Status: "confirmed",
			SystemName: systemName, Owner: owner, Department: department,
		}
		if err := config.DB.Create(&a).Error; err != nil {
			return err
		}
		confirmPorts(ip)
		return nil
	}
	if err := config.DB.Model(&model.Asset{}).Where("ip = ?", ip).Updates(map[string]interface{}{
		"status": "confirmed", "system_name": systemName,
		"owner": owner, "department": department,
	}).Error; err != nil {
		return err
	}
	// 批量处置原来只改资产、没标记服务已确认，也没写基线 → 确认完行还在、还永远算"台账有"
	confirmPorts(ip)
	return nil
}
