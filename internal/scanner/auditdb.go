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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// auditEnabled 仅 agent 进程置 true（EnableAudit）；
// server / bench 进程保持 false，**不写审计库**（避免中心容器磁盘无界增长）。
var auditEnabled atomic.Bool

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
func newAuditWriter(project string, logFn LogFunc) *auditWriter {
	if !AuditEnabled() {
		return nil // server/bench 进程不写审计库
	}
	if project == "" {
		project = "Default"
	}
	dbPath := EngineDBPath()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		logf(logFn, "[审计库] 目录创建失败（跳过落库）: "+err.Error())
		return nil
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(3000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		logf(logFn, "[审计库] 打开失败（跳过落库）: "+err.Error())
		return nil
	}
	db.SetMaxOpenConns(1) // sqlite 单写者，串行化避免 database is locked
	if _, err := db.Exec(auditSchema); err != nil {
		logf(logFn, "[审计库] 建表失败（跳过落库）: "+err.Error())
		_ = db.Close()
		return nil
	}
	_ = db.Ping()
	logf(logFn, "[审计库] 落库路径 "+dbPath)
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
