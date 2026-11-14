package service

// ===== IP 租约队列（v0.0.27 下发架构升级：分片制 → 单 IP 租约队列） =====
//
// 动机：旧分片制（batch_size 领批 + 服务端 alivePool 内存待扫池 + 断点文件）
// 状态散落在内存/文件/DB 三处，节点故障回收、进度分母、重入队互相纠缠，
// 历史上出过「两条流水线并行跑」「任务卡 99%」一类难复现竞态。
//
// 队列制把所有待扫状态收敛进 task_units 一张表：
//   - 下发时把目标展开为逐 IP 单元（probes_total=端口数，进度分母创建即定）；
//   - 执行方（agent 会话或本机路径）用 ClaimUnit 原子领取
//     （SELECT FOR UPDATE SKIP LOCKED，Postgres 原生 work-stealing，无并发双领）；
//   - 租约 60s，随心跳续租；节点崩溃 → reaper 打回 pending（attempts≥3 隔离 failed_unit）；
//   - 进度/ETA 全部由单元聚合（见 tasketa.go）。
//
// 回退开关 USE_IP_QUEUE=false：新任务走旧分片制（dispatch.go 流水线原样保留一个
// 版本周期）。开关只在下发时读取并固化到 scan_tasks.queue_mode，翻开关不影响在途任务。
//
// 队列模式不做服务端 ICMP 预探测：connect 扫描本身就是存活判据（死 IP 的 SYN
// 超时上限即单元成本），预探测那一层（alivePool/断点文件）整体退役。

import (
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/scanner"
	"asset-platform/internal/target"
	"asset-platform/internal/tasklog"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 队列参数
const (
	// LeaseTTL 单元租约时长。agent 心跳每 2s 续租一次，60s = 30 拍容错窗口；
	// reaper 每 10s 扫一轮 → 节点崩溃后最坏 70s 单元回池（验收 F5 预算）。
	LeaseTTL = 60 * time.Second
	// UnitMaxAttempts 领取次数上限：同一 IP 连续搞挂 3 个槽位（节点崩溃/回收）
	// 判 failed_unit 隔离，无限重试只会拖死任务收敛。
	UnitMaxAttempts = 3
)

// UseIPQueue 全局默认开关（env USE_IP_QUEUE；默认开，"false"/"0" 关闭回退分片制）。
func UseIPQueue() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("USE_IP_QUEUE")))
	return v != "false" && v != "0"
}

// ErrQueuePaused 任务处于暂停态：拒绝领取新单元（在扫单元照常跑完）。
var ErrQueuePaused = errors.New("任务已暂停，暂不可领取新单元")

// QueueUnit 一次领取的结果（单 IP 任务描述）。
type QueueUnit struct {
	IP          string `json:"ip"`
	ProbesTotal int64  `json:"probesTotal"`
	SessionID   uint   `json:"sessionId"` // 执行槽位 = agent_tasks.id（心跳/取消/日志的路由键）
	NodeID      uint   `json:"-"`
}

// QueueStats 队列聚合（进度/ETA/前端概览共用）。
type QueueStats struct {
	Pending     int64 `json:"pending"`
	Leased      int64 `json:"leased"`
	Done        int64 `json:"done"`
	FailedUnit  int64 `json:"failed"`
	ProbesDone  int64 `json:"probesDone"`
	ProbesTotal int64 `json:"probesTotal"`
	// Total 单元总数（= 实际要扫的 IP 数）。进度按单元算，不按探针算：
	// 探针口径下 94239 个 IP 各 100 端口 = 942 万探针，前几十秒永远是 0%。
	Total int64 `json:"total"`
	// Phase 阶段：probing=存活探测中 / scanning=扫IP中 / done=已完成
	Phase string `json:"phase"`
}

// TaskPortsOf 任务端口串（引擎口径：top100→空、all→1-65535）。
func TaskPortsOf(t *model.ScanTask) string {
	switch t.PortMode {
	case "top100":
		return ""
	case "all":
		return "1-65535"
	}
	return t.Ports
}

// PortCountOf 任务端口数真值（进度分母的单元系数）。解析失败按 Top100=100 兜底。
func PortCountOf(t *model.ScanTask) int {
	n, err := scanner.PortCount(TaskPortsOf(t))
	if err != nil || n <= 0 {
		return 100
	}
	return n
}

// InitUnits 把任务目标展开为逐 IP 单元（幂等：ON CONFLICT DO NOTHING，
// 服务重启/重复下发不产生脏行；已 done 单元不因重发而回滚）。
// 返回 (本次新建数, 单元总数, error)。
func InitUnits(taskID uint, ipRanges string, portCount int) (int, int, error) {
	if portCount <= 0 {
		return 0, 0, fmt.Errorf("端口数必须为正（当前 %d）", portCount)
	}
	ips := target.Resolve(parseTargets(ipRanges), ExcludeRangeStrings())
	return InitUnitsFromIPs(taskID, ips, portCount)
}

// InitUnitsFromIPs 按给定 IP 列表建单元（存活探测后只把存活 IP 交给它）。
func InitUnitsFromIPs(taskID uint, ips []string, portCount int) (int, int, error) {
	if portCount <= 0 {
		return 0, 0, fmt.Errorf("端口数必须为正（当前 %d）", portCount)
	}
	if len(ips) == 0 {
		return 0, 0, fmt.Errorf("目标展开后无有效 IP")
	}
	seen := make(map[string]bool, len(ips))
	rows := make([]model.TaskUnit, 0, len(ips))
	for _, ip := range ips {
		if seen[ip] {
			continue
		}
		seen[ip] = true
		rows = append(rows, model.TaskUnit{
			TaskID: taskID, IP: ip, Status: model.UnitPending, ProbesTotal: int64(portCount),
		})
	}
	if len(rows) == 0 {
		return 0, 0, fmt.Errorf("目标展开后无有效 IP")
	}
	// 分批插入（9 万 IP 单语句会撞参数/包大小上限）
	created := 0
	for i := 0; i < len(rows); i += 2000 {
		end := i + 2000
		if end > len(rows) {
			end = len(rows)
		}
		res := config.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(rows[i:end])
		if res.Error != nil {
			return created, 0, res.Error
		}
		created += int(res.RowsAffected)
	}
	// 单元总数按行回填端口数（模板/端口改动后的老 pending 行分母跟着校正；
	// done 行不动，避免进度倒退）
	config.DB.Model(&model.TaskUnit{}).
		Where("task_id = ? AND status IN ?", taskID, []string{model.UnitPending, model.UnitLeased}).
		Update("probes_total", portCount)
	var total int64
	config.DB.Model(&model.TaskUnit{}).Where("task_id = ?", taskID).Count(&total)
	return created, int(total), nil
}

