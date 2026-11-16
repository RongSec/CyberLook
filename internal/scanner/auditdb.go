package scanner

// 引擎审计库（本地 sqlite，旁路写）
//
// 用途：扫描过程中的每一批结果除流式上报平台外，同步落一份到**节点本地**的
// config.db（表结构沿用原 TscanPlus 的 ipscan/urlscan 列子集），供事后排查
// 「上报丢没丢、扫描当时看到什么」。
//
// 约束（v0.0.27 更新）：
//   - **agent 与 server 两个进程都显式 EnableAudit()**：agent 写节点本地审计链，
//     server 写自己 exe 同目录的 config/config.db，覆盖「本机直跑任务」的审计。
//     bench 等工具进程不调用 → 不落库（避免压测污染/磁盘无界增长）。
//   - 路径 = 与可执行文件**同目录**的 config/config.db
//     （与历史 TscanClient 行为一致：下载 exe 到哪、审计库就生成到哪：
//     Linux systemd → /opt/weijian-agent/config/，Windows → C:\weijian-agent\config\，
//     compose agent → /app/config/）。exe 目录不可写时回退 ./config/config.db。
//   - **任何写库失败只记日志，绝不影响扫描**（旁路）。
//   - 数据保留到任务 ingest 完成后，由平台清理指令（cleanup:）按 Project 删行，
//     防止占满磁盘（沿用原 clearProjectIPScan 的语义，见 ClearAuditProject）。

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// auditEnabled 仅 agent 进程置 true（EnableAudit）；
// server / bench 进程保持 false，**不写审计库**（避免中心容器磁盘无界增长）。
var auditEnabled atomic.Bool

// auditPathOnce 落库路径只报一次（每个扫描单元都会建一次 writer）。
var auditPathOnce sync.Once

// auditErrOnce / auditDisabledOnce 失败与未启用也只报一次：
// 队列模式下每个 IP 单元都会调进来，984 个单元就是 984 行同样的报错。
var (
	auditErrOnce      sync.Once
	auditDisabledOnce sync.Once
)

// EnableAudit 开启审计落库。只在 cmd/agent 的 main 里调用一次。
// 测试中用于验证开启态；开启后不可逆（进程级开关）。
func EnableAudit() { auditEnabled.Store(true) }

// AuditEnabled 当前进程是否启用审计落库。
func AuditEnabled() bool { return auditEnabled.Load() }

// EngineDBPath 审计库文件完整路径：exe 同目录 config/config.db，不可写回退 ./config/config.db。
// （历史 TscanClient 的 config.db 就在二进制同目录；审计库沿用该语义。）
func EngineDBPath() string {
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	if exeDir != "" {
		candidate := filepath.Join(exeDir, "config")
		if dirWritable(candidate) {
			return filepath.Join(candidate, "config.db")
		}
	}
	return filepath.Join(".", "config", "config.db")
}

// dirWritable 目录存在（或可创建）且可写
func dirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".writetest")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// auditSchema ipscan：端口层事实；urlscan：HTTP 应用层事实。
// 列 = 原 TscanPlus 表的高价值子集（读取方 readTable / 数据库管理只用到这些）。
const auditSchema = `
CREATE TABLE IF NOT EXISTS ipscan (
	Project TEXT, Host TEXT, Port TEXT, Protocol TEXT,
	Banner TEXT, Title TEXT, Os TEXT, Mid TEXT,
	StatusCode TEXT, Lenbody TEXT, Response TEXT,
	CreatedAt TEXT
);
CREATE INDEX IF NOT EXISTS idx_ipscan_project ON ipscan(Project);
CREATE TABLE IF NOT EXISTS urlscan (
	Project TEXT, Host TEXT, Port TEXT, Protocol TEXT,
	Banner TEXT, Title TEXT, Os TEXT, Mid TEXT,
	StatusCode TEXT, Lenbody TEXT, Response TEXT,
	CreatedAt TEXT
);
CREATE INDEX IF NOT EXISTS idx_urlscan_project ON urlscan(Project);
`

// auditWriter 一次扫描的审计写入器。跨 flush 批次串行写（内部 mutex + 单连接）。
type auditWriter struct {
	db      *sql.DB
	mu      sync.Mutex
	project string
	logFn   LogFunc
	closed  bool
	written int
	failed  int
}

