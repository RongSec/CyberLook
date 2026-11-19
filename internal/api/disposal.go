package api

import (
	"strconv"
	"strings"
	"fmt"
	"net/http"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"

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
	Category string `json:"category"` // host_new=主机新出现 / port_new=端口新出现 / changed=新变更
	// 台账登记信息（仅参考展示，不参与分类）
	LedgerCount int `json:"ledgerCount"`
	// Activity 该端口活跃度：active / sleeping
	Activity string `json:"activity"`
	Confirmed    bool       `json:"confirmed"`
	LedgerSystem string     `json:"ledgerSystem"`
	LedgerOwner  string     `json:"ledgerOwner"`
	LedgerDept   string     `json:"ledgerDept"`
	FirstSeen    *time.Time `json:"firstSeen"`
	LastSeen     *time.Time `json:"lastSeen"`
	CreatedAt    time.Time  `json:"createdAt"`
}

// listDisposalItems 待处置 web 资产（以扫描结果为准，台账只作参考）
//
// 分类口径（v0.0.49 重定）：
//   主机新出现  host_new  这个 IP 从未被登记/确认过（台账里也没有） → 需要先认领主机
//   端口新出现  port_new  主机已登记，但这个端口还没确认过          → 只需确认端口
//   新变更      changed   端口确认过，但标题与确认基线不一致          → 需要核对页面
//
// 不再有「台账有」这个标签：端口确认后该 IP 会进资产总览，如果还留「台账有」，
// 同 IP 其它端口会立刻变成"台账有"，看上去像"确认了又回来"。
// 台账信息只作为「台账登记」列的参考展示，不参与分类。
func listDisposalItems() []DisposalItem {
	var svcs []model.Service
	// 只保留真正能访问的：有响应码或有页面标题（哪怕 404/502 也算可达）
	config.DB.Where("protocol ILIKE 'http%' OR service ILIKE '%http%'").
		Where("COALESCE(status_code,'') <> '' OR COALESCE(title,'') <> '' OR COALESCE(content_length,0) > 0").
		Order("ip, port").Find(&svcs)

	// 一个 IP 可能有多条归属（多归属），台账信息取"最完整"的那条作参考
	assets := map[string][]model.Asset{}
	var rows []model.Asset
	config.DB.Find(&rows)
	for _, a := range rows {
		assets[a.IP] = append(assets[a.IP], a)
	}

	items := []DisposalItem{}
	for _, svc := range svcs {
		ledger, hasTrace := pickAsset(assets[svc.IP])

		it := DisposalItem{
			IP: svc.IP, Title: svc.Title, Fingerprint: svc.Fingerprint,
			WebPort: svc.Port, WebPorts: []int{svc.Port},
			StatusCode: svc.StatusCode, ContentLength: svc.ContentLength,
			PrevTitle: svc.PrevTitle, PrevFingerprint: svc.PrevFingerprint,
			PrevStatusCode: svc.PrevStatusCode, PrevContentLength: svc.PrevContentLength,
			FirstSeen: svc.FirstSeen, LastSeen: svc.LastSeen,
			Confirmed: svc.Confirmed,
			Activity:  svc.Activity,
		}
		if svc.FirstSeen != nil {
			it.CreatedAt = *svc.FirstSeen
		}
		if ledger != nil {
			it.LedgerSystem = ledger.SystemName
			it.LedgerOwner = ledger.Owner
			it.LedgerDept = ledger.Department
			it.LedgerCount = len(assets[svc.IP])
		}

		switch {
		case !hasTrace:
			// 没有任何登记/确认痕迹 → 主机级新出现
			it.Category = "host_new"
		case !svc.Confirmed:
			// 主机已登记，端口还没认过
			it.Category = "port_new"
		case strings.TrimSpace(svc.ConfirmedTitle) != strings.TrimSpace(svc.Title):
			it.Category = "changed"
		default:
			continue // 确认过且没变化，不再打扰
		}
		items = append(items, it)
	}
	return items
}