// ResetUnits 重跑任务：全部单元回 pending（attempts 清零），租约清空。
func ResetUnits(taskID uint) {
	config.DB.Model(&model.TaskUnit{}).Where("task_id = ?", taskID).
		Updates(map[string]interface{}{
			"status": model.UnitPending, "node_id": 0, "session_id": 0,
			"lease_expire": nil, "attempts": 0, "probes_done": 0,
			"open_found": 0, "identified": 0, "pps": 0, "updated_at": time.Now(),
		})
}

// claimSkipLocked 原子领取一个 pending 单元。
// 两条语句必须同事务（gorm 的 Raw+Exec 在同一个 *gorm.DB 事务里）：
// CTE 行锁出了事务即释放——写成两条独立语句会给并发双领留窗口。
const claimSkipLocked = `
UPDATE task_units u
SET status = 'leased', node_id = ?, session_id = ?,
    lease_expire = ?, attempts = u.attempts + 1, updated_at = ?
WHERE u.id = (
    SELECT id FROM task_units
    WHERE task_id = ? AND status = 'pending'
    ORDER BY id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING ip, probes_total, session_id`

// claimSkipLockedBatch 批量原子领取：一次 SQL 领最多 ? 个 pending 单元。
// 与单条版本同语义（SKIP LOCKED 保证并发执行方之间不重叠），只是把「领一个」
// 换成「领一批」——agent 单次往返换来 K 个在途单元，端到端吞吐提升 K 倍，
// 往返开销与 UDP/TCP 握手次数都摊薄。LIMIT 由调用方钳制（1..MaxClaimBatch）。
const claimSkipLockedBatch = `
UPDATE task_units u
SET status = 'leased', node_id = ?, session_id = ?,
    lease_expire = ?, attempts = u.attempts + 1, updated_at = ?
WHERE u.id IN (
    SELECT id FROM task_units
    WHERE task_id = ? AND status = 'pending'
    ORDER BY id
    FOR UPDATE SKIP LOCKED
    LIMIT ?
)
RETURNING ip, probes_total, session_id`

// MaxClaimBatch 单次批量领取上限（防一个节点一口气锁走整段队列）。
const MaxClaimBatch = 32

type claimRow struct {
	IP          string `gorm:"column:ip"`
	ProbesTotal int64  `gorm:"column:probes_total"`
	SessionID   uint   `gorm:"column:session_id"`
}

// QueuePaused 队列领取闸门：执行层暂停（/control pause 内存态）或调度层暂停
// （status=paused）都算。claim/进度展示共用一个口径，避免两处打架。
func QueuePaused(task *model.ScanTask) bool {
	return task != nil && (task.Status == "paused" || TaskPausedNow(task.ID))
}

// ClaimUnit 领取一个待扫单元（nodeID=0=本机执行槽；sessionID 写入会话行）。
// 队列空/任务不可领 → (nil, nil)；暂停 → (nil, ErrQueuePaused)（调用方退避而不是退出）。
// 暂停语义（拍板）：完成当前单元后停止领取——已 leased 的单元不受影响，
// 引擎冻结由 cmd=pause 投影（limiter Pause）完成，两者正交。
func ClaimUnit(taskID, nodeID, sessionID uint) (*QueueUnit, error) {
	var task model.ScanTask
	if err := config.DB.Select("id", "status", "queue_mode").First(&task, taskID).Error; err != nil {
		return nil, err
	}
	if QueuePaused(&task) {
		return nil, ErrQueuePaused
	}
	if task.Status != "running" || !task.QueueMode {
		return nil, nil
	}
	now := time.Now()
	var row claimRow
	err := config.DB.Raw(claimSkipLocked, nodeID, sessionID, now.Add(LeaseTTL), now, taskID).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.IP == "" {
		return nil, nil
	}
	return &QueueUnit{IP: row.IP, ProbesTotal: row.ProbesTotal, SessionID: sessionID, NodeID: nodeID}, nil
}

// ClaimUnits 批量领取最多 n 个待扫单元（n 钳制到 1..MaxClaimBatch）。
// 语义与 ClaimUnit 完全一致：暂停 → ErrQueuePaused；不可领/领空 → 空切片。
// 单次 SQL 完成，SKIP LOCKED 保证多执行方并发下不重复。
func ClaimUnits(taskID, nodeID, sessionID uint, n int) ([]QueueUnit, error) {
	if n <= 0 {
		n = 1
	}
	if n > MaxClaimBatch {
		n = MaxClaimBatch
	}
	var task model.ScanTask
	if err := config.DB.Select("id", "status", "queue_mode").First(&task, taskID).Error; err != nil {
		return nil, err
	}
	if QueuePaused(&task) {
		return nil, ErrQueuePaused
	}
	if task.Status != "running" || !task.QueueMode {
		return nil, nil
	}
	now := time.Now()
	var rows []claimRow
	if err := config.DB.Raw(claimSkipLockedBatch, nodeID, sessionID, now.Add(LeaseTTL), now, taskID, n).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]QueueUnit, 0, len(rows))
	for _, r := range rows {
		if r.IP == "" {
			continue
		}
		out = append(out, QueueUnit{IP: r.IP, ProbesTotal: r.ProbesTotal, SessionID: sessionID, NodeID: nodeID})
	}
	return out, nil
}

