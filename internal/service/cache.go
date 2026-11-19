package service

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// 缓存清理：随任务产生的中间数据（会话、快照、队列残留、日志、用量）会持续增长，
// 这里统一给出「占用有多大 / 清了会少什么」，由人决定清哪一类、保留多久。
// 口径：只碰"可重建或纯历史"的表；资产、服务、任务定义、用户等业务数据一律不动。

// CacheItem 一类缓存
type CacheItem struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Desc  string `json:"desc"`
	Rows  int64  `json:"rows"`
	Bytes int64  `json:"bytes"`
	// History=true 表示清掉会丢历史（用量统计），界面上单独提示
	History bool `json:"history"`
	// Protected=true 表示核心数据，只展示占用、不提供清理（如扫描快照）
	Protected bool `json:"protected"`
	// Note 清理时附带的动作说明
	Note string `json:"note,omitempty"`
}

// CacheStats 缓存总体占用
type CacheStats struct {
	DBTotalBytes int64       `json:"dbTotalBytes"` // 整个库大小
	DBDeadTuples int64       `json:"dbDeadTuples"` // 待回收的死元组行数（VACUUM 可收回）
	Items        []CacheItem `json:"items"`
	// 可清理合计（所有类别相加，仅作参考：保留期不同实际会少）
	CleanableBytes int64 `json:"cleanableBytes"`
	CleanableRows  int64 `json:"cleanableRows"`
	// 运行中任务的 ID，清理时自动跳过它们的日志/单元
	RunningTasks []uint `json:"runningTasks"`
}

// cacheDef 一类缓存的表与时间列
type cacheDef struct {
	Key     string
	Label   string
	Desc    string
	Table   string
	TimeCol string
	Where   string // 附加过滤（清理与统计共用）
	History   bool
	Protected bool
	Note      string
}

var cacheDefs = []cacheDef{
	{
		Key: "ai_conv", Label: "AI 会话记录（含完整报文）",
		Desc: "每次服务识别/问答一条会话，正文带完整请求与响应，是增长最快的一类",
		Table: "llm_conversations", TimeCol: "updated_at",
	},
	{
		Key: "ai_msg", Label: "AI 会话消息正文",
		Desc: "会话里的一问一答，单条可达数十 KB（完整报文所致）",
		Table: "llm_messages", TimeCol: "created_at",
	},
	{
		Key: "scan_snapshots", Label: "扫描快照（IP 历史）",
		Desc: "每次任务 × 每个 IP 一条，支撑资产变化趋势与 IP 历史；核心数据，不提供清理",
		Table: "scan_snapshots", TimeCol: "scanned_at", History: true, Protected: true,
		Note: "核心数据：只统计占用，不可清理",
	},
	{
		Key: "task_units", Label: "任务队列残留单元",
		Desc: "IP 租约队列的工作单元，任务结束后即无用（只清 done/failed_unit）",
		Table: "task_units", TimeCol: "updated_at",
		Where: "status IN ('done','failed_unit')",
	},
	{
		Key: "task_logs", Label: "任务运行日志",
		Desc: "任务终端输出，运行中的任务自动跳过",
		Table: "task_logs", TimeCol: "created_at", Note: "运行中任务的日志不会被删除",
	},
	{
		Key: "llm_usages", Label: "AI 用量明细",
		Desc: "每次模型调用的 token / 耗时记录，用于成本统计",
		Table: "llm_usages", TimeCol: "created_at", History: true,
		Note: "清掉后用量统计只能从清理点之后重新累计",
	},
	{
		Key: "faults", Label: "故障记录",
		Desc: "系统自动生成的故障/告警记录（默认另有 30 天自动清理）",
		Table: "faults", TimeCol: "created_at",
	},
}