// pickAsset 从一个 IP 的多条归属里挑一条作展示，并判断这个 IP 是否"已登记"。
// 已登记 = 有人工痕迹：姓名/系统/部门任一非空，或确认过标题/指纹。
func pickAsset(list []model.Asset) (*model.Asset, bool) {
	if len(list) == 0 {
		return nil, false
	}
	var best *model.Asset
	bestScore := -1
	hasTrace := false
	for i := range list {
		a := &list[i]
		score := 0
		if strings.TrimSpace(a.SystemName) != "" {
			score += 2
		}
		if strings.TrimSpace(a.Owner) != "" {
			score++
		}
		if strings.TrimSpace(a.Department) != "" {
			score++
		}
		if score > bestScore {
			best, bestScore = a, score
		}
		if strings.TrimSpace(a.SystemName) != "" || strings.TrimSpace(a.Owner) != "" ||
			strings.TrimSpace(a.Department) != "" || strings.TrimSpace(a.ConfirmedTitle) != "" ||
			a.ConfirmedSimhash != 0 {
			hasTrace = true
		}
	}
	return best, hasTrace
}

// confirmPorts 确认一个 IP：所有服务标记已处置，并把**每个 web 端口当前的标题**记成该端口的基线。
// 基线必须按端口记，否则多端口 IP 里除一个端口外，其余端口永远对不上基线 → 永久"新变更"。
// 注意：标题为空时基线就记空串（空对空 = 没变化），不要再回退到 IP 级基线，否则空标题端口永远确认不掉。
// 注意：这是「整 IP 已确认归属」的口径，只应被显式填写归属的路径调用；
// 处置列表的「按台账确认」必须走端口级（ConfirmByLedger keys），否则会连坐未审端口。
func confirmPorts(ip string) {
	config.DB.Model(&model.Service{}).Where("ip = ?", ip).Update("confirmed", true)

	var svcs []model.Service
	config.DB.Where("ip = ? AND (protocol ILIKE 'http%' OR service ILIKE '%http%')", ip).Find(&svcs)
	for _, s := range svcs {
		config.DB.Model(&model.Service{}).Where("id = ?", s.ID).
			Update("confirmed_title", strings.TrimSpace(s.Title))
	}
}