// ClaimForNode agent 领取入口：校验队列任务归属后建/复用该节点的会话再原子领取。
// 返回 (nil, nil) = 该任务此刻对本节点不可领（非队列/非 running/不允许该节点/无 pending）；
// (nil, ErrQueuePaused) = 暂停停领。
func ClaimForNode(taskID, nodeID uint, nodeName string) (*QueueUnit, error) {
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return nil, nil
	}
	if !task.QueueMode || !taskAllowsNode(&task, nodeID) {
		return nil, nil
	}
	if QueuePaused(&task) {
		return nil, ErrQueuePaused
	}
	if task.Status != "running" {
		return nil, nil
	}
	sessID, err := EnsureSession(taskID, nodeID, nodeName)
	if err != nil {
		return nil, err
	}
	return ClaimUnit(taskID, nodeID, sessID)
}

// ClaimForNodeBatch 批量版 ClaimForNode：校验归属 + 建/复用会话后一次领最多 n 个单元。
// 语义与单个版本一致：(nil,nil) = 本节点此刻不可领；ErrQueuePaused = 暂停停领。
func ClaimForNodeBatch(taskID, nodeID uint, nodeName string, n int) ([]QueueUnit, error) {
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return nil, nil
	}
	if !task.QueueMode || !taskAllowsNode(&task, nodeID) {
		return nil, nil
	}
	if QueuePaused(&task) {
		return nil, ErrQueuePaused
	}
	if task.Status != "running" {
		return nil, nil
	}
	sessID, err := EnsureSession(taskID, nodeID, nodeName)
	if err != nil {
		return nil, err
	}
	return ClaimUnits(taskID, nodeID, sessID, n)
}

// PeekQueueClaimable 领取前置探测：任务是否还有可领单元（含过期租约即时回收）。
// 供 agent 轮询节流（避免每 3s 对已收敛任务空转 claim——claim 会 +attempts）。
func PeekQueueClaimable(taskID uint) bool {
	var n int64
	config.DB.Model(&model.TaskUnit{}).
		Where("task_id = ? AND status = ?", taskID, model.UnitPending).Count(&n)
	return n > 0
}

// UnitReport 队列心跳载荷（/progress 带 ip 时解析出的单元读数）。
type UnitReport struct {
	ProbesDone  int64
	ProbesTotal int64
	OpenFound   int64
	Identified  int64
	Pps         float64
	Status      string // ""|progress=续租+读数刷新；done=完成落账；failed=打回队列
}

// ReportUnit 队列心跳的统一入口（agent 经 /progress 调用；本机路径进程内直调）。
// 只作用于该槽位持有的 leased 单元；行不存在（被回收/已终态）静默忽略（旁路语义，
// 迟到的心跳不该干扰 reaper 的结果）。done/failed 同时给会话行累计探针数。
func ReportUnit(taskID, nodeID uint, ip string, r UnitReport) {
	if ip == "" || taskID == 0 {
		return
	}
	switch r.Status {
	case "done":
		CompleteUnit(taskID, nodeID, ip, r.ProbesDone, r.OpenFound, true)
		NoteSessionUnit(sessionOf(taskID, nodeID), r.ProbesTotal, r.ProbesDone, true)
		go CheckQueueDone(taskID)
	case "failed":
		CompleteUnit(taskID, nodeID, ip, r.ProbesDone, r.OpenFound, false)
		NoteSessionUnit(sessionOf(taskID, nodeID), r.ProbesTotal, r.ProbesDone, false)
		go CheckQueueDone(taskID)
	default: // progress：续租 + 实时读数
		now := time.Now()
		updates := map[string]interface{}{"lease_expire": now.Add(LeaseTTL), "updated_at": now}
		if r.ProbesDone >= 0 {
			// 单调保护：乱序/迟到的心跳不回卷进度
			updates["probes_done"] = gorm.Expr("CASE WHEN ? > probes_done THEN ? ELSE probes_done END", r.ProbesDone, r.ProbesDone)
		}
		if r.ProbesTotal > 0 {
			updates["probes_total"] = r.ProbesTotal
		}
		if r.Pps > 0 {
			updates["pps"] = r.Pps
		}
		if r.OpenFound > 0 {
			updates["open_found"] = r.OpenFound
		}
		if r.Identified > 0 {
			updates["identified"] = r.Identified
		}
		config.DB.Model(&model.TaskUnit{}).
			Where("task_id = ? AND ip = ? AND node_id = ? AND status = ?", taskID, ip, nodeID, model.UnitLeased).
			Updates(updates)
	}
}

// sessionOf 该槽位在该任务上的会话行 id（拿不到返回 0，NoteSessionUnit 会跳过）。
func sessionOf(taskID, nodeID uint) uint {
	var at model.AgentTask
	if err := config.DB.Where("task_id = ? AND node_id = ? AND batch_no = 0", taskID, nodeID).
		Order("id").First(&at).Error; err == nil {
		return at.ID
	}
	return 0
}

// RenewLease 续租该槽位的全部 leased 单元（心跳不带 ip 时的兜底）。
func RenewLease(taskID, nodeID uint, ip string) error {
	now := time.Now()
	q := config.DB.Model(&model.TaskUnit{}).
		Where("task_id = ? AND node_id = ? AND status = ?", taskID, nodeID, model.UnitLeased)
	if ip != "" {
		q = q.Where("ip = ?", ip)
	}
	return q.Update("lease_expire", now.Add(LeaseTTL)).Error
}