// CacheStat 统计当前各类缓存占用与数据库总量
func CacheStat() CacheStats {
	var st CacheStats
	if config.DB == nil {
		return st
	}
	config.DB.Raw(`SELECT pg_database_size(current_database())`).Scan(&st.DBTotalBytes)
	config.DB.Raw(`SELECT COALESCE(SUM(n_dead_tup),0) FROM pg_stat_user_tables`).Scan(&st.DBDeadTuples)

	running := runningTaskIDs()
	st.RunningTasks = running

	for _, d := range cacheDefs {
		rows, bytes := statTable(d.Table, d.TimeCol, d.Where, 0, running)
		st.Items = append(st.Items, CacheItem{
			Key: d.Key, Label: d.Label, Desc: d.Desc,
			Rows: rows, Bytes: bytes, History: d.History, Protected: d.Protected, Note: d.Note,
		})
		if !d.Protected {
			st.CleanableRows += rows
			st.CleanableBytes += bytes
		}
	}
	// 页面截图：记录行数 + 文件实际占用
	{
		var rows int64
		config.DB.Model(&model.PageSnapshot{}).Count(&rows)
		var bytes int64 = dirSize(SnapshotDir)
		st.Items = append(st.Items, CacheItem{
			Key: "page_shots", Label: "页面截图文件",
			Desc: "服务页面留档图（data/snapshots），删除记录同时删文件",
			Rows: rows, Bytes: bytes, History: true,
			Note: "清掉后服务详情页无历史截图为证",
		})
		st.CleanableRows += rows
		st.CleanableBytes += bytes
	}
	return st
}

