package service

import (
	"strings"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"gorm.io/gorm"
)

// ruleKeyword 规则对应的指纹关键词
func ruleKeyword(rule model.ComplianceRule) string {
	name := strings.ToLower(rule.Name)
	keywords := []struct{ kw, match string }{
		{"ssh", "ssh"}, {"mysql", "mysql"}, {"redis", "redis"},
		{"postgres", "postgres"}, {"sql server", "sqlserver"}, {"rdp", "rdp"},
		{"mongodb", "mongo"}, {"elasticsearch", "elastic"},
		{"memcached", "memcache"}, {"zookeeper", "zookeeper"}, {"kafka", "kafka"},
	}
	for _, k := range keywords {
		if strings.Contains(name, k.kw) {
			return k.match
		}
	}
	return ""
}

// MatchRule 端口 或 指纹 任一命中即判违规
func MatchRule(rule model.ComplianceRule, svc model.Service) bool {
	// 端口命中即违规
	if svc.Port == rule.Port {
		return true
	}
	// 指纹命中即违规（端口不同但服务类型匹配，如 2222 端口的 ssh）
	kw := ruleKeyword(rule)
	if kw == "" {
		return false
	}
	hay := strings.ToLower(svc.Service + " " + svc.Banner + " " + svc.Fingerprint + " " + svc.Protocol)
	return strings.Contains(hay, kw)
}

// CheckComplianceForService 单服务实时合规检测（扫描入库时即时调用）
func CheckComplianceForService(svc model.Service) {
	var rules []model.ComplianceRule
	config.DB.Where("enabled = true").Find(&rules)
	now := time.Now()
	for _, rule := range rules {
		if !MatchRule(rule, svc) {
			continue
		}
		var cr model.ComplianceResult
		err := config.DB.Where("ip = ? AND port = ? AND rule_id = ?", svc.IP, svc.Port, rule.ID).First(&cr).Error
		if err == gorm.ErrRecordNotFound {
			config.DB.Create(&model.ComplianceResult{
				AssetID: svc.AssetID, IP: svc.IP, Port: svc.Port, RuleID: &rule.ID,
				Status: "violation", DetectedAt: &now, LastSeen: &now,
			})
		} else if err == nil {
			config.DB.Model(&cr).Updates(map[string]interface{}{"status": "violation", "last_seen": now})
		}
	}
}

// ListRules 合规规则列表
func ListRules() []model.ComplianceRule {
	var rules []model.ComplianceRule
	config.DB.Order("port").Find(&rules)
	return rules
}

// UpsertRule 新增/更新规则
func UpsertRule(r *model.ComplianceRule) error {
	if r.ID == 0 {
		return config.DB.Create(r).Error
	}
	return config.DB.Model(&model.ComplianceRule{}).Where("id = ?", r.ID).
		Updates(map[string]interface{}{"name": r.Name, "port": r.Port, "protocol": r.Protocol, "enabled": r.Enabled}).Error
}

// DeleteRule 删除规则
func DeleteRule(id uint) error {
	config.DB.Where("rule_id = ?", id).Delete(&model.ComplianceResult{})
	return config.DB.Delete(&model.ComplianceRule{}, id).Error
}

// ListWhitelist 堡垒机白名单
func ListWhitelist() []model.BastionWhitelist {
	var wl []model.BastionWhitelist
	config.DB.Order("ip").Find(&wl)
	return wl
}

// AddWhitelist 添加白名单
func AddWhitelist(ip, note string) error {
	return config.DB.Create(&model.BastionWhitelist{IP: ip, Note: note}).Error
}

// DeleteWhitelist 删除白名单
func DeleteWhitelist(id uint) error {
	return config.DB.Delete(&model.BastionWhitelist{}, id).Error
}

// ListComplianceResults 合规结果列表
func ListComplianceResults(status string, page, size int) ([]model.ComplianceResult, int64) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	q := config.DB.Model(&model.ComplianceResult{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	q.Count(&total)
	var results []model.ComplianceResult
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&results)
	return results, total
}

// RunComplianceCheck 一键检测：开放敏感端口 ∩ 规则 - 白名单 = 违规；端口已关 = 已整改
func RunComplianceCheck() (int, int, error) {
	var rules []model.ComplianceRule
	config.DB.Where("enabled = true").Find(&rules)

	now := time.Now()
	newViolation := 0
	// 一次取出全部服务，按「端口 或 指纹」匹配
	var allSvcs []model.Service
	config.DB.Find(&allSvcs)
	for _, rule := range rules {
		for _, svc := range allSvcs {
			if !MatchRule(rule, svc) {
				continue
			}
			var cr model.ComplianceResult
			err := config.DB.Where("ip = ? AND port = ? AND rule_id = ?", svc.IP, svc.Port, rule.ID).First(&cr).Error
			if err == gorm.ErrRecordNotFound {
				config.DB.Create(&model.ComplianceResult{
					AssetID: svc.AssetID, IP: svc.IP, Port: svc.Port, RuleID: &rule.ID,
					Status: "violation", DetectedAt: &now, LastSeen: &now,
				})
				newViolation++
			} else if err == nil {
				config.DB.Model(&cr).Updates(map[string]interface{}{"status": "violation", "last_seen": now})
			}
		}
	}

	// 已整改：违规记录对应的服务已不存在（端口已关）
	fixed := 0
	var violations []model.ComplianceResult
	config.DB.Where("status = 'violation'").Find(&violations)
	for _, v := range violations {
		var count int64
		config.DB.Model(&model.Service{}).Where("ip = ? AND port = ?", v.IP, v.Port).Count(&count)
		if count == 0 {
			config.DB.Model(&v).Updates(map[string]interface{}{"status": "fixed", "fixed_at": now})
			fixed++
		}
	}
	return newViolation, fixed, nil
}