// CompleteUnit 单元终态。
// ok=true → done（probes_done/open_found 取最大值落账）；
// ok=false → 打回 pending（attempts 已在 claim 时 +1，≥上限即隔离 failed_unit）。
func CompleteUnit(taskID, nodeID uint, ip string, probesDone, openFound int64, ok bool) {
	now := time.Now()
	if ok {
		res := config.DB.Model(&model.TaskUnit{}).
			Where("task_id = ? AND ip = ? AND node_id = ? AND status = ?", taskID, ip, nodeID, model.UnitLeased).
			Updates(map[string]interface{}{
				"status":       model.UnitDone,
				"probes_done":  gorm.Expr("GREATEST(probes_done, ?)", probesDone),
				"open_found":   gorm.Expr("GREATEST(open_found, ?)", openFound),
				"lease_expire": nil, "updated_at": now,
			})
		if res.Error != nil {
			log.Printf("[队列] 单元 done 落库失败 task=%d ip=%s: %v", taskID, ip, res.Error)
		}
		return
	}
	RequeueOrIsolate(taskID, ip, now)
}

// RequeueOrIsolate 把单元打回队列：attempts<上限 → pending，否则 failed_unit。
func RequeueOrIsolate(taskID uint, ip string, now time.Time) {
	var u model.TaskUnit
	if err := config.DB.Where("task_id = ? AND ip = ?", taskID, ip).First(&u).Error; err != nil {
		return
	}
	if u.Status == model.UnitDone {
		return
	}
	st := model.UnitPending
	if u.Attempts >= UnitMaxAttempts {
		st = model.UnitFailedUnit
	}
	config.DB.Model(&model.TaskUnit{}).Where("id = ?", u.ID).Updates(map[string]interface{}{
		"status": st, "node_id": 0, "session_id": 0, "lease_expire": nil, "updated_at": now,
	})
	if st == model.UnitFailedUnit {
		tasklog.AppendTS(taskID, "[队列] IP %s 三次租约过期已隔离（failed_unit）", ip)
	}
}

// QueueStatsOf 队列聚合统计。
func QueueStatsOf(taskID uint) QueueStats {
	var s QueueStats
	type row struct {
		Status string `gorm:"column:status"`
		Cnt    int64  `gorm:"column:cnt"`
		PDone  int64  `gorm:"column:pdone"`
		PTotal int64  `gorm:"column:ptotal"`
	}
	var rows []row
	config.DB.Model(&model.TaskUnit{}).Where("task_id = ?", taskID).
		Select("status, COUNT(*) AS cnt, COALESCE(SUM(probes_done),0) AS pdone, COALESCE(SUM(probes_total),0) AS ptotal").
		Group("status").Scan(&rows)
	for _, r := range rows {
		switch r.Status {
		case model.UnitPending:
			s.Pending += r.Cnt
		case model.UnitLeased:
			s.Leased += r.Cnt
		case model.UnitDone:
			s.Done += r.Cnt
		case model.UnitFailedUnit:
			s.FailedUnit += r.Cnt
		}
		s.ProbesDone += r.PDone
		s.ProbesTotal += r.PTotal
		s.Total += r.Cnt
	}
	switch {
	case s.Total == 0:
		s.Phase = "probing"
	case s.Pending == 0 && s.Leased == 0:
		// 没有待领也没有在途：扫描已经收敛（终态是 done 还是 failed 由任务自身状态说）
		s.Phase = "done"
	default:
		s.Phase = "scanning"
	}
	return s
}

// ===== 租约 reaper =====

var reaperOnce sync.Once

// StartQueueReaper 启动租约过期回收 + 队列收敛巡检（server main 调一次）。
// tick=10s：节点崩溃后单元最迟 60+10=70s 回池（F5 验收预算）。
func StartQueueReaper() {
	reaperOnce.Do(func() {
		go func() {
			t := time.NewTicker(10 * time.Second)
			defer t.Stop()
			for range t.C {
				if n := ReapExpiredUnits(); n > 0 {
					log.Printf("[队列] 回收 %d 个租约过期单元", n)
				}
				SampleQueueETA() // 10s 滚动采样（ETA 的 EWMA 输入）
				checkQueueSettling()
			}
		}()
	})
}

// ReapExpiredUnits 租约过期单元批量回池/隔离；返回回收行数。
func ReapExpiredUnits() int {
	now := time.Now()
	r1 := config.DB.Model(&model.TaskUnit{}).
		Where("status = ? AND lease_expire IS NOT NULL AND lease_expire < ? AND attempts < ?",
			model.UnitLeased, now, UnitMaxAttempts).
		Updates(map[string]interface{}{
			"status": model.UnitPending, "node_id": 0, "session_id": 0,
			"lease_expire": nil, "updated_at": now,
		})
	r2 := config.DB.Model(&model.TaskUnit{}).
		Where("status = ? AND lease_expire IS NOT NULL AND lease_expire < ? AND attempts >= ?",
			model.UnitLeased, now, UnitMaxAttempts).
		Updates(map[string]interface{}{
			"status": model.UnitFailedUnit, "lease_expire": nil, "updated_at": now,
		})
	n := int(r1.RowsAffected + r2.RowsAffected)
	if r2.RowsAffected > 0 {
		var taskIDs []uint
		config.DB.Model(&model.TaskUnit{}).
			Where("status = ? AND updated_at >= ?", model.UnitFailedUnit, now.Add(-time.Minute)).
			Distinct().Pluck("task_id", &taskIDs)
		for _, tid := range taskIDs {
			var cnt int64
			config.DB.Model(&model.TaskUnit{}).
				Where("task_id = ? AND status = ?", tid, model.UnitFailedUnit).Count(&cnt)
			tasklog.AppendTS(tid, "[队列] 累计 %d 个 IP 三次租约过期已隔离（failed_unit），不再下发", cnt)
			// 隔离即可能触发最后收敛
			go CheckQueueDone(tid)
		}
	}
	return n
}

