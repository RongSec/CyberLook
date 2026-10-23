package service

import (
	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// ListTemplates 扫描模板列表
func ListTemplates() []model.ScanTemplate {
	var items []model.ScanTemplate
	config.DB.Order("id").Find(&items)
	return items
}

// GetTemplate 获取单个模板
func GetTemplate(id uint) (*model.ScanTemplate, error) {
	var t model.ScanTemplate
	if err := config.DB.First(&t, id).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// UpsertTemplate 新增/更新模板
func UpsertTemplate(t *model.ScanTemplate) error {
	if t.ID == 0 {
		return config.DB.Create(t).Error
	}
	return config.DB.Model(&model.ScanTemplate{}).Where("id = ?", t.ID).Updates(map[string]interface{}{
		"name": t.Name, "modules": t.Modules, "port_mode": t.PortMode, "ports": t.Ports,
		"thread": t.Thread, "timeout": t.Timeout, "no_ping": t.NoPing,
		"no_smart": t.NoSmart, "finger": t.Finger, "extra_args": t.ExtraArgs,
	}).Error
}

// DeleteTemplate 删除模板
func DeleteTemplate(id uint) error {
	return config.DB.Delete(&model.ScanTemplate{}, id).Error
}
