package llm

import (
	"encoding/json"

	"strings"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/fingerprint"
	"asset-platform/internal/model"
)

// Tool OpenAI Function Calling 工具定义
type Tool struct {
	Type     string       `json:"type"` // function
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

func param(props map[string]interface{}, required ...string) map[string]interface{} {
	if required == nil {
		required = []string{}
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func str(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": desc}
}

func num(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "integer", "description": desc}
}

func boolean(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "boolean", "description": desc}
}

// Tools 全部可用工具
func Tools() []Tool {
	t := func(name, desc string, params map[string]interface{}) Tool {
		return Tool{Type: "function", Function: ToolFunction{
			Name: name, Description: desc, Parameters: params,
		}}
	}

	return []Tool{
		// ===== 读取 =====
		t("list_assets", "查询资产列表。可过滤状态（pending/confirmed）、部门、负责人、系统名、IP 关键字",
			param(map[string]interface{}{
				"status":     str("状态：pending=待确认，confirmed=已确认"),
				"department": str("部门名，模糊匹配"),
				"owner":      str("负责人，模糊匹配"),
				"keyword":    str("IP 或系统名关键字"),
				"limit":      num("返回条数，默认 50，最大 200"),
				"offset":     num("偏移量"),
			})),

		t("get_asset_detail", "查询单个 IP 的完整信息：资产记录、所有端口/服务、页面快照历史",
			param(map[string]interface{}{
				"ip": str("目标 IP"),
			}, "ip")),

		t("list_services", "查询服务（端口级）列表。可按 IP、协议、是否有标题、指纹关键字过滤",
			param(map[string]interface{}{
				"ip":          str("IP 关键字"),
				"protocol":    str("协议：HTTP / HTTPS / SMB 等"),
				"hasTitle":    boolean("true=只返回有标题的，false=只返回无标题的"),
				"fingerprint": str("指纹关键字，如 nginx/HSTS"),
				"limit":       num("返回条数，默认 50"),
			})),

		t("list_tasks", "查询扫描任务列表及状态",
			param(map[string]interface{}{
				"status": str("状态：pending/running/paused/done/failed"),
				"limit":  num("返回条数，默认 30"),
			})),

		t("get_task_status", "查询单个任务的执行状态、进度、结果统计",
			param(map[string]interface{}{
				"taskId": num("任务 ID"),
			}, "taskId")),

		t("list_nodes", "查询扫描节点列表：在线状态、版本、当前任务",
			param(map[string]interface{}{})),

		t("list_disposal", "查询待处置资产池（扫描发现但未确认归属的资产）",
			param(map[string]interface{}{
				"tag":   str("标签：new=新发现，ledger=台账有，changed=页面变更"),
				"limit": num("返回条数，默认 50"),
			})),

		t("get_dashboard_stats", "查询首页统计：资产总数、待处置数、今日新增、任务状态分布",
			param(map[string]interface{}{})),

		t("get_compliance", "查询合规检查结果（端口开放、弱口令等风险项）",
			param(map[string]interface{}{
				"level": str("风险等级：high/medium/low"),
				"limit": num("返回条数，默认 50"),
			})),

		t("search_assets", "全局搜索：在资产、服务、任务中同时查找关键字",
			param(map[string]interface{}{
				"keyword": str("搜索关键字"),
			}, "keyword")),

		// ===== 操作 =====
		t("create_task", "创建扫描任务。目标为 IP/CIDR/范围，可设置定时",
			param(map[string]interface{}{
				"name":     str("任务名称"),
				"targets":  str("扫描目标，多个用换行或逗号分隔，如 10.0.0.1,10.0.1.0/24"),
				"portMode": str("端口模式：top100 / top1000 / all / custom"),
				"ports":    str("自定义端口，portMode=custom 时必填，如 80,443,8080-8090"),
				"cron":     str("定时表达式（可选），如 0 2 * * *"),
			}, "name", "targets")),

		t("control_task", "控制任务：run=立即执行，stop=停止，pause=暂停，resume=恢复，restart=重跑",
			param(map[string]interface{}{
				"taskId": num("任务 ID"),
				"action": str("动作：run / stop / pause / resume / restart"),
			}, "taskId", "action")),

		t("delete_task", "删除扫描任务（不会删除已发现的资产）",
			param(map[string]interface{}{
				"taskId": num("任务 ID"),
			}, "taskId")),

		t("confirm_asset", "确认资产归属：填写部门、负责人、系统名，状态变为 confirmed",
			param(map[string]interface{}{
				"ip":         str("资产 IP"),
				"department": str("部门"),
				"owner":      str("负责人"),
				"systemName": str("系统名称"),
			}, "ip")),

		t("update_asset", "更新资产信息（不改状态）",
			param(map[string]interface{}{
				"ip":         str("资产 IP"),
				"department": str("部门"),
				"owner":      str("负责人"),
				"systemName": str("系统名称"),
			}, "ip")),

		t("delete_asset", "删除资产及其所有服务记录（危险操作，需用户明确确认）",
			param(map[string]interface{}{
				"ip": str("资产 IP"),
			}, "ip")),

		t("batch_update_assets", "批量更新资产归属（按 IP 列表）",
			param(map[string]interface{}{
				"ips":        str("IP 列表，逗号分隔"),
				"department": str("部门"),
				"owner":      str("负责人"),
			}, "ips")),

		t("identify_fingerprint", "对指定 IP:端口 做三层指纹识别（修复 TLS 兼容 + 提取标题/产品）",
			param(map[string]interface{}{
				"ip":       str("目标 IP"),
				"port":     num("端口"),
				"protocol": str("协议：HTTP 或 HTTPS"),
			}, "ip", "port")),

		t("get_ledger_diff", "台账比对：找出台账有但未扫描到、扫描到但台账没有的资产",
			param(map[string]interface{}{
				"limit": num("返回条数，默认 50"),
			})),
	}
}

// Execute 执行工具调用，返回 JSON 字符串
func Execute(name string, argsJSON string) string {
	var args map[string]interface{}
	if argsJSON != "" {
		_ = json.Unmarshal([]byte(argsJSON), &args)
	}
	s := func(k string) string {
		if v, ok := args[k]; ok {
			if str, ok := v.(string); ok {
				return strings.TrimSpace(str)
			}
		}
		return ""
	}
	n := func(k string, def int) int {
		if v, ok := args[k]; ok {
			switch t := v.(type) {
			case float64:
				return int(t)
			case int:
				return t
			}
		}
		return def
	}
	limit := n("limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	switch name {
	case "list_assets":
		return listAssets(s("status"), s("department"), s("owner"), s("keyword"), limit, n("offset", 0))

	case "get_asset_detail":
		return getAssetDetail(s("ip"))

	case "list_services":
		return listServices(s("ip"), s("protocol"), args["hasTitle"], s("fingerprint"), limit)

	case "list_tasks":
		return listTasks(s("status"), limit)

	case "get_task_status":
		return getTaskStatus(n("taskId", 0))

	case "list_nodes":
		return listNodes()

	case "list_disposal":
		return listDisposal(s("tag"), limit)

	case "get_dashboard_stats":
		return dashboardStats()

	case "get_compliance":
		return compliance(s("level"), limit)

	case "search_assets":
		return searchAssets(s("keyword"))

	case "create_task":
		return createTask(s("name"), s("targets"), s("portMode"), s("ports"), s("cron"))

	case "control_task":
		return controlTask(n("taskId", 0), s("action"))

	case "delete_task":
		return deleteTask(n("taskId", 0))

	case "confirm_asset":
		return confirmAsset(s("ip"), s("department"), s("owner"), s("systemName"))

	case "update_asset":
		return updateAsset(s("ip"), s("department"), s("owner"), s("systemName"))

	case "delete_asset":
		return deleteAsset(s("ip"))

	case "batch_update_assets":
		return batchUpdateAssets(s("ips"), s("department"), s("owner"))

	case "identify_fingerprint":
		r := fingerprint.Identify(s("protocol"), s("ip"), n("port", 0))
		return jsonOut(map[string]interface{}{
			"ip": s("ip"), "port": n("port", 0),
			"title": r.Title, "fingerprint": r.Fingerprint,
			"metaDesc": r.MetaDesc, "framework": r.Framework,
			"server": r.Server, "layer": r.Layer, "confidence": r.Confidence,
		})

	case "get_ledger_diff":
		return ledgerDiff(limit)
	}
	return jsonOut(map[string]string{"error": "未知工具: " + name})
}

func jsonOut(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---- 实现 ----

func listAssets(status, department, owner, keyword string, limit, offset int) string {
	q := config.DB.Model(&model.Asset{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if department != "" {
		q = q.Where("department ILIKE ?", "%"+department+"%")
	}
	if owner != "" {
		q = q.Where("owner ILIKE ?", "%"+owner+"%")
	}
	if keyword != "" {
		q = q.Where("ip::text ILIKE ? OR system_name ILIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	var total int64
	q.Count(&total)
	var items []model.Asset
	q.Order("ip").Limit(limit).Offset(offset).Find(&items)
	return jsonOut(map[string]interface{}{"total": total, "count": len(items), "items": items})
}

func getAssetDetail(ip string) string {
	var asset model.Asset
	if err := config.DB.Where("ip = ?", ip).First(&asset).Error; err != nil {
		return jsonOut(map[string]string{"error": "资产不存在: " + ip})
	}
	var services []model.Service
	config.DB.Where("ip = ?", ip).Order("port").Find(&services)
	return jsonOut(map[string]interface{}{
		"asset": asset, "services": services, "serviceCount": len(services),
	})
}

func listServices(ip, protocol string, hasTitle interface{}, fpKeyword string, limit int) string {
	q := config.DB.Model(&model.Service{})
	if ip != "" {
		q = q.Where("ip::text ILIKE ?", "%"+ip+"%")
	}
	if protocol != "" {
		q = q.Where("protocol ILIKE ?", protocol+"%")
	}
	if b, ok := hasTitle.(bool); ok {
		if b {
			q = q.Where("title != '' AND title IS NOT NULL")
		} else {
			q = q.Where("(title = '' OR title IS NULL)")
		}
	}
	if fpKeyword != "" {
		q = q.Where("fingerprint ILIKE ?", "%"+fpKeyword+"%")
	}
	var total int64
	q.Count(&total)
	var items []model.Service
	q.Order("ip, port").Limit(limit).Find(&items)
	return jsonOut(map[string]interface{}{"total": total, "count": len(items), "items": items})
}

func listTasks(status string, limit int) string {
	q := config.DB.Model(&model.ScanTask{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var items []model.ScanTask
	q.Order("id DESC").Limit(limit).Find(&items)
	return jsonOut(map[string]interface{}{"count": len(items), "items": items})
}

func getTaskStatus(taskID int) string {
	if taskID == 0 {
		return jsonOut(map[string]string{"error": "缺少 taskId"})
	}
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return jsonOut(map[string]string{"error": "任务不存在"})
	}
	var runCount int64
	config.DB.Model(&model.TaskRun{}).Where("task_id = ?", taskID).Count(&runCount)
	return jsonOut(map[string]interface{}{"task": task, "runCount": runCount})
}

func listNodes() string {
	var items []model.Node
	config.DB.Order("id").Find(&items)
	return jsonOut(map[string]interface{}{"count": len(items), "items": items})
}

func listDisposal(tag string, limit int) string {
	q := config.DB.Model(&model.Asset{}).Where("status = ?", "pending")
	if tag == "changed" {
		q = q.Where("content_changed = true")
	}
	var items []model.Asset
	q.Order("updated_at DESC").Limit(limit).Find(&items)
	return jsonOut(map[string]interface{}{"count": len(items), "items": items})
}

func dashboardStats() string {
	var total, pending, confirmed, todayNew int64
	config.DB.Model(&model.Asset{}).Count(&total)
	config.DB.Model(&model.Asset{}).Where("status = ?", "pending").Count(&pending)
	config.DB.Model(&model.Asset{}).Where("status = ?", "confirmed").Count(&confirmed)
	config.DB.Model(&model.Asset{}).Where("created_at >= ?", time.Now().Truncate(24*time.Hour)).Count(&todayNew)

	var taskRunning, taskTotal int64
	config.DB.Model(&model.ScanTask{}).Count(&taskTotal)
	config.DB.Model(&model.ScanTask{}).Where("status = ?", "running").Count(&taskRunning)

	return jsonOut(map[string]interface{}{
		"assetTotal": total, "assetPending": pending, "assetConfirmed": confirmed,
		"todayNew": todayNew, "taskTotal": taskTotal, "taskRunning": taskRunning,
	})
}

func compliance(status string, limit int) string {
	var items []model.ComplianceResult
	q := config.DB.Model(&model.ComplianceResult{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	q.Order("id DESC").Limit(limit).Find(&items)
	return jsonOut(map[string]interface{}{"count": len(items), "items": items})
}

func searchAssets(keyword string) string {
	if keyword == "" {
		return jsonOut(map[string]string{"error": "缺少 keyword"})
	}
	kw := "%" + keyword + "%"
	var assets []model.Asset
	config.DB.Where("ip::text ILIKE ? OR system_name ILIKE ? OR owner ILIKE ?", kw, kw, kw).Limit(20).Find(&assets)

	var services []model.Service
	config.DB.Where("ip::text ILIKE ? OR title ILIKE ? OR fingerprint ILIKE ?", kw, kw, kw).Limit(20).Find(&services)

	return jsonOut(map[string]interface{}{
		"assets": assets, "assetCount": len(assets),
		"services": services, "serviceCount": len(services),
	})
}

func createTask(name, targets, portMode, ports, cron string) string {
	if name == "" || targets == "" {
		return jsonOut(map[string]string{"error": "需要 name 和 targets"})
	}
	if portMode == "" {
		portMode = "top100"
	}
	task := model.ScanTask{
		Name: name, IPRanges: targets, PortMode: portMode, Ports: ports,
		Cron: cron, Status: "pending",
	}
	if err := config.DB.Create(&task).Error; err != nil {
		return jsonOut(map[string]string{"error": err.Error()})
	}
	return jsonOut(map[string]interface{}{"ok": true, "taskId": task.ID, "message": "任务已创建"})
}

func controlTask(taskID int, action string) string {
	if taskID == 0 {
		return jsonOut(map[string]string{"error": "缺少 taskId"})
	}
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return jsonOut(map[string]string{"error": "任务不存在"})
	}
	switch action {
	case "run":
		task.Status = "pending"
	case "stop":
		task.Status = "stopped"
	case "pause":
		task.Status = "paused"
	case "resume":
		task.Status = "pending"
	case "restart":
		task.Status = "pending"
	default:
		return jsonOut(map[string]string{"error": "未知动作: " + action})
	}
	config.DB.Save(&task)
	return jsonOut(map[string]interface{}{"ok": true, "taskId": taskID, "status": task.Status})
}

func deleteTask(taskID int) string {
	if taskID == 0 {
		return jsonOut(map[string]string{"error": "缺少 taskId"})
	}
	if err := config.DB.Delete(&model.ScanTask{}, taskID).Error; err != nil {
		return jsonOut(map[string]string{"error": err.Error()})
	}
	return jsonOut(map[string]interface{}{"ok": true, "message": "任务已删除"})
}

func confirmAsset(ip, department, owner, systemName string) string {
	updates := map[string]interface{}{"status": "confirmed"}
	if department != "" {
		updates["department"] = department
	}
	if owner != "" {
		updates["owner"] = owner
	}
	if systemName != "" {
		updates["system_name"] = systemName
	}
	res := config.DB.Model(&model.Asset{}).Where("ip = ?", ip).Updates(updates)
	if res.Error != nil {
		return jsonOut(map[string]string{"error": res.Error.Error()})
	}
	if res.RowsAffected == 0 {
		return jsonOut(map[string]string{"error": "资产不存在: " + ip})
	}
	return jsonOut(map[string]interface{}{"ok": true, "ip": ip, "message": "已确认"})
}

func updateAsset(ip, department, owner, systemName string) string {
	updates := map[string]interface{}{}
	if department != "" {
		updates["department"] = department
	}
	if owner != "" {
		updates["owner"] = owner
	}
	if systemName != "" {
		updates["system_name"] = systemName
	}
	if len(updates) == 0 {
		return jsonOut(map[string]string{"error": "没有要更新的字段"})
	}
	res := config.DB.Model(&model.Asset{}).Where("ip = ?", ip).Updates(updates)
	if res.RowsAffected == 0 {
		return jsonOut(map[string]string{"error": "资产不存在: " + ip})
	}
	return jsonOut(map[string]interface{}{"ok": true, "ip": ip})
}

func deleteAsset(ip string) string {
	tx := config.DB.Begin()
	tx.Where("ip = ?", ip).Delete(&model.Service{})
	res := tx.Where("ip = ?", ip).Delete(&model.Asset{})
	if res.RowsAffected == 0 {
		tx.Rollback()
		return jsonOut(map[string]string{"error": "资产不存在: " + ip})
	}
	tx.Commit()
	return jsonOut(map[string]interface{}{"ok": true, "ip": ip, "message": "已删除资产及关联服务"})
}

func batchUpdateAssets(ips, department, owner string) string {
	if ips == "" {
		return jsonOut(map[string]string{"error": "缺少 ips"})
	}
	list := strings.FieldsFunc(ips, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' })
	updates := map[string]interface{}{}
	if department != "" {
		updates["department"] = department
	}
	if owner != "" {
		updates["owner"] = owner
	}
	if len(updates) == 0 {
		return jsonOut(map[string]string{"error": "没有要更新的字段"})
	}
	res := config.DB.Model(&model.Asset{}).Where("ip IN ?", list).Updates(updates)
	return jsonOut(map[string]interface{}{"ok": true, "affected": res.RowsAffected, "total": len(list)})
}

func ledgerDiff(limit int) string {
	// 台账来源 = ledger，扫描来源 = scan
	var ledgerOnly []model.Asset
	config.DB.Where("source = ? AND status != ?", "ledger", "confirmed").Limit(limit).Find(&ledgerOnly)

	var scanOnly []model.Asset
	config.DB.Where("source = ?", "scan").Limit(limit).Find(&scanOnly)

	return jsonOut(map[string]interface{}{
		"ledgerNotScanned": ledgerOnly,
		"scanNotInLedger":  scanOnly,
	})
}

// SystemPrompt 系统提示词
func SystemPrompt() string {
	return `你是「资产测绘平台」的智能助手，可以查询和操作平台全部数据。

【数据模型】
- assets（资产）：IP、系统名、部门、负责人、来源(scan/ledger)、状态(pending待确认/confirmed已确认)、页面变更标记
- services（服务）：IP、端口、协议、标题、指纹、状态码、内容长度
- scan_tasks（任务）：名称、目标范围、端口模式、cron、状态
- nodes（节点）：扫描节点，在线状态、版本

【业务规则】
1. 扫描发现的资产默认 status=pending，进入「资产处置」待人工确认
2. 人工填部门/负责人/系统名后 status=confirmed
3. 台账导入的资产 source=ledger，扫描发现 source=scan
4. 页面内容变化会标记 content_changed=true，需重新确认

【回答要求】
- 中文回答，简洁直接
- 查数据用工具，不要编造
- 涉及删除等危险操作，先说明影响范围，等用户确认
- 数据量大时先给汇总，再问是否需要明细`
}