// checkQueueSettling 对在途队列任务做收敛判定（单元全部终态化时落 done）。
func checkQueueSettling() {
	var tasks []model.ScanTask
	config.DB.Where("queue_mode = ? AND status = ?", true, "running").Find(&tasks)
	for _, t := range tasks {
		CheckQueueDone(t.ID)
	}
}

// ===== 队列模式收敛判定 =====
//
// 判据：无 pending 且无「未过期 leased」→ 任务 done；failed_unit>0 时 error 记摘要。
// 与分片制 CheckTaskAllDone 并列，queue_mode 决定走哪条（一个版本周期后旧的退役）。

var queueNotifyOnce sync.Map // taskID -> true：done 通知只发一次

// CheckQueueDone 队列任务收敛判定（claim 落空 / finish / reaper 均可调，幂等）。
// 返回 true = 本轮判定收敛。
func CheckQueueDone(taskID uint) bool {
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return false
	}
	if !task.QueueMode || task.Status != "running" {
		return false
	}
	now := time.Now()
	// 过期 leased 顺手回收（判定路径自带，收敛零延迟）
	config.DB.Model(&model.TaskUnit{}).
		Where("status = ? AND task_id = ? AND lease_expire IS NOT NULL AND lease_expire < ?",
			model.UnitLeased, taskID, now).
		Updates(map[string]interface{}{
			"status": model.UnitPending, "node_id": 0, "session_id": 0,
			"lease_expire": nil, "updated_at": now,
		})
	var pending, leased int64
	config.DB.Model(&model.TaskUnit{}).Where("task_id = ? AND status = ?", taskID, model.UnitPending).Count(&pending)
	config.DB.Model(&model.TaskUnit{}).Where("task_id = ? AND status = ?", taskID, model.UnitLeased).Count(&leased)
	if pending > 0 || leased > 0 {
		return false
	}
	s := QueueStatsOf(taskID)
	if s.Done+s.FailedUnit == 0 {
		return false // 单元表是空的：InitUnits 还没跑，不算收敛
	}
	updates := map[string]interface{}{"status": "done", "finished_at": now}
	if s.FailedUnit > 0 {
		updates["error"] = fmt.Sprintf("%d 个 IP 三次失败已隔离", s.FailedUnit)
	}
	// 条件更新防双写：只有还是 running 才落 done
	res := config.DB.Model(&model.ScanTask{}).Where("id = ? AND status = ?", taskID, "running").Updates(updates)
	if res.RowsAffected == 0 {
		return false
	}
	ClearTaskCtrl(taskID)
	// 会话行收尾：running 的队列会话置 done
	config.DB.Model(&model.AgentTask{}).
		Where("task_id = ? AND batch_no = 0 AND status IN ?", taskID, []string{"pending", "running"}).
		Updates(map[string]interface{}{"status": "done", "progress": 100, "message": "队列消费完成", "finished_at": now})
	tasklog.AppendTS(taskID, "[队列] 任务收敛：完成 %d，隔离 %d，探针 %d/%d",
		s.Done, s.FailedUnit, s.ProbesDone, s.ProbesTotal)
	FinishTaskRun(taskID, "done")
	if _, loaded := queueNotifyOnce.LoadOrStore(taskID, true); !loaded {
		NotifyTaskDone(taskID, task.Name, "完成")
	}
	return true
}

// ===== 队列会话（agent/本机执行槽位，挂 agent_tasks） =====
//
// 队列模式下不再有"一个分片一行"，但取消投影（cancelled）、调控投影（cmd）、
// 终端日志、节点进度弹窗全挂在 agent_tasks 上。折中：每节点×每任务一行**会话**
// （batch_no=0 标记队列会话）：
//   - claim 时按需创建，sessionId 随领取响应给 agent，是心跳/上报的路由键；
//   - 会话的 total/scanned 累计该槽位已结算单元的真值（节点进度弹窗照旧能看）；
//   - StopTask 把会话置 cancelled → agent 心跳收到 cancelled → 停当前单元并退出领取循环。

