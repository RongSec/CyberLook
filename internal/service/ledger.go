package service

import (
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

// ImportLedger 导入台账：IP 已存在则更新，否则新增
func ImportLedger(rows []LedgerRow) (imported, updated, failed int, errs []string) {
	for _, r := range rows {
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
		err := config.DB.Where("ip = ?", r.IP).First(&existing).Error
		switch {
		case err == gorm.ErrRecordNotFound:
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
			// IP 已存在：新台账覆盖旧台账（置信度更新）
			// 同时清空「确认时的标题/页面指纹」——系统信息变了，之前的处置基线失效，需重新处置
			updateErr := config.DB.Model(&existing).Updates(map[string]interface{}{
				"system_name":       r.SystemName,
				"owner":             r.Owner,
				"department":        r.Department,
				"source":            "ledger",
				"status":            "pending",
				"confirmed_title":   "",
				"confirmed_simhash": 0,
				"content_changed":   false,
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