// statTable 统计一张表的行数与实际内容字节数（按同口径过滤）
func statTable(table, timeCol, where string, days int, running []uint) (int64, int64) {
	if config.DB == nil {
		return 0, 0
	}
	q := fmt.Sprintf(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(t.*)),0) FROM %s t`, table)
	var conds []string
	var args []interface{}
	if where != "" {
		conds = append(conds, where)
	}
	if days > 0 && timeCol != "" {
		conds = append(conds, fmt.Sprintf("t.%s < ?", timeCol))
		args = append(args, time.Now().AddDate(0, 0, -days))
	}
	if table == "task_logs" && len(running) > 0 {
		conds = append(conds, "t.task_id NOT IN ?")
		args = append(args, running)
	}
	if len(conds) > 0 {
		q += " WHERE " + joinAnd(conds)
	}
	var rows, bytes int64
	if err := config.DB.Raw(q, args...).Row().Scan(&rows, &bytes); err != nil {
		log.Printf("[cache] 统计 %s 失败: %v", table, err)
		return 0, 0
	}
	return rows, bytes
}

func joinAnd(conds []string) string {
	out := ""
	for i, c := range conds {
		if i > 0 {
			out += " AND "
		}
		out += c
	}
	return out
}

func runningTaskIDs() []uint {
	if config.DB == nil {
		return nil
	}
	var ids []uint
	config.DB.Model(&model.ScanTask{}).
		Where("status IN ?", []string{"running", "paused"}).
		Pluck("id", &ids)
	return ids
}

// CleanResult 清理结果
type CleanResult struct {
	Deleted      map[string]int64 `json:"deleted"`
	FreedBytes   int64            `json:"freedBytes"`
	BeforeBytes  int64            `json:"beforeBytes"`
	AfterBytes   int64            `json:"afterBytes"`
	Compacted    bool             `json:"compacted"`
	Skipped      []string         `json:"skipped"` // 被保护、未清理的类别
	DurationMs   int64            `json:"durationMs"`
}

// CleanCaches 按类别清理。retentionDays>0 表示只删该天数之前的记录，0 表示全清。
// compact=true 时对涉及的整表做 VACUUM FULL，真正把磁盘还回去。
func CleanCaches(keys []string, retentionDays int, compact bool) CleanResult {
	res := CleanResult{Deleted: map[string]int64{}}
	if config.DB == nil || len(keys) == 0 {
		return res
	}
	started := time.Now()
	config.DB.Raw(`SELECT pg_database_size(current_database())`).Scan(&res.BeforeBytes)
	diskBefore := dirSize(SnapshotDir)

	var cutoff *time.Time
	if retentionDays > 0 {
		t := time.Now().AddDate(0, 0, -retentionDays)
		cutoff = &t
	}
	running := runningTaskIDs()
	touch := map[string]bool{}

	for _, key := range keys {
		switch key {
		case "ai_conv":
			n1 := deleteByTime("llm_conversations", "updated_at", cutoff, "")
			// 会话删了，消息必须一起删，否则成孤儿（孤儿清理不按时间，天然安全）
			tx := config.DB.Exec("DELETE FROM llm_messages WHERE conversation_id NOT IN (SELECT id FROM llm_conversations)")
			res.Deleted[key] = n1 + tx.RowsAffected
			touch["llm_conversations"] = true
			touch["llm_messages"] = true
		case "ai_msg":
			res.Deleted[key] = deleteByTime("llm_messages", "created_at", cutoff, "")
			touch["llm_messages"] = true
		case "scan_snapshots":
			// 核心数据（IP 历史 / 资产变化趋势的支撑），永不清除；前端也不给按钮，
			// 这里再挡一次，防止直接调接口误删。
			res.Skipped = append(res.Skipped, key)
		case "task_units":
			res.Deleted[key] = deleteByTime("task_units", "updated_at", cutoff, "status IN ('done','failed_unit')")
			touch["task_units"] = true
		case "task_logs":
			where := ""
			if len(running) > 0 {
				where = fmt.Sprintf("task_id NOT IN (%s)", uintList(running))
			}
			res.Deleted[key] = deleteByTime("task_logs", "created_at", cutoff, where)
			touch["task_logs"] = true
		case "llm_usages":
			res.Deleted[key] = deleteByTime("llm_usages", "created_at", cutoff, "")
			touch["llm_usages"] = true
		case "faults":
			res.Deleted[key] = deleteByTime("faults", "created_at", cutoff, "")
			touch["faults"] = true
		case "page_shots":
			res.Deleted[key] = cleanPageShots(cutoff)
		}
	}

	// VACUUM 必须在事务外执行，逐表来
	for table := range touch {
		stmt := "VACUUM (ANALYZE) " + table
		if compact {
			stmt = "VACUUM (FULL, ANALYZE) " + table
		}
		if err := config.DB.Exec(stmt).Error; err != nil {
			log.Printf("[cache] %s 失败: %v", stmt, err)
		}
	}
	res.Compacted = compact
	config.DB.Raw(`SELECT pg_database_size(current_database())`).Scan(&res.AfterBytes)
	dbFreed := res.BeforeBytes - res.AfterBytes
	diskFreed := diskBefore - dirSize(SnapshotDir)
	res.FreedBytes = dbFreed + diskFreed
	if res.FreedBytes < 0 {
		res.FreedBytes = 0
	}
	res.DurationMs = time.Since(started).Milliseconds()
	return res
}

func deleteByTime(table, timeCol string, cutoff *time.Time, where string) int64 {
	q := "DELETE FROM " + table
	var conds []string
	var args []interface{}
	if cutoff != nil {
		conds = append(conds, timeCol+" < ?")
		args = append(args, *cutoff)
	}
	if where != "" {
		conds = append(conds, where)
	}
	if len(conds) > 0 {
		q += " WHERE " + joinAnd(conds)
	}
	tx := config.DB.Exec(q, args...)
	if tx.Error != nil {
		log.Printf("[cache] 清理 %s 失败: %v", table, tx.Error)
		return 0
	}
	return tx.RowsAffected
}

// cleanPageShots 删截图记录 + 对应文件；顺手清掉没有记录引用的孤儿文件
// （历史版本直接删记录不删文件，目录会一直涨）
func cleanPageShots(cutoff *time.Time) int64 {
	q := config.DB.Model(&model.PageSnapshot{})
	if cutoff != nil {
		q = q.Where("captured_at < ?", *cutoff)
	}
	var snaps []model.PageSnapshot
	q.Find(&snaps)
	var n int64
	for _, s := range snaps {
		if s.ImagePath != "" {
			_ = os.Remove(s.ImagePath)
		}
		if err := config.DB.Delete(&model.PageSnapshot{}, s.ID).Error; err == nil {
			n++
		}
	}
	// 孤儿文件：目录里有、库里没人引用
	keep := map[string]bool{}
	var all []model.PageSnapshot
	config.DB.Model(&model.PageSnapshot{}).Select("image_path").Find(&all)
	for _, a := range all {
		keep[filepath.Base(a.ImagePath)] = true
	}
	if ents, err := os.ReadDir(SnapshotDir); err == nil {
		for _, e := range ents {
			if e.IsDir() || keep[e.Name()] {
				continue
			}
			if err := os.Remove(filepath.Join(SnapshotDir, e.Name())); err == nil {
				n++
			}
		}
	}
	return n
}

// dirSize 目录下文件总字节（截图文件占用，单层即可）
func dirSize(dir string) int64 {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			n += info.Size()
		}
	}
	return n
}

func uintList(ids []uint) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprint(id)
	}
	return out
}