// EnsureSession 取/建节点在任务上的队列会话行，返回 agent_tasks.id。
func EnsureSession(taskID, nodeID uint, nodeName string) (uint, error) {
	var at model.AgentTask
	err := config.DB.Where("task_id = ? AND node_id = ? AND batch_no = 0", taskID, nodeID).
		Order("id").First(&at).Error
	if err == nil {
		if at.Status != "running" && at.Status != "pending" {
			config.DB.Model(&model.AgentTask{}).Where("id = ?", at.ID).
				Updates(map[string]interface{}{"status": "running", "message": "", "finished_at": nil})
		}
		return at.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	now := time.Now()
	at = model.AgentTask{
		TaskID: taskID, NodeID: nodeID, NodeName: nodeName,
		BatchNo: 0, BatchTotal: 0, Status: "running",
		Message: "IP 租约队列", StartedAt: &now,
	}
	if err := config.DB.Create(&at).Error; err != nil {
		return 0, err
	}
	tasklog.AppendTS(taskID, "[队列] 节点 %s 加入队列消费（会话 #%d）", nodeName, at.ID)
	return at.ID, nil
}

// NoteSessionUnit 会话累计一个已结算单元的探针数（节点进度弹窗口径不变）。
// 单条 UPDATE 原子完成：total/scanned 累加 + progress 按累计真值刷新（弹窗进度条读 progress）。
func NoteSessionUnit(sessionID uint, probesTotal, probesDone int64, ok bool) {
	if sessionID == 0 {
		return
	}
	up := map[string]interface{}{
		"total":       gorm.Expr("total + ?", probesTotal),
		"scanned":     gorm.Expr("scanned + ?", probesDone),
		"progress":    gorm.Expr("CASE WHEN total + ? > 0 THEN LEAST(100, (scanned + ?) * 100 / (total + ?)) ELSE 0 END", probesTotal, probesDone, probesTotal),
		"eta_seconds": 0,
	}
	if !ok {
		up["message"] = "单元失败已回池"
	}
	config.DB.Model(&model.AgentTask{}).Where("id = ?", sessionID).Updates(up)
}

// QueueTasksForNode 该节点可消费的在线队列任务（agent 轮询领取用）。
// node_ids：""=本机 / "all"=全部 / "1,2"=指定。返回 [{taskId, taskName}] 保持先建先消费。
type QueueTaskRef struct {
	TaskID   uint   `json:"taskId"`
	TaskName string `json:"taskName"`
	// SessionID 该节点在该任务上的会话行（agent_task.batch_no=0）ID。
	// agent 用它拼审计库 Project（agent-q-<sessionID>-<ip>），据此清理已结束任务的审计行。
	SessionID uint `json:"sessionId"`
}

func QueueTasksForNode(nodeID uint) []QueueTaskRef {
	var tasks []model.ScanTask
	config.DB.Where("queue_mode = ? AND status = ?", true, "running").Order("id").Limit(50).Find(&tasks)
	out := make([]QueueTaskRef, 0, len(tasks))
	for _, t := range tasks {
		if taskAllowsNode(&t, nodeID) {
			out = append(out, QueueTaskRef{TaskID: t.ID, TaskName: t.Name, SessionID: sessionOf(t.ID, nodeID)})
		}
	}
	return out
}

// taskAllowsNode 任务的 node_ids 是否允许该节点领取（""=仅本机执行，不给 agent；
// 与旧分片制不同：旧制 "" 会自动指派最优节点，队列制把「本机」与「节点」分干净——
// CreateTask/RunTask 里 nodeIds 为空且无在线节点才走本机路径，见 dispatchQueueTask）。
func taskAllowsNode(t *model.ScanTask, nodeID uint) bool {
	switch {
	case t.NodeIDs == "all":
		return true
	case t.NodeIDs == "":
		return false // 本机任务：agent 不抢
	default:
		for _, s := range strings.Split(t.NodeIDs, ",") {
			if id, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && uint(id) == nodeID {
				return true
			}
		}
		return false
	}
}

// ===== 下发入口：队列模式分支 =====

// DispatchQueueTask 以队列模式下发任务：展开单元 → 状态落 running → 唤醒目标/全部节点。
// nodeIDs="" 且无在线节点 → 本机消费（由调用方走 ExecuteTaskQueue）；
// nodeIDs="" 但有在线节点 → 沿用旧行为自动指派最优节点（前端展示"派给了谁"）。
// 返回是否走了本机（调用方据此决定 go ExecuteTaskQueue）。
func DispatchQueueTask(task *model.ScanTask) (localFallback bool, err error) {
	// 终态任务再次下发（cron 触发 / RunTask 重跑）= 全新执行：单元回炉、error 清零。
	// 否则上轮 done 的单元会让本轮"无可领取"直接秒收敛（cron 二次触发 bug 的根治点）。
	if task.Status == "done" || task.Status == "failed" || task.Status == "stopped" {
		ResetUnits(task.ID)
		config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).Update("error", "")
		task.Error = ""
		queueNotifyOnce.Delete(task.ID)
		DropETAState(task.ID)
	}
	pc := PortCountOf(task)

	// 单元已经就绪（可能是存活探测阶段建好的，也可能是重跑/重复下发）→ 直接收尾，
	// **绝不能再按 IPRanges 展开一遍**：那会把存活探测判死的 IP 重新塞回队列，
	// 存活探测就白做了（实测：99 个存活 IP 的任务被二次下发成 256 个单元）。
	if n := unitCountOf(task.ID); n > 0 {
		return finishQueueActivation(task, 0, int(n), pc), nil
	}

	// 开启存活探测时，先把目标探一遍再入队（异步，避免请求堵住几分钟）。
	if task.AliveProbe {
		if !startQueueAfterAliveProbe(*task, pc) {
			return false, fmt.Errorf("存活探测启动失败")
		}
		return false, nil
	}

	created, total, err := InitUnits(task.ID, task.IPRanges, pc)
	if err != nil {
		return false, err
	}
	return finishQueueActivation(task, created, total, pc), nil
}

// finishQueueActivation 队列准备完成后的统一收尾：指派节点 → 落 running → 唤醒领取。
// 抽出来是因为有两条入口：①无存活探测（直接展开全量 IP）；②存活探测完成（只有存活 IP）。
func finishQueueActivation(task *model.ScanTask, created, total, pc int) (localFallback bool) {
	if task.NodeIDs == "" {
		// 未指定节点：有在线节点就自动指派（与分片制同策略），否则本机消费
		var nodes []model.Node
		config.DB.Find(&nodes)
		pickIdx := -1
		for i := range nodes {
			if nodes[i].Name == "node-docker-1" && nodeAlive(&nodes[i]) {
				pickIdx = i
				break
			}
		}
		if pickIdx < 0 {
			for i := range nodes {
				if !nodeAlive(&nodes[i]) {
					continue
				}
				if pickIdx < 0 || (nodes[i].LastHeartbeat != nil && nodes[pickIdx].LastHeartbeat != nil &&
					nodes[i].LastHeartbeat.After(*nodes[pickIdx].LastHeartbeat)) {
					pickIdx = i
				}
			}
		}
		if pickIdx >= 0 {
			task.NodeIDs = fmt.Sprint(nodes[pickIdx].ID)
			log.Printf("[队列] 任务 #%d 未指定节点，自动指派 %s", task.ID, nodes[pickIdx].Name)
		} else {
			localFallback = true
		}
	}
	now := time.Now()
	updates := map[string]interface{}{
		"status": "running", "queue_mode": true, "total_ips": total, "node_ids": task.NodeIDs,
	}
	if task.StartedAt == nil {
		updates["started_at"] = now
		task.StartedAt = &now
	}
	config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).Updates(updates)
	task.Status = "running"
	tasklog.AppendTS(task.ID, "[队列] 任务 #%d 展开 %d 个 IP × %d 端口 = %d 探针（新建单元 %d），进入单 IP 租约队列",
		task.ID, total, pc, int64(total)*int64(pc), created)
	if !localFallback {
		wakeQueueConsumers(task)
	}
	return localFallback
}