// ConfirmByLedger 按台账确认。
// 粒度必须是端口：处置列表一行 = 一个端口，确认一行只认这一行。
// 早期实现按 IP 全量确认（confirmPorts），会把这个 IP 上**没被勾选的端口**一起标成已确认、
// 并用当前标题覆盖它们的基线，导致两件事：
//   1) 用户没审过的端口，之后标题一变就被判成「新变更」；
//   2) 真正的「新变更」行在被批量确认时被静默接受（基线被改写成新值）。
// 现在：keys 精确到端口，只写被选中端口的基线；资产级仅补一次归属痕迹，不覆盖已有基线。
func ConfirmByLedger(c *gin.Context) {
	var req struct {
		Keys []string `json:"keys"` // ip:port，精确到端口（推荐）
		IPs  []string `json:"ips"`  // 兼容旧调用：整 IP
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if len(req.Keys) == 0 && len(req.IPs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要确认的资产"})
		return
	}

	ok, skip := 0, 0
	touchedAssets := map[string]bool{}

	if len(req.Keys) > 0 {
		for _, k := range req.Keys {
			ip, port, err := splitKey(k)
			if err != nil {
				skip++
				continue
			}
			var svc model.Service
			if err := config.DB.Where("ip = ? AND port = ?", ip, port).First(&svc).Error; err != nil {
				skip++
				continue
			}
			// 只写这一行的基线（标题为空也照写：空对空 = 无变化，避免永久"新变更"）
			config.DB.Model(&model.Service{}).Where("id = ?", svc.ID).Updates(map[string]interface{}{
				"confirmed":       true,
				"confirmed_title": strings.TrimSpace(svc.Title),
			})
			if !touchedAssets[ip] {
				touchAssetConfirmed(ip, svc.Title, svc.PageSimhash)
				touchedAssets[ip] = true
			}
			ok++
		}
	} else {
		// 旧口径：整 IP（保留给脚本/老前端）。仍然逐端口写基线，但不覆盖已有基线的端口。
		for _, ip := range req.IPs {
			if strings.TrimSpace(ip) == "" {
				skip++
				continue
			}
			confirmPorts(ip)
			var svc model.Service
			if err := config.DB.Where("ip = ? AND (protocol ILIKE 'http%' OR title <> '')", ip).
				Order("port").First(&svc).Error; err == nil {
				touchAssetConfirmed(ip, svc.Title, svc.PageSimhash)
			}
			ok++
		}
	}

	c.JSON(http.StatusOK, gin.H{"confirmed": ok, "skip": skip})
}

// touchAssetConfirmed 记录"这个 IP 已经人工确认过"的痕迹。
// 只补空缺：已有的资产级基线不覆盖（否则会把其他端口的变更判定一起抹掉）。
func touchAssetConfirmed(ip, baselineTitle string, simhash uint64) {
	updates := map[string]interface{}{"status": "confirmed", "content_changed": false}
	var asset model.Asset
	if err := config.DB.Where("ip = ?", ip).Order("id").First(&asset).Error; err != nil {
		// 没有资产行：扫描直接发现 web 服务但还没建资产
		config.DB.Create(&model.Asset{
			IP: ip, Source: "scan", Status: "confirmed", ConfirmedTitle: strings.TrimSpace(baselineTitle),
			ConfirmedSimhash: simhash,
		})
		return
	}
	if strings.TrimSpace(asset.ConfirmedTitle) == "" {
		updates["confirmed_title"] = strings.TrimSpace(baselineTitle)
	}
	if asset.ConfirmedSimhash == 0 {
		updates["confirmed_simhash"] = simhash
	}
	config.DB.Model(&model.Asset{}).Where("ip = ?", ip).Updates(updates)
}

// splitKey 解析 ip:port
func splitKey(k string) (string, int, error) {
	parts := strings.SplitN(strings.TrimSpace(k), ":", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("bad key")
	}
	port, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("bad port")
	}
	return strings.TrimSpace(parts[0]), port, nil
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

	// 查/建资产记录：同一 IP 允许多归属，按 (IP, 系统, 负责人, 部门) 精确匹配
	if _, err := upsertOwnerAsset(req.IP, req.SystemName, req.Owner, req.Department); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 标记所有服务已处置，并按端口记录确认基线
	confirmPorts(req.IP)
	service.WakeServices(req.IP)

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
	if _, err := upsertOwnerAsset(ip, systemName, owner, department); err != nil {
		return err
	}
	confirmPorts(ip)
	service.WakeServices(ip)
	return nil
}

// upsertOwnerAsset 按 (IP, 系统, 负责人, 部门) 落一条归属记录。
// 同一 IP 允许多归属（不同系统/负责人各一行）；完全相同的归属只更新状态，不新增。
// 只动归属字段：title/fingerprint 等识别结果一律不碰，
// 批量处置弹窗里显示的「-」不会被写进库。
func upsertOwnerAsset(ip, systemName, owner, department string) (*model.Asset, error) {
	systemName = strings.TrimSpace(systemName)
	owner = strings.TrimSpace(owner)
	department = strings.TrimSpace(department)

	var asset model.Asset
	q := config.DB.Where("ip = ?", ip)
	q = q.Where("COALESCE(system_name,'') = ?", systemName)
	q = q.Where("COALESCE(owner,'') = ?", owner)
	q = q.Where("COALESCE(department,'') = ?", department)
	if err := q.Order("id").First(&asset).Error; err == nil {
		config.DB.Model(&model.Asset{}).Where("id = ?", asset.ID).
			Update("status", "confirmed")
		return &asset, nil
	}
	asset = model.Asset{
		IP: ip, Source: "scan", Status: "confirmed",
		SystemName: systemName, Owner: owner, Department: department,
	}
	if err := config.DB.Create(&asset).Error; err != nil {
		return nil, err
	}
	return &asset, nil
}
