package service

import (
	"fmt"
	"strings"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"gorm.io/gorm"
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

// ExcludeReasonDefault 原因为空时的归类名。
// 之前的交互强制每条都写原因，实际库里堆了一大批空原因，列表上看不出哪条是哪类，
// 现在统一归到「未分类」，至少一眼能看出"这批没写原因"。
const ExcludeReasonDefault = "未分类"

// NormalizeExcludeReason 统一原因：去空白，空值归「未分类」
func NormalizeExcludeReason(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "未填写" {
		return ExcludeReasonDefault
	}
	return s
}

// ExcludeGroup 按原因聚合后的一组排除范围（列表一行 = 一个原因）
type ExcludeGroup struct {
	Reason    string    `json:"reason"`
	Count     int       `json:"count"`
	Ranges    []string  `json:"ranges"`    // 该原因下的全部条目
	Text      string    `json:"text"`      // 换行拼接，编辑框直接展示
	Preview   string    `json:"preview"`   // 列表里展示的前几条（节省列宽）
	CreatedAt time.Time `json:"createdAt"` // 组内最早一条的添加时间
}

// ListExcludeGroups 按原因聚合免扫描清单。
// 为什么不在 /api/excludes 上直接改：那个接口是公开的，agent 拉去拼扫描排除列表，
// 必须是扁平字符串数组；聚合视图只给前端页面用。
func ListExcludeGroups() []ExcludeGroup {
	var items []model.ExcludeRange
	config.DB.Order("id").Find(&items)

	idx := map[string]*ExcludeGroup{}
	var order []string
	for _, it := range items {
		reason := NormalizeExcludeReason(it.Reason)
		g, ok := idx[reason]
		if !ok {
			g = &ExcludeGroup{Reason: reason, CreatedAt: it.CreatedAt}
			idx[reason] = g
			order = append(order, reason)
		}
		g.Ranges = append(g.Ranges, it.IPRange)
		g.Count++
		if g.CreatedAt.IsZero() || (!it.CreatedAt.IsZero() && it.CreatedAt.Before(g.CreatedAt)) {
			g.CreatedAt = it.CreatedAt
		}
	}
	out := make([]ExcludeGroup, 0, len(order))
	for _, reason := range order {
		g := idx[reason]
		g.Text = strings.Join(g.Ranges, "\n")
		// 预览：前 6 条，多了给省略号
		n := len(g.Ranges)
		if n > 6 {
			g.Preview = strings.Join(g.Ranges[:6], "、") + fmt.Sprintf(" …等 %d 条", n)
		} else {
			g.Preview = strings.Join(g.Ranges, "、")
		}
		out = append(out, *g)
	}
	return out
}

// ReplaceExcludeGroup 整组替换：把 oldReason 下的所有条目删掉，按新原因 + 新列表重建。
// 编辑框里改一行 IP 就等于改这一组的成员，不需要前端逐条 diff。
func ReplaceExcludeGroup(oldReason, newReason string, ranges []string) (added int, err error) {
	oldReason = NormalizeExcludeReason(oldReason)
	newReason = NormalizeExcludeReason(newReason)

	// 去重 + 去空
	seen := map[string]bool{}
	var list []string
	for _, r := range ranges {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		list = append(list, r)
	}
	if len(list) == 0 {
		return 0, fmt.Errorf("至少要保留一个 IP 或范围")
	}

	return added, config.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("COALESCE(NULLIF(TRIM(reason), ''), ?) IN (?, ?)",
			ExcludeReasonDefault, oldReason, "未填写").Delete(&model.ExcludeRange{}).Error; err != nil {
			return err
		}
		for _, r := range list {
			if err := tx.Create(&model.ExcludeRange{IPRange: r, Reason: newReason}).Error; err != nil {
				return err
			}
			added++
		}
		return nil
	})
}

// DeleteExcludeGroup 按原因整组删除
func DeleteExcludeGroup(reason string) (int64, error) {
	reason = NormalizeExcludeReason(reason)
	res := config.DB.Where("COALESCE(NULLIF(TRIM(reason), ''), ?) = ?", ExcludeReasonDefault, reason).
		Delete(&model.ExcludeRange{})
	return res.RowsAffected, res.Error
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
