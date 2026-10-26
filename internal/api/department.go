package api

import (
	"sort"
	"strings"
	"net/http"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"strconv"

	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ListDepartments 部门列表
func ListDepartments(c *gin.Context) {
	c.JSON(http.StatusOK, service.ListDepartments())
}

// CreateDepartment 新增部门
func CreateDepartment(c *gin.Context) {
	var req struct {
		Name     string `json:"name"`
		ParentID *uint  `json:"parentId"`
		Owners   string `json:"owners"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "部门名必填"})
		return
	}
	if err := service.CreateDepartment(req.Name, req.ParentID, req.Owners); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已添加"})
}

// DeleteDepartment 删除部门
func DeleteDepartment(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteDepartment(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ConfirmAsset 确认待确认资产（部门/负责人必填）
func ConfirmAsset(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		SystemName string `json:"systemName"`
		Owner      string `json:"owner"`
		Department string `json:"department"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Department == "" || req.Owner == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须选择部门和负责人"})
		return
	}
	if err := service.ConfirmAsset(uint(id), req.SystemName, req.Owner, req.Department); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已确认入库"})
}

// splitOwners 拆分负责人（支持逗号/顿号/分号分隔，台账一格可能有多个人）
func splitOwners(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '、' || r == ';' || r == '；' || r == '/'
	}) {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// SyncDepartmentsFromAssets 从资产总览中提取部门/负责人，自动创建组织架构
// 一个部门可以有多个负责人（台账中"负责人"列可能写多个人）
func SyncDepartmentsFromAssets(c *gin.Context) {
	// 提取 department + owner 对
	var rows []struct {
		Department string
		Owner      string
	}
	config.DB.Model(&model.Asset{}).
		Where("department IS NOT NULL AND department <> ''").
		Select("department, owner").Scan(&rows)

	deptOwners := map[string]map[string]bool{}
	deptList := []string{}
	for _, r := range rows {
		if _, ok := deptOwners[r.Department]; !ok {
			deptOwners[r.Department] = map[string]bool{}
			deptList = append(deptList, r.Department)
		}
		for _, o := range splitOwners(r.Owner) {
			deptOwners[r.Department][o] = true
		}
	}

	created, updated := 0, 0
	for _, d := range deptList {
		owners := []string{}
		for o := range deptOwners[d] {
			owners = append(owners, o)
		}
		sort.Strings(owners)
		ownersStr := strings.Join(owners, ",")

		var dept model.Department
		if err := config.DB.Where("name = ?", d).First(&dept).Error; err != nil {
			config.DB.Create(&model.Department{Name: d, Owners: ownersStr})
			created++
		} else if dept.Owners != ownersStr {
			config.DB.Model(&dept).Update("owners", ownersStr)
			updated++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"message":     "已同步",
		"departments": len(deptList),
		"created":     created,
		"updated":     updated,
	})
}

// MergeDepartments 合并多个部门到目标部门（资产的 department 一起改）
func MergeDepartments(c *gin.Context) {
	var req struct {
		SourceNames []string `json:"sourceNames"`
		TargetName  string   `json:"targetName"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.SourceNames) == 0 || req.TargetName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	// 目标部门不存在则创建
	var cnt int64
	config.DB.Model(&model.Department{}).Where("name = ?", req.TargetName).Count(&cnt)
	if cnt == 0 {
		config.DB.Create(&model.Department{Name: req.TargetName})
	}
	// 资产的 department 一起改
	config.DB.Model(&model.Asset{}).Where("department IN ?", req.SourceNames).
		Update("department", req.TargetName)
	// 删除源部门
	config.DB.Where("name IN ?", req.SourceNames).Delete(&model.Department{})
	c.JSON(http.StatusOK, gin.H{"message": "已合并"})
}

// SplitDepartment 拆分部门：把源部门的负责人分到多个新部门，源部门删除
func SplitDepartment(c *gin.Context) {
	var req struct {
		SourceName string `json:"sourceName"`
		Targets    []struct {
			Name    string   `json:"name"`
			Owners  []string `json:"owners"`
		} `json:"targets"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.SourceName == "" || len(req.Targets) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	for _, t := range req.Targets {
		// 创建新部门
		var cnt int64
		config.DB.Model(&model.Department{}).Where("name = ?", t.Name).Count(&cnt)
		if cnt == 0 {
			config.DB.Create(&model.Department{Name: t.Name})
		}
		// 把这些负责人的资产改到新部门
		if len(t.Owners) > 0 {
			config.DB.Model(&model.Asset{}).
				Where("department = ? AND owner IN ?", req.SourceName, t.Owners).
				Update("department", t.Name)
		}
	}
	// 源部门下未分配的负责人资产：按需求"未被选中的负责人会被删除"
	// 这里把它们的 department 置空（资产保留，部门归属清除）
	config.DB.Model(&model.Asset{}).Where("department = ?", req.SourceName).
		Update("department", "")
	// 删除源部门
	config.DB.Where("name = ?", req.SourceName).Delete(&model.Department{})
	c.JSON(http.StatusOK, gin.H{"message": "已拆分"})
}

// UpdateDepartment 编辑部门（名称/负责人/上级）
func UpdateDepartment(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Name     string `json:"name"`
		Owners   string `json:"owners"`
		ParentID *uint  `json:"parentId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var dept model.Department
	if err := config.DB.First(&dept, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "部门不存在"})
		return
	}
	// 重名检查（排除自己）
	var cnt int64
	config.DB.Model(&model.Department{}).Where("name = ? AND id <> ?", req.Name, id).Count(&cnt)
	if cnt > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "部门名称已存在"})
		return
	}

	oldName := dept.Name
	config.DB.Model(&dept).Updates(map[string]interface{}{
		"name": req.Name, "owners": req.Owners, "parent_id": req.ParentID,
	})
	// 改名时同步资产的 department
	if oldName != req.Name {
		config.DB.Model(&model.Asset{}).Where("department = ?", oldName).Update("department", req.Name)
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}