// newAuditWriter 打开/创建审计库并确保表结构；未启用/失败返回 nil（只记日志）。
//
// 失败必须让人看得见：这个库是节点「数据库管理」的唯一数据源，静默返回 nil 的后果
// 是界面上永远是"暂无数据"，而且没有任何一处提示为什么（实测踩过：库里连表都没建，
// 日志里也一个字都没有）。
func newAuditWriter(project string, logFn LogFunc) *auditWriter {
	if !AuditEnabled() {
		auditDisabledOnce.Do(func() {
			logf(logFn, "[审计库] 未启用（本进程没有调用 EnableAudit，跳过落库）")
		})
		return nil // server/bench 进程不写审计库
	}
	if project == "" {
		project = "Default"
	}
	dbPath := EngineDBPath()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		auditErrOnce.Do(func() {
			logf(logFn, "[审计库] 目录创建失败（跳过落库）: "+err.Error())
		})
		return nil
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(3000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		auditErrOnce.Do(func() {
			logf(logFn, "[审计库] 打开失败（跳过落库）: "+err.Error())
		})
		return nil
	}
	db.SetMaxOpenConns(1) // sqlite 单写者，串行化避免 database is locked
	if _, err := db.Exec(auditSchema); err != nil {
		auditErrOnce.Do(func() {
			logf(logFn, "[审计库] 建表失败（跳过落库），库路径 "+dbPath+"："+err.Error())
		})
		_ = db.Close()
		return nil
	}
	_ = db.Ping()
	auditPathOnce.Do(func() { logf(logFn, "[审计库] 落库路径 "+dbPath) })
	return &auditWriter{db: db, project: project, logFn: logFn}
}

// WriteBatch 把一批结果写入 ipscan + urlscan（HTTP 结果才进 urlscan）。
// 整批一个事务；失败仅记一次日志（旁路语义），不返回错误给扫描链路。
func (w *auditWriter) WriteBatch(results []ScanResult) {
	if w == nil || len(results) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	tx, err := w.db.Begin()
	if err != nil {
		w.noteFail("开启事务失败", err)
		return
	}
	ipIns, err := tx.Prepare(`INSERT INTO ipscan
		(Project,Host,Port,Protocol,Banner,Title,Os,Mid,StatusCode,Lenbody,Response,CreatedAt)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		w.noteFail("准备 ipscan 语句失败", err)
		return
	}
	defer ipIns.Close()

	isHTTP := func(r ScanResult) bool {
		p := strings.ToLower(r.Protocol + " " + r.Service)
		return strings.Contains(p, "http")
	}
	var urlIns *sql.Stmt
	hasHTTP := false
	for _, r := range results {
		if isHTTP(r) {
			hasHTTP = true
			break
		}
	}
	if hasHTTP {
		urlIns, err = tx.Prepare(`INSERT INTO urlscan
			(Project,Host,Port,Protocol,Banner,Title,Os,Mid,StatusCode,Lenbody,Response,CreatedAt)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			_ = tx.Rollback()
			w.noteFail("准备 urlscan 语句失败", err)
			return
		}
		defer urlIns.Close()
	}

	for _, r := range results {
		host := r.IP + ":" + strconv.Itoa(r.Port)
		resp := r.Response
		if len(resp) > 200000 {
			resp = resp[:200000] // 与旧 readTable 的 substr(Response,1,200000) 口径一致
		}
		if _, err := ipIns.Exec(w.project, host, strconv.Itoa(r.Port), r.Protocol,
			r.Banner, r.Title, r.OS, r.Mid, r.StatusCode, strconv.Itoa(r.ContentLength), resp, now); err != nil {
			_ = tx.Rollback()
			w.noteFail("写 ipscan 失败", err)
			return
		}
		if urlIns != nil && isHTTP(r) {
			if _, err := urlIns.Exec(w.project, host, strconv.Itoa(r.Port), r.Protocol,
				r.Banner, r.Title, r.OS, r.Mid, r.StatusCode, strconv.Itoa(r.ContentLength), resp, now); err != nil {
				_ = tx.Rollback()
				w.noteFail("写 urlscan 失败", err)
				return
			}
		}
		w.written++
	}
	if err := tx.Commit(); err != nil {
		w.noteFail("提交事务失败", err)
	}
}

func (w *auditWriter) noteFail(what string, err error) {
	w.failed++
	if w.failed == 1 || w.failed%20 == 0 { // 首次必报 + 降频，避免刷屏
		logf(w.logFn, fmt.Sprintf("[审计库] %s: %v（累计失败 %d 次，旁路不影响扫描）", what, err, w.failed))
	}
}