// wakeQueueConsumers 唤醒该任务允许的所有节点（拉醒领取循环）。
func wakeQueueConsumers(task *model.ScanTask) {
	var nodes []model.Node
	if task.NodeIDs == "all" {
		config.DB.Find(&nodes)
	} else {
		var ids []uint
		for _, s := range strings.Split(task.NodeIDs, ",") {
			if id, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && id > 0 {
				ids = append(ids, uint(id))
			}
		}
		config.DB.Where("id IN ?", ids).Find(&nodes)
	}
	for i := range nodes {
		if nodeAlive(&nodes[i]) {
			WakeAgent(nodes[i].ID)
		}
	}
}

// ===== 本机执行路径：进程内消费队列（与 agent 路径同构） =====
//
// claim(nodeID=0) → 单 IP RunBuiltinPortScan → 2s ticker 续租+上报 → done → 再 claim。
// 暂停：完成当前单元后停止领取（limiter Pause 由 localCtrlPoller 投影，进度冻结）；
// 取消：StopTask 已把任务置非 running，循环出口自然收敛；
//       在扫单元由 StopTask→StopScan(taskProjectID) 直接命中（句柄键=任务名，恒定，
//       与旧本机路径一致——单元串行，同一时刻本机只会持有一个句柄）。

var (
	// 引擎进度行：「进度 21% (283200/1279104) [3m4s:10m23s] 5500探针/秒 开放 3 已识别 2」
	// 与 internal/scanner/builtin.go 的进度行文案对齐：
	// 「[自研引擎] 扫描中 45%（4500/10000 探针，已用 3s，剩余 6s）｜613 探针/秒｜开放端口 2 个，已识别 2 个」
	unitProgRe = regexp.MustCompile(`(\d+)\s*/\s*(\d+)\s*探针|\((\d+)\s*/\s*(\d+)\)`)
	unitPpsRe  = regexp.MustCompile(`([\d.]+)\s*探针/秒`)
	unitOpenRe = regexp.MustCompile(`开放端口 (\d+) 个[，,]?\s*(?:已识别|识别服务) (\d+) 个|开放 (\d+) 已识别 (\d+)`)
)

// ExecuteTaskQueue 本机队列消费主循环。返回任务级错误（队列空返回 nil 由收敛判定收尾）。
func ExecuteTaskQueue(taskID uint) error {
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return err
	}
	pc := PortCountOf(&task)
	// 单元可能已由「存活探测」阶段建好（只有存活 IP）。那种情况下绝不能再用
	// task.IPRanges 全量展开一遍，否则死 IP 又被塞进队列，存活探测就白做了。
	if unitCountOf(task.ID) == 0 {
		if task.AliveProbe {
			// 开了存活探测但单元还没建 → 说明探测还在后台跑。
			// 这里必须**什么都不做**直接返回：探测完成时会自动拉起本机消费
			// （见 startQueueAfterAliveProbe 的 local 分支）。
			// 早期版本在这里顺手 InitUnits(IPRanges)，结果探测白做（实测 99 存活却排了 256 个单元）。
			log.Printf("[队列] 任务 #%d 存活探测进行中，等待单元就绪后再消费", task.ID)
			return nil
		}
		if _, total, err := InitUnits(task.ID, task.IPRanges, pc); err != nil {
			return fmt.Errorf("队列初始化失败: %w", err)
		} else {
			config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).
				Updates(map[string]interface{}{"status": "running", "queue_mode": true, "total_ips": total})
		}
	} else {
		config.DB.Model(&model.ScanTask{}).Where("id = ?", task.ID).
			Updates(map[string]interface{}{"status": "running", "queue_mode": true})
	}
	tasklog.Clear(task.ID)
	tasklog.AppendTS(task.ID, "[本机] 收到任务「%s」，进入单 IP 租约队列消费", task.Name)
	sessID, err := EnsureSession(task.ID, 0, "本机")
	if err != nil {
		return err
	}

	ports := TaskPortsOf(&task)
	baseOpts := scanner.ScanOptions{
		ProjectID: taskProjectID(task),
		Modules:   task.Modules, Finger: task.Finger,
		Ports: ports, Thread: task.Thread, Timeout: task.Timeout * 1000,
		NoPing: task.NoPing, NoSmart: task.NoSmart, ExtraArgs: task.ExtraArgs,
	}

	// 调控投影轮询（暂停/调速 → 进程内直调 ControlScan；与旧本机路径一致）
	stopPoll := make(chan struct{})
	defer close(stopPoll)
	go localCtrlPoller(task.ID, baseOpts.ProjectID, stopPoll)

	consumed := 0
	for {
		var t model.ScanTask
		if err := config.DB.First(&t, taskID).Error; err != nil {
			break
		}
		if t.Status != "running" && t.Status != "paused" {
			break // 停止/取消/收敛
		}
		u, err := ClaimUnit(taskID, 0, sessID)
		if errors.Is(err, ErrQueuePaused) {
			time.Sleep(1500 * time.Millisecond) // 暂停：完成当前单元后停领待恢复
			continue
		}
		if err != nil {
			log.Printf("[task #%d] 本机领取单元失败: %v", taskID, err)
			time.Sleep(time.Second)
			continue
		}
		if u == nil {
			break // 无可领单元（队列空——出口处收敛判定会回收过期租约后判 done）
		}
		runLocalUnit(taskID, baseOpts, u)
		consumed++
	}
	tasklog.AppendTS(taskID, "[本机] 队列消费退出：本机完成 %d 个单元", consumed)
	CheckQueueDone(taskID)
	return nil
}

