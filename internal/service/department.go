package service

import (
	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// ListDepartments 部门列表（平铺，前端组装树）
func ListDepartments() []model.Department {
	var items []model.Department
	config.DB.Order("sort, id").Find(&items)
	return items
}

// CreateDepartment 新增部门
func CreateDepartment(name string, parentID *uint, owners string) error {
	return config.DB.Create(&model.Department{Name: name, ParentID: parentID, Owners: owners}).Error
}

// UpdateDepartment 更新部门
func UpdateDepartment(id uint, name string, parentID *uint, owners string) error {
	return config.DB.Model(&model.Department{}).Where("id = ?", id).
		Updates(map[string]interface{}{"name": name, "parent_id": parentID, "owners": owners}).Error
}

// DeleteDepartment 删除部门（子部门上提到根）
func DeleteDepartment(id uint) error {
	config.DB.Model(&model.Department{}).Where("parent_id = ?", id).Update("parent_id", nil)
	return config.DB.Delete(&model.Department{}, id).Error
}

// ConfirmAsset 确认资产（填系统名/负责人/部门，正式入库）
func ConfirmAsset(id uint, systemName, owner, department string) error {
	return config.DB.Model(&model.Asset{}).Where("id = ?", id).Updates(map[string]interface{}{
		"system_name": systemName, "owner": owner, "department": department,
		"status": "confirmed", "source": "ledger",
	}).Error
}
