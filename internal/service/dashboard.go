package service

import (
	"sort"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// DashboardSummary 首页仪表盘汇总
type DashboardSummary struct {
	AssetTotal     int64           `json:"assetTotal"`
	AssetConfirmed int64           `json:"assetConfirmed"`
	AssetPending   int64           `json:"assetPending"`
	ImportedTotal  int64           `json:"importedTotal"`
	RunningTasks   int64           `json:"runningTasks"`
	ViolationTotal int64           `json:"violationTotal"`
	NewAlive       []string        `json:"newAlive"`
	NewService     []model.Service `json:"newService"`
}

// GetDashboardSummary 汇总统计 + 最近两次任务 diff（新存活/新应用）
func GetDashboardSummary() DashboardSummary {
	var s DashboardSummary
	// 三个资产指标统一按「可达 web 资产」统计，保证 总数 = 已登记 + 待确认
	// 非 web 主机（SSH/RDP/SNMP 等）不计入资产登记，只在资产总览页按 IP 展示
	config.DB.Raw(`SELECT count(DISTINCT s.ip) FROM services s
		WHERE (s.protocol ILIKE 'http%' OR s.service ILIKE '%http%')
		  AND (COALESCE(s.status_code,'') <> '' OR COALESCE(s.title,'') <> '')`).Scan(&s.AssetTotal)
	config.DB.Raw(`SELECT count(DISTINCT s.ip) FROM services s
		JOIN assets a ON a.ip = s.ip
		WHERE (s.protocol ILIKE 'http%' OR s.service ILIKE '%http%')
		  AND (COALESCE(s.status_code,'') <> '' OR COALESCE(s.title,'') <> '')
		  AND a.status = 'confirmed'`).Scan(&s.AssetConfirmed)
	// 与「资产处置」页完全同源：可达 web 服务中尚未处置的（按 IP 去重）
	// 之前用 status='pending' 会把非 web、不可达、已确认的台账资产都算进来，导致首页和列表对不上
	config.DB.Raw(`SELECT count(DISTINCT s.ip) FROM services s
		JOIN assets a ON a.ip = s.ip
		WHERE (s.protocol ILIKE 'http%' OR s.service ILIKE '%http%')
		  AND (COALESCE(s.status_code,'') <> '' OR COALESCE(s.title,'') <> '')
		  AND COALESCE(a.confirmed_title,'') = ''
		  AND COALESCE(a.confirmed_simhash,0) = 0
		  AND a.status <> 'confirmed'`).Scan(&s.AssetPending)
	// 导入资产 = 资产总览的总数（assets 表全部记录，含扫描发现与台账导入）
	config.DB.Model(&model.Asset{}).Count(&s.ImportedTotal)
	// 运行中任务
	config.DB.Model(&model.ScanTask{}).Where("status = 'running'").Count(&s.RunningTasks)
	config.DB.Model(&model.ComplianceResult{}).Where("status = 'violation'").Count(&s.ViolationTotal)

	var tasks []model.ScanTask
	config.DB.Where("status = 'done'").Order("id desc").Limit(2).Find(&tasks)
	if len(tasks) >= 1 {
		cur := tasks[0]
		// 必须初始化成空切片，nil 会被序列化成 null，前端 .length 会抛异常
		s.NewAlive = []string{}
		s.NewService = []model.Service{}
		s.NewAlive = diffNewAlive(cur, tasks)
		if cur.StartedAt != nil {
			config.DB.Where("first_seen >= ?", *cur.StartedAt).
				Order("first_seen desc").Limit(50).Find(&s.NewService)
		}
	}
	return s
}

// diffNewAlive 对比最近两次任务，返回新存活 IP
func diffNewAlive(cur model.ScanTask, tasks []model.ScanTask) []string {
	var curIPs []string
	config.DB.Model(&model.ScanSnapshot{}).
		Where("task_id = ? AND is_alive = true", cur.ID).Pluck("ip", &curIPs)
	if len(tasks) < 2 {
		return curIPs // 首次扫描，全部视为新存活
	}
	var prevIPs []string
	config.DB.Model(&model.ScanSnapshot{}).
		Where("task_id = ? AND is_alive = true", tasks[1].ID).Pluck("ip", &prevIPs)
	prevSet := map[string]bool{}
	for _, ip := range prevIPs {
		prevSet[ip] = true
	}
	var out []string
	for _, ip := range curIPs {
		if !prevSet[ip] {
			out = append(out, ip)
		}
	}
	return out
}

// GetIPHistory 某 IP 的历史态势（最近 N 次快照）
func GetIPHistory(ip string, limit int) []model.ScanSnapshot {
	if limit < 1 || limit > 100 {
		limit = 10
	}
	var snaps []model.ScanSnapshot
	config.DB.Where("ip = ?", ip).Order("scanned_at desc").Limit(limit).Find(&snaps)
	return snaps
}

// TrendPoint 趋势数据点（多指标）
type TrendPoint struct {
	Time            string `json:"time"`
	WebAssets       int    `json:"webAssets"`
	AssetConfirmed  int    `json:"assetConfirmed"`
	AssetPending    int    `json:"assetPending"`
	ImportedTotal   int    `json:"importedTotal"`
	Violations      int    `json:"violations"`
}

// GetServiceTrend 多指标趋势：5 条线与首页卡片同源
// 每点 = 当天结束时的「累计值」（不是当天新增）
// 最后一点 = 当前实时值，保证与卡片数字完全同步
func GetServiceTrend(limit int) []TrendPoint {
	if limit < 1 || limit > 100 {
		limit = 7
	}
	type dayRow struct {
		Day time.Time
		Cnt int64
	}
	// 按天取新增（用 first_seen / created_at 分桶）
	getCum := func(sql string) map[string]int64 {
		var rows []dayRow
		config.DB.Raw(sql, limit).Scan(&rows)
		// 从早到晚累加成累计
		sort.Slice(rows, func(i, j int) bool { return rows[i].Day.Before(rows[j].Day) })
		out := map[string]int64{}
		var acc int64
		for _, r := range rows {
			acc += r.Cnt
			out[r.Day.Format("01-02")] = acc
		}
		return out
	}

	webCum := getCum(`SELECT date_trunc('day', first_seen) AS day, count(DISTINCT ip) AS cnt
		FROM services WHERE (protocol ILIKE 'http%' OR service ILIKE '%http%')
		  AND (COALESCE(status_code,'') <> '' OR COALESCE(title,'') <> '')
		  AND first_seen IS NOT NULL GROUP BY 1 ORDER BY 1 DESC LIMIT ?`)
	impCum := getCum(`SELECT date_trunc('day', created_at) AS day, count(*) AS cnt
		FROM assets GROUP BY 1 ORDER BY 1 DESC LIMIT ?`)
	vioCum := getCum(`SELECT date_trunc('day', created_at) AS day, count(*) AS cnt
		FROM compliance_results WHERE status = 'violation' GROUP BY 1 ORDER BY 1 DESC LIMIT ?`)

	// 已登记/待确认依赖 assets 的 status，历史快照不全，用当前值填最后一点
	var cur WebStat
	config.DB.Raw(`SELECT count(DISTINCT s.ip) AS web_assets,
		count(DISTINCT CASE WHEN a.status = 'confirmed' THEN s.ip END) AS confirmed
		FROM services s JOIN assets a ON a.ip = s.ip
		WHERE (s.protocol ILIKE 'http%' OR s.service ILIKE '%http%')
		  AND (COALESCE(s.status_code,'') <> '' OR COALESCE(s.title,'') <> '')`).Scan(&cur)

	// 收集所有出现过的日期
	days := map[string]bool{}
	for k := range webCum {
		days[k] = true
	}
	for k := range impCum {
		days[k] = true
	}
	for k := range vioCum {
		days[k] = true
	}
	list := make([]string, 0, len(days))
	for k := range days {
		list = append(list, k)
	}
	sort.Strings(list)

	out := make([]TrendPoint, 0, len(list))
	for _, k := range list {
		p := TrendPoint{
			Time:           k,
			WebAssets:      int(webCum[k]),
			ImportedTotal:  int(impCum[k]),
			Violations:     int(vioCum[k]),
			AssetConfirmed: int(cur.Confirmed),
			AssetPending:   int(cur.WebAssets - cur.Confirmed),
		}
		out = append(out, p)
	}
	// 最后一点强制用当前实时值，保证与卡片数字一致
	if len(out) > 0 {
		var summary DashboardSummary
		summary = GetDashboardSummary()
		last := &out[len(out)-1]
		last.WebAssets = int(summary.AssetTotal)
		last.AssetConfirmed = int(summary.AssetConfirmed)
		last.AssetPending = int(summary.AssetPending)
		last.ImportedTotal = int(summary.ImportedTotal)
		last.Violations = int(summary.ViolationTotal)
	}
	return out
}

// WebStat 聚合用
type WebStat struct {
	WebAssets int64
	Confirmed int64
}