// localCtrlPoller 本机执行路径的调控状态投影（1.5s）：pause/resume/limit → ControlScan。
func localCtrlPoller(taskID uint, projectID string, stop <-chan struct{}) {
	t := time.NewTicker(1500 * time.Millisecond)
	defer t.Stop()
	appliedPaused := false
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			cmd := TaskCtrlSnapshot(taskID)
			switch {
			case cmd.Paused && !appliedPaused:
				if scanner.ControlScan(projectID, scanner.ScanCtl{Action: "pause"}) {
					tasklog.AppendTS(taskID, "[调控] 本机扫描已挂起（许可清零，在途探针自然结束）")
					appliedPaused = true
				}
			case !cmd.Paused && appliedPaused:
				if scanner.ControlScan(projectID, scanner.ScanCtl{Action: "resume"}) {
					tasklog.AppendTS(taskID, "[调控] 本机扫描已恢复")
					appliedPaused = false
				}
			}
			if !cmd.Paused && cmd.Action == "limit" && (cmd.Concurrency > 0 || cmd.TimeoutMs > 0) {
				scanner.ControlScan(projectID, scanner.ScanCtl{
					Action: "limit", Concurrency: cmd.Concurrency, TimeoutMs: cmd.TimeoutMs})
			}
		}
	}
}

// runLocalUnit 本机扫一个单元：2s ticker 把引擎进度写单元行（续租+ETA 数据源），
// 结果流式入库，完成判 done、失败回池。取消返回 context.Canceled 也算「未正常完成」→ 回池，
// 但任务已非 running，不会有人再领，留 pending 行由重跑续扫。
func runLocalUnit(taskID uint, opts scanner.ScanOptions, u *QueueUnit) {
	t0 := time.Now()
	var (
		mu    sync.Mutex
		pd    int64 // probes done
		pt    int64 // probes total（引擎行里的真值）
		open  int64
		ident int64
		pps   float64
	)
	logFn := func(line string, isProgress bool) {
		if !isProgress {
			tasklog.Append(taskID, line)
			return
		}
		tasklog.SetProgress(taskID, line)
		if a, b, ok := progPairOf(line); ok {
			mu.Lock()
			if a > pd {
				pd = a
			}
			if b > pt {
				pt = b
			}
			mu.Unlock()
		}
		if m := unitPpsRe.FindStringSubmatch(line); len(m) == 2 {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				mu.Lock()
				pps = v
				mu.Unlock()
			}
		}
		if a, b, ok := openPairOf(line); ok {
			mu.Lock()
			if a > open {
				open = a
			}
			if b > ident {
				ident = b
			}
			mu.Unlock()
		}
	}

	stopTk := make(chan struct{})
	tkDone := make(chan struct{})
	go func() {
		defer close(tkDone)
		tk := time.NewTicker(2 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-stopTk:
				return
			case <-tk.C:
				mu.Lock()
				cp, ct, co, ci, cps := pd, pt, open, ident, pps
				mu.Unlock()
				// 本机槽位 node_id=0；续租+实时落账一步完成
				ReportUnit(taskID, 0, u.IP, UnitReport{
					ProbesDone: cp, ProbesTotal: ct, OpenFound: co, Identified: ci, Pps: cps,
				})
			}
		}
	}()

	results, err := scanner.RunPortScan([]string{u.IP}, ExcludeRangeStrings(), opts, logFn,
		func(batch []scanner.ScanResult) { ingestServicesLive(batch) })
	close(stopTk)
	<-tkDone

	mu.Lock()
	fd, ft, fo, fi, fps := pd, pt, open, ident, pps
	mu.Unlock()
	if ft == 0 {
		ft = u.ProbesTotal
	}
	if fd == 0 && err == nil {
		fd = ft // 进度行来不及抽（秒级 tick 之外的快单元）：成功即按满额计
		fo = int64(len(results))
	}
	st := "done"
	if err != nil {
		st = "failed"
	}
	// 统一走 ReportUnit：落单元终态 + 会话累计 + 触发收敛判定
	ReportUnit(taskID, 0, u.IP, UnitReport{
		ProbesDone: fd, ProbesTotal: ft, OpenFound: fo, Identified: fi, Pps: fps, Status: st,
	})
	if err != nil {
		tasklog.AppendTS(taskID, "[本机] 单元 %s 未正常完成（%v），已回池", u.IP, err)
		return
	}
	tasklog.AppendTS(taskID, "[本机] 单元 %s 完成：%d 探针 %d 开放，耗时 %v",
		u.IP, fd, fo, time.Since(t0).Round(time.Millisecond))
}

// progPairOf / openPairOf 解析引擎进度行（同时兼容新旧两代文案）。
func progPairOf(line string) (int64, int64, bool) {
	m := unitProgRe.FindStringSubmatch(line)
	if len(m) == 0 {
		return 0, 0, false
	}
	pick := func(i int) int64 {
		if i < len(m) && m[i] != "" {
			v, _ := strconv.ParseInt(m[i], 10, 64)
			return v
		}
		return 0
	}
	if m[1] != "" {
		return pick(1), pick(2), true
	}
	return pick(3), pick(4), true
}

func openPairOf(line string) (int64, int64, bool) {
	m := unitOpenRe.FindStringSubmatch(line)
	if len(m) == 0 {
		return 0, 0, false
	}
	pick := func(i int) int64 {
		if i < len(m) && m[i] != "" {
			v, _ := strconv.ParseInt(m[i], 10, 64)
			return v
		}
		return 0
	}
	if m[1] != "" {
		return pick(1), pick(2), true
	}
	return pick(3), pick(4), true
}
