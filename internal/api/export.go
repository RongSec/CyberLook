package api

import (
	"strings"
	"fmt"
	"net/http"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

// 各类型可导出列（key -> 中文表头）
var exportLabels = map[string]map[string]string{
	"assets": {"ip": "IP", "systemName": "关联业务", "owner": "负责人", "department": "隶属部门",
		"source": "来源", "status": "状态", "hasWeb": "是否有WEB", "createdAt": "创建时间",
		"port": "端口", "protocol": "协议", "service": "服务", "statusCode": "响应码",
		"title": "网站标题", "lastSeen": "最近发现"},
	"services": {"ip": "IP", "department": "部门", "owner": "负责人", "port": "端口",
		"protocol": "协议", "service": "服务", "statusCode": "响应码",
		"contentLength": "响应长度", "title": "网站标题", "fingerprint": "指纹",
		"banner": "Banner", "lastSeen": "最近发现"},
	"tasks": {"id": "ID", "name": "名称", "status": "状态", "progress": "进度",
		"cron": "定时", "agentTotal": "分配", "etaSeconds": "预计剩余",
		"createdAt": "创建时间", "reportPath": "报告文件"},
	"disposal": {"category": "类别", "ip": "IP", "webPort": "Web端口", "statusCode": "响应码",
		"contentLength": "响应长度", "title": "网站标题", "ledger": "台账登记",
		"fingerprint": "指纹", "createdAt": "发现时间"},
	"compliance": {"ip": "IP", "port": "端口", "service": "服务", "status": "状态",
		"ruleId": "规则ID", "detectedAt": "首次发现", "lastSeen": "最近发现", "fixedAt": "整改时间"},
	"snapshots": {"id": "ID", "url": "URL", "title": "标题", "capturedAt": "截图时间"},
	"departments": {"name": "部门", "owners": "预设负责人", "parentName": "上级部门"},
	"nodes":       {"name": "节点名", "ip": "IP", "status": "状态", "lastHeartbeat": "最后心跳"},
	"rules":       {"name": "名称", "port": "端口", "protocol": "协议", "enabled": "启用"},
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
func fmtVal(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
func mapSource(s string) string {
	if s == "ledger" {
		return "台账"
	}
	return "扫描"
}
func mapStatus(s string) string {
	if s == "confirmed" {
		return "已登记"
	}
	return "待确认"
}
func mapBool(b bool) string {
	if b {
		return "有"
	}
	return "无"
}

// fetchExportRows 按类型取导出数据
func fetchExportRows(typ string, f map[string]string, conds []SearchCond) []map[string]interface{} {
	out := []map[string]interface{}{}
	switch typ {
	case "assets":
		var list []model.Asset
		q := config.DB.Model(&model.Asset{})
		if v := f["ip"]; v != "" {
			q = q.Where("ip::text LIKE ?", "%"+v+"%")
		}
		if v := f["systemName"]; v != "" {
			q = q.Where("system_name ILIKE ?", "%"+v+"%")
		}
		if v := f["owner"]; v != "" {
			q = q.Where("owner ILIKE ?", "%"+v+"%")
		}
		if v := f["department"]; v != "" {
			q = q.Where("department = ?", v)
		}
		if v := f["source"]; v != "" {
			q = q.Where("source = ?", v)
		}
		if v := f["status"]; v != "" {
			q = q.Where("status = ?", v)
		}
		if f["hasWeb"] == "true" {
			q = q.Where("EXISTS (SELECT 1 FROM services s WHERE s.ip = assets.ip AND s.protocol ILIKE 'http%')")
		}
		if f["hasWeb"] == "false" {
			q = q.Where("NOT EXISTS (SELECT 1 FROM services s WHERE s.ip = assets.ip AND s.protocol ILIKE 'http%')")
		}
		q = ApplySearchConds(q, conds, map[string]string{
			"ip": "ip", "owner": "owner", "department": "department",
			"systemName": "system_name", "status": "status", "source": "source",
		})
		q.Order("ip").Find(&list)
		for _, a := range list {
			var svc model.Service
			hasSvc := config.DB.Where("ip = ? AND protocol ILIKE 'http%'", a.IP).
				Order("port").First(&svc).Error == nil
			row := map[string]interface{}{
				"ip": a.IP, "systemName": a.SystemName, "owner": a.Owner, "department": a.Department,
				"source": mapSource(a.Source), "status": mapStatus(a.Status), "hasWeb": mapBool(hasSvc),
				"createdAt": fmtVal(a.CreatedAt),
			}
			if hasSvc {
				row["port"] = svc.Port
				row["protocol"] = svc.Protocol
				row["service"] = svc.Service
				row["statusCode"] = svc.StatusCode
				row["title"] = svc.Title
				row["lastSeen"] = fmtTime(svc.LastSeen)
			}
			out = append(out, row)
		}
	case "services":
		var list []model.Service
		q := config.DB.Model(&model.Service{}).Where("protocol ILIKE 'http%'")
		if v := f["ip"]; v != "" {
			q = q.Where("ip::text LIKE ?", "%"+v+"%")
		}
		if v := f["port"]; v != "" {
			q = q.Where("port = ?", v)
		}
		if v := f["service"]; v != "" {
			q = q.Where("service ILIKE ?", "%"+v+"%")
		}
		rest := make([]SearchCond, 0, len(conds))
		for _, c := range conds {
			vals := c.values()
			if len(vals) == 0 {
				continue
			}
			v := vals[0]
			switch c.Field {
			case "department":
				q = q.Where("EXISTS (SELECT 1 FROM assets a WHERE a.ip = services.ip AND a.department::text ILIKE ?)", "%"+v+"%")
				continue
			case "owner":
				q = q.Where("EXISTS (SELECT 1 FROM assets a WHERE a.ip = services.ip AND a.owner::text ILIKE ?)", "%"+v+"%")
				continue
			}
			rest = append(rest, c)
		}
		q = ApplySearchConds(q, rest, map[string]string{
			"ip": "ip", "port": "port", "service": "service", "protocol": "protocol",
			"title": "title", "statusCode": "status_code", "banner": "banner",
			"contentLength": "content_length", "finger": "fingerprint", "lastSeen": "last_seen",
		})
		q.Order("ip").Order("port").Find(&list)
		// 预取 IP -> 归属（避免每行查库）
		ownerMap := map[string]model.Asset{}
		var assets []model.Asset
		config.DB.Select("ip", "owner", "department").Find(&assets)
		for _, a := range assets {
			ownerMap[a.IP] = a
		}
		for _, s := range list {
			a := ownerMap[s.IP]
			out = append(out, map[string]interface{}{
				"ip": s.IP, "department": a.Department, "owner": a.Owner,
				"port": s.Port, "protocol": s.Protocol, "service": s.Service,
				"statusCode": s.StatusCode, "contentLength": s.ContentLength,
				"title": s.Title, "fingerprint": s.Fingerprint, "banner": s.Banner,
				"lastSeen": fmtTime(s.LastSeen),
			})
		}
	case "tasks":
		var list []model.ScanTask
		config.DB.Order("id desc").Find(&list)
		for _, t := range list {
			// 进度：已扫端口 / 总端口；分配：分片数
			var done, total, agents int64
			var eta int64
			config.DB.Raw(`SELECT COALESCE(SUM(scanned),0), COALESCE(SUM(total),0), count(*),
				COALESCE(MAX(eta_seconds),0) FROM agent_tasks WHERE task_id = ?`, t.ID).
				Row().Scan(&done, &total, &agents, &eta)
			progress := ""
			if total > 0 {
				progress = fmt.Sprintf("%.0f%%", float64(done)/float64(total)*100)
			}
			out = append(out, map[string]interface{}{
				"id": t.ID, "name": t.Name, "status": t.Status, "progress": progress,
				"cron": t.Cron, "agentTotal": agents, "etaSeconds": eta,
				"createdAt": fmtVal(t.CreatedAt), "reportPath": t.ReportPath,
			})
		}
	case "disposal":
		dispConds := conds
		for _, it := range listDisposalItems() {
			if !matchDisposalConds(it, dispConds) {
				continue
			}
			out = append(out, map[string]interface{}{
				"category": it.Category, "ip": it.IP, "webPort": it.WebPort,
				"statusCode": it.StatusCode, "contentLength": it.ContentLength,
				"title": it.Title, "ledger": it.LedgerSystem,
				"fingerprint": it.Fingerprint, "createdAt": fmtVal(it.CreatedAt),
			})
		}
	case "compliance":
		var list []model.ComplianceResult
		q := config.DB.Model(&model.ComplianceResult{})
		if v := f["status"]; v != "" {
			q = q.Where("status = ?", v)
		}
		q.Order("id desc").Find(&list)
		for _, r := range list {
			st := "违规"
			if r.Status == "fixed" {
				st = "已整改"
			}
			// 服务名从 services 表按 IP+端口取
			var svc model.Service
			svcName := ""
			if config.DB.Where("ip = ? AND port = ?", r.IP, r.Port).First(&svc).Error == nil {
				svcName = svc.Service
			}
			out = append(out, map[string]interface{}{
				"ip": r.IP, "port": r.Port, "service": svcName, "status": st, "ruleId": r.RuleID,
				"detectedAt": fmtTime(r.DetectedAt), "lastSeen": fmtTime(r.LastSeen),
				"fixedAt": fmtTime(r.FixedAt),
			})
		}
	case "snapshots":
		var list []model.PageSnapshot
		config.DB.Order("captured_at desc").Find(&list)
		for _, s := range list {
			out = append(out, map[string]interface{}{
				"id": s.ID, "url": s.URL, "title": s.Title, "capturedAt": fmtVal(s.CapturedAt),
			})
		}
	case "departments":
		var list []model.Department
		config.DB.Order("sort, id").Find(&list)
		nameMap := map[uint]string{}
		for _, d := range list {
			nameMap[d.ID] = d.Name
		}
		for _, d := range list {
			parent := ""
			if d.ParentID != nil {
				parent = nameMap[*d.ParentID]
			}
			out = append(out, map[string]interface{}{"name": d.Name, "owners": d.Owners, "parentName": parent})
		}
	case "nodes":
		var list []model.Node
		config.DB.Order("id").Find(&list)
		for _, n := range list {
			out = append(out, map[string]interface{}{
				"name": n.Name, "ip": n.IP, "status": n.Status, "lastHeartbeat": fmtTime(n.LastHeartbeat),
			})
		}
	case "rules":
		var list []model.ComplianceRule
		config.DB.Order("port").Find(&list)
		for _, r := range list {
			en := "停用"
			if r.Enabled {
				en = "启用"
			}
			out = append(out, map[string]interface{}{
				"name": r.Name, "port": r.Port, "protocol": r.Protocol, "enabled": en,
			})
		}
	}
	return out
}

// ExportExcel 通用导出（自选列）
func ExportExcel(c *gin.Context) {
	var req struct {
		Type    string            `json:"type"`
		Columns []string          `json:"columns"`
		Filters map[string]string `json:"filters"`
		Conds   []SearchCond      `json:"conds"` // 当前 SPL 筛选条件
		Title   string            `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Type == "" || len(req.Columns) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请指定导出类型与列"})
		return
	}
	labels, ok := exportLabels[req.Type]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的导出类型"})
		return
	}
	rows := fetchExportRows(req.Type, req.Filters, req.Conds)

	f := excelize.NewFile()
	sheet := "数据"
	f.SetSheetName("Sheet1", sheet)
	// 表头
	for i, col := range req.Columns {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, labels[col])
	}
	// 数据
	for r, row := range rows {
		for cidx, col := range req.Columns {
			cell, _ := excelize.CoordinatesToCellName(cidx+1, r+2)
			f.SetCellValue(sheet, cell, safeCell(row[col]))
		}
	}
	name := req.Title
	if name == "" {
		name = req.Type
	}
	filename := fmt.Sprintf("%s_%s.xlsx", name, time.Now().Format("20060102_150405"))
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", "attachment; filename="+filename)
	if err := f.Write(c.Writer); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// matchDisposalConds 处置项按 SPL 条件过滤（复刻前端 matchCond）
func matchDisposalConds(it DisposalItem, conds []SearchCond) bool {
	for _, c := range conds {
		vals := c.values()
		if len(vals) == 0 {
			continue
		}
		var actual string
		switch c.Field {
		case "ip":
			actual = it.IP
		case "category":
			actual = it.Category
		case "webPort":
			actual = fmt.Sprint(it.WebPort)
		case "statusCode":
			actual = it.StatusCode
		case "contentLength":
			actual = fmt.Sprint(it.ContentLength)
		case "title":
			actual = it.Title
		case "fingerprint":
			actual = it.Fingerprint
		case "inLedger":
			if it.InLedger {
				actual = "是"
			} else {
				actual = "否"
			}
		case "lastSeen", "createdAt":
			t := it.CreatedAt
			if c.Field == "lastSeen" && it.LastSeen != nil {
				t = *it.LastSeen
			}
			actual = t.Format("2006-01-02 15:04:05")
		case "ledgerSystem":
			actual = it.LedgerSystem
		default:
			continue
		}

		// 多选：命中任一即可
		if len(vals) > 1 {
			hit := false
			for _, v := range vals {
				if strings.EqualFold(actual, v) {
					hit = true
					break
				}
			}
			if !hit {
				return false
			}
			continue
		}

		v := vals[0]
		a := strings.ToLower(actual)
		vv := strings.ToLower(strings.TrimSpace(v))
		ok := false
		switch c.Op {
		case "eq":
			ok = a == vv
		case "neq":
			ok = a != vv
		case "prefix":
			ok = strings.HasPrefix(a, vv)
		case "gt", "lt":
			var af, vf float64
			if _, err := fmt.Sscanf(actual, "%f", &af); err == nil {
				if _, err := fmt.Sscanf(v, "%f", &vf); err == nil {
					if c.Op == "gt" {
						ok = af > vf
					} else {
						ok = af < vf
					}
				}
			}
		default: // like
			ok = strings.Contains(a, vv)
		}
		if !ok {
			return false
		}
	}
	return true
}
