package service

import (
	"strings"
	"bytes"
	"fmt"
	"net/netip"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
)

// LedgerRow 台账一行
type LedgerRow struct {
	IP         string
	SystemName string
	Owner      string
	Department string
}

// GenerateTemplate 生成台账模板 Excel（4 列：IP/系统名/负责人/隶属部门）
func GenerateTemplate() (*excelize.File, error) {
	f := excelize.NewFile()
	sheet := "资产台账"
	_ = f.SetSheetName("Sheet1", sheet)

	headers := []string{"IP", "系统名", "负责人", "隶属部门"}
	for i, h := range headers {
		cell := fmt.Sprintf("%c1", 'A'+i)
		_ = f.SetCellValue(sheet, cell, h)
	}
	// 示例行
	_ = f.SetCellValue(sheet, "A2", "10.1.1.1")
	_ = f.SetCellValue(sheet, "B2", "示例系统")
	_ = f.SetCellValue(sheet, "C2", "张三")
	_ = f.SetCellValue(sheet, "D2", "信息中心")

	// 表头加粗
	style, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
	})
	_ = f.SetCellStyle(sheet, "A1", "D1", style)
	return f, nil
}

// ParseLedgerFile 解析上传的台账 Excel，返回行数据
func ParseLedgerFile(data []byte) ([]LedgerRow, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("打开 Excel 失败: %w", err)
	}
	defer f.Close()

	sheet := f.GetSheetName(0)
	rows, err := f.GetRows(sheet)
	if err != nil {
		return nil, fmt.Errorf("读取表格失败: %w", err)
	}

	var result []LedgerRow
	for i, row := range rows {
		if i == 0 { // 跳过表头
			continue
		}
		if len(row) == 0 || (len(row) > 0 && row[0] == "") {
			continue
		}
		r := LedgerRow{}
		if len(row) > 0 {
			r.IP = row[0]
		}
		if len(row) > 1 {
			r.SystemName = row[1]
		}
		if len(row) > 2 {
			r.Owner = row[2]
		}
		if len(row) > 3 {
			r.Department = row[3]
		}
		result = append(result, r)
	}
	return result, nil
}

// ImportLedger 导入台账：同一 (IP,系统名,负责人) 视为同一条归属，存在则更新，否则新增。
//
// 为什么不是「IP 存在即更新」：一个 IP 允许多归属（不同系统/负责人各一条），
// 按 IP 匹配会把新归属写到别人的行上，或把合法的第二归属误报成冲突。
// 冲突只发生在「同一条归属已被人工确认、而新台账改了它的部门/写法」时。
func ImportLedger(rows []LedgerRow) (imported, updated, conflict, failed int, errs []string) {
	for _, r := range rows {
		r.Owner = NormalizeOwners(r.Owner)
		if r.Owner != "" {
			RegisterOwners(r.Department, r.Owner)
		}
		// 校验 IP
		if _, err := netip.ParseAddr(r.IP); err != nil {
			failed++
			errs = append(errs, fmt.Sprintf("%s: IP 格式错误", r.IP))
			continue
		}
		if r.SystemName == "" {
			failed++
			errs = append(errs, fmt.Sprintf("%s: 系统名为空", r.IP))
			continue
		}

		var existing model.Asset
		err := config.DB.Where("ip = ?", r.IP).
			Where("COALESCE(system_name,'') = ?", strings.TrimSpace(r.SystemName)).
			Where("COALESCE(owner,'') = ?", r.Owner).
			Order("id").First(&existing).Error
		switch {
		case err == gorm.ErrRecordNotFound:
			// 该 IP 没有这条归属（要么全新，要么是合法的第二/第三归属）→ 新增一行
			asset := model.Asset{
				IP: r.IP, SystemName: r.SystemName, Owner: r.Owner,
				Department: r.Department, Source: "ledger", Status: "pending",
			}
			if createErr := config.DB.Create(&asset).Error; createErr != nil {
				failed++
				errs = append(errs, fmt.Sprintf("%s: %v", r.IP, createErr))
			} else {
				imported++
			}
		case err == nil:
			// 已人工确认的资产：不覆盖，落冲突表等人工决定
			if existing.Status == "confirmed" {
				// 部门等其余字段完全相同 → 无实质变化，不记录冲突
				if strings.TrimSpace(existing.SystemName) == strings.TrimSpace(r.SystemName) &&
					strings.TrimSpace(existing.Owner) == strings.TrimSpace(r.Owner) &&
					strings.TrimSpace(existing.Department) == strings.TrimSpace(r.Department) {
					updated++
					continue
				}
				// 同一条归属已有未处理冲突则更新新值，避免重复条目
				var c model.ImportConflict
				if e := config.DB.Where("asset_id = ?", existing.ID).First(&c).Error; e == nil {
					config.DB.Model(&c).Updates(map[string]interface{}{
						"new_system_name": r.SystemName,
						"new_owner":       r.Owner,
						"new_department":  r.Department,
					})
				} else {
					config.DB.Create(&model.ImportConflict{
						AssetID: existing.ID, IP: r.IP,
						OldSystem: existing.SystemName, OldOwner: existing.Owner, OldDept: existing.Department,
						NewSystem: r.SystemName, NewOwner: r.Owner, NewDept: r.Department,
					})
				}
				conflict++
				continue
			}
			// 未确认：新台账直接覆盖
			updateErr := config.DB.Model(&existing).Updates(map[string]interface{}{
				"system_name": r.SystemName,
				"owner":       r.Owner,
				"department":  r.Department,
				"source":      "ledger",
				"status":      "pending",
			}).Error
			if updateErr != nil {
				failed++
				errs = append(errs, fmt.Sprintf("%s: %v", r.IP, updateErr))
			} else {
				updated++
			}
		default:
			failed++
			errs = append(errs, fmt.Sprintf("%s: %v", r.IP, err))
		}
	}
	return
}

// NormalizeOwners 把多个责任人统一成逗号分隔（兼容 / ， 、 ; 空格 等分隔符）
func NormalizeOwners(raw string) string {
	if raw == "" {
		return ""
	}
	// 先把各种分隔符统一成半角逗号
	r := strings.NewReplacer(
		"/", ",", "，", ",", "、", ",", ";", ",", "；", ",", "|", ",", "＋", ",", "+", ",",
	)
	s := r.Replace(raw)
	parts := strings.Split(s, ",")
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return strings.Join(out, ",")
}

// RegisterOwners 把责任人自动挂到对应部门下（不存在则追加）
func RegisterOwners(department, owners string) {
	dept := strings.TrimSpace(department)
	if dept == "" || owners == "" {
		return
	}
	var d model.Department
	if err := config.DB.Where("name = ?", dept).First(&d).Error; err != nil {
		// 部门不存在：建一个
		d = model.Department{Name: dept}
		if config.DB.Create(&d).Error != nil {
			return
		}
	}
	existing := map[string]bool{}
	for _, x := range strings.Split(d.Owners, ",") {
		if n := strings.TrimSpace(x); n != "" {
			existing[n] = true
		}
	}
	changed := false
	for _, name := range strings.Split(owners, ",") {
		n := strings.TrimSpace(name)
		if n == "" || existing[n] {
			continue
		}
		d.Owners = strings.TrimSpace(d.Owners + "," + n)
		if strings.HasPrefix(d.Owners, ",") {
			d.Owners = strings.TrimPrefix(d.Owners, ",")
		}
		existing[n] = true
		changed = true
	}
	if changed {
		config.DB.Model(&model.Department{}).Where("id = ?", d.ID).Update("owners", d.Owners)
	}
}
