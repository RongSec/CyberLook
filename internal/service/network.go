package service

import (
	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// ---------- 网段（机房） ----------

// ListSegments 网段列表
func ListSegments() []model.IPSegment {
	var items []model.IPSegment
	config.DB.Order("name").Find(&items)
	return items
}

// CreateSegment 新增网段
func CreateSegment(name, cidr, description string) error {
	return config.DB.Create(&model.IPSegment{Name: name, CIDR: cidr, Description: description}).Error
}

// DeleteSegment 删除网段
func DeleteSegment(id uint) error {
	return config.DB.Delete(&model.IPSegment{}, id).Error
}

// ---------- 免扫描清单 ----------

// ListExcludeRanges 免扫描清单列表
func ListExcludeRanges() []model.ExcludeRange {
	var items []model.ExcludeRange
	config.DB.Order("id").Find(&items)
	return items
}

// CreateExcludeRange 新增免扫描范围
func CreateExcludeRange(ipRange, reason string) error {
	return config.DB.Create(&model.ExcludeRange{IPRange: ipRange, Reason: reason}).Error
}

// DeleteExcludeRange 删除免扫描范围
func DeleteExcludeRange(id uint) error {
	return config.DB.Delete(&model.ExcludeRange{}, id).Error
}

// ExcludeRangeStrings 返回免扫描清单的 IP 范围字符串列表（供扫描排除用）
func ExcludeRangeStrings() []string {
	var items []model.ExcludeRange
	config.DB.Find(&items)
	var out []string
	for _, it := range items {
		if it.IPRange != "" {
			out = append(out, it.IPRange)
		}
	}
	return out
}