// Close 关库并汇报落库统计。
func (w *auditWriter) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	_ = w.db.Close()
	if w.logFn != nil {
		logf(w.logFn, fmt.Sprintf("[审计库] 本次落库 %d 条（Project=%s，失败 %d 次）", w.written, w.project, w.failed))
	}
}

// ClearAuditProject 任务 ingest 完成后的清行逻辑（原 clearProjectIPScan 的引擎版）：
// 删除该 Project 在 ipscan/urlscan 的行，防审计库占满卷空间。
// dbPath 空 = 用 EngineDBPath()。
func ClearAuditProject(dbPath, projectID string) error {
	if dbPath == "" {
		dbPath = EngineDBPath()
	}
	if projectID == "" {
		projectID = "Default"
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, tbl := range []string{"ipscan", "urlscan"} {
		if _, err := db.Exec("DELETE FROM "+tbl+" WHERE Project = ?", projectID); err != nil {
			return err
		}
	}
	return nil
}

// AuditProjects 列出审计库里出现过的 Project（ipscan ∪ urlscan）。
// 库不存在或驱动不可用（mipsle 降级）时返回 nil，调用方按"无项目"处理。
func AuditProjects() []string { return AuditProjectsIn("") }

// AuditProjectsIn 同 AuditProjects，可指定库路径（空=EngineDBPath()，测试用临时库）。
func AuditProjectsIn(dbPath string) []string {
	if dbPath == "" {
		dbPath = EngineDBPath()
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()
	seen := map[string]bool{}
	out := make([]string, 0, 16)
	for _, tbl := range []string{"ipscan", "urlscan"} {
		rows, err := db.Query("SELECT DISTINCT Project FROM " + tbl)
		if err != nil {
			continue // 表还没建（从未落库）
		}
		for rows.Next() {
			var p sql.NullString
			if rows.Scan(&p) == nil && p.Valid && p.String != "" && !seen[p.String] {
				seen[p.String] = true
				out = append(out, p.String)
			}
		}
		rows.Close()
	}
	return out
}

// PruneAuditProjects 清理"会话已结束"的队列审计行，防审计库无界增长。
//
// projectPrefix 是队列单元 Project 的前缀（agent 侧为 "agent-q-"），keepSessions
// 是当前仍在本节点队列里的会话 ID 集合。规则：Project 形如
// "<prefix><session>-<ip>"，解析出的 session 不在 keepSessions 里 → 整条 Project 删除。
// 解析不出来的（手工跑的 Default 等）一律不动，避免误删人工数据。
func PruneAuditProjects(projectPrefix string, keepSessions map[uint]bool) []string {
	return PruneAuditProjectsIn("", projectPrefix, keepSessions)
}

// PruneAuditProjectsIn 同 PruneAuditProjects，可指定库路径（空=EngineDBPath()）。
func PruneAuditProjectsIn(dbPath, projectPrefix string, keepSessions map[uint]bool) []string {
	if projectPrefix == "" {
		return nil
	}
	var removed []string
	for _, p := range AuditProjectsIn(dbPath) {
		if !strings.HasPrefix(p, projectPrefix) {
			continue
		}
		rest := p[len(projectPrefix):]
		dash := strings.IndexByte(rest, '-')
		if dash <= 0 {
			continue // 形态不认识，不动
		}
		sid, err := strconv.ParseUint(rest[:dash], 10, 32)
		if err != nil {
			continue
		}
		if keepSessions[uint(sid)] {
			continue
		}
		if err := ClearAuditProject(dbPath, p); err == nil {
			removed = append(removed, p)
		}
	}
	// 删行只是把页标成空闲，文件不会自己变小：WAL 会一直涨，主库也留着空洞。
	// 节点上这个库是 exe 同目录的 config.db，长期跑下来能吃掉几十 MB 纯垃圾。
	if len(removed) > 0 {
		CompactAuditDBIn(dbPath)
	}
	return removed
}

// AuditProjectStat 单个 Project 的体量与最后写入时间。
type AuditProjectStat struct {
	Project string
	Rows    int64
	LastAt  time.Time
}

// AuditProjectStatsIn 统计每个 Project 的行数与最后写入时间（ipscan ∪ urlscan）。
func AuditProjectStatsIn(dbPath string) []AuditProjectStat {
	if dbPath == "" {
		dbPath = EngineDBPath()
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()
	agg := map[string]*AuditProjectStat{}
	for _, tbl := range []string{"ipscan", "urlscan"} {
		rows, err := db.Query("SELECT Project, COUNT(*), MAX(CreatedAt) FROM " + tbl + " GROUP BY Project")
		if err != nil {
			continue
		}
		for rows.Next() {
			var (
				p    sql.NullString
				n    int64
				last sql.NullString
			)
			if rows.Scan(&p, &n, &last) != nil || !p.Valid || p.String == "" {
				continue
			}
			st, ok := agg[p.String]
			if !ok {
				st = &AuditProjectStat{Project: p.String}
				agg[p.String] = st
			}
			st.Rows += n
			if last.Valid {
				// 落库统一写 "2006-01-02 15:04:05"，按本地时区解析
				if t, err := time.ParseInLocation("2006-01-02 15:04:05", last.String, time.Local); err == nil && t.After(st.LastAt) {
					st.LastAt = t
				}
			}
		}
		rows.Close()
	}
	out := make([]AuditProjectStat, 0, len(agg))
	for _, st := range agg {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.Before(out[j].LastAt) })
	return out
}

// PruneAuditByPolicy 按「保留期 + 容量上限」清理审计库，在途会话一律不碰。
//
// 为什么不再用「会话是否在途」当唯一依据：那等于任务一结束就把证据删掉 ——
// 实测队列任务收敛后 20 秒，刚写进去的 6 条结果就被判为“陈旧”清空，
// 节点「数据库管理」永远显示空。审计库的用途就是**事后排障**，必须留一段时间。
//
//   retain   保留期：最后写入早于 now-retain 的 Project 删除
//   maxBytes 容量上限：删完过期项后仍超限，就按“最旧优先”继续删到阈值以下
//
// 手工 Project（不带 agent-q- 前缀，如 Default）不参与删除：那是人自己跑的。
func PruneAuditByPolicy(dbPath string, inFlight map[uint]bool, retain time.Duration, maxBytes int64) (removed []string, kept int) {
	if retain <= 0 {
		retain = 7 * 24 * time.Hour
	}
	stats := AuditProjectStatsIn(dbPath)
	if len(stats) == 0 {
		return nil, 0
	}
	deadline := time.Now().Add(-retain)

	// 在途会话：project 形如 agent-q-<session>-<ip>
	isInFlight := func(p string) bool {
		const prefix = "agent-q-"
		if !strings.HasPrefix(p, prefix) {
			return false
		}
		rest := p[len(prefix):]
		dash := strings.IndexByte(rest, '-')
		if dash <= 0 {
			return false
		}
		sid, err := strconv.ParseUint(rest[:dash], 10, 32)
		if err != nil {
			return false
		}
		return inFlight[uint(sid)]
	}

	drop := func(p string) {
		if err := ClearAuditProject(dbPath, p); err == nil {
			removed = append(removed, p)
		}
	}

	alive := make([]AuditProjectStat, 0, len(stats))
	for _, st := range stats { // 已按 LastAt 升序
		if isInFlight(st.Project) {
			kept++
			alive = append(alive, st)
			continue
		}
		if !st.LastAt.IsZero() && st.LastAt.Before(deadline) {
			drop(st.Project)
			continue
		}
		alive = append(alive, st)
	}

	// 容量兜底：按最旧优先删，直到文件降到阈值以下
	if maxBytes > 0 {
		size := auditFileBytes(dbPath)
		for i := 0; i < len(alive) && size > maxBytes; i++ {
			if isInFlight(alive[i].Project) {
				continue
			}
			drop(alive[i].Project)
			size = auditFileBytes(dbPath) // 逐次量，删多少算多少（不估算）
		}
	}
	if len(removed) > 0 {
		CompactAuditDBIn(dbPath)
	}
	return removed, kept
}

// auditFileBytes 审计库占用（主库 + WAL + SHM）。
func auditFileBytes(dbPath string) int64 {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if fi, err := os.Stat(dbPath + suffix); err == nil {
			total += fi.Size()
		}
	}
	return total
}

// CompactAuditDBIn 回收审计库空间：WAL checkpoint + VACUUM。
// 只在真的删过行之后调用（VACUUM 会锁库重写整个文件，频繁做没意义）。
func CompactAuditDBIn(dbPath string) {
	if dbPath == "" {
		dbPath = EngineDBPath()
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return
	}
	defer db.Close()
	// TRUNCATE：把 WAL 文件截回 0，否则它只增不减
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("[审计库] WAL checkpoint 失败（忽略）: %v", err)
	}
	if _, err := db.Exec("VACUUM"); err != nil {
		log.Printf("[审计库] VACUUM 失败（忽略）: %v", err)
	}
}
