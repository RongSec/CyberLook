package scanner

// 审计库单测：写入 / 读取 / 清理 / 禁用态 / 响应体截断。
// 通过 auditDBOverride 把库路径指到临时目录。

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// openTestAudit 在临时目录建一个已初始化表结构的审计库 writer。
func openTestAudit(t *testing.T, project string) (*auditWriter, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "config.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(auditSchema); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return &auditWriter{db: db, project: project}, dbPath
}

func countRows(t *testing.T, dbPath, table, project string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	q := "SELECT COUNT(*) FROM " + table + " WHERE Project = ?"
	if err := db.QueryRow(q, project).Scan(&n); err != nil {
		t.Fatalf("查询 %s 失败: %v", table, err)
	}
	return n
}

func TestAuditWriteReadClear(t *testing.T) {
	w, dbPath := openTestAudit(t, "unit-audit")
	batch := []ScanResult{
		{IP: "10.0.0.1", Port: 22, Protocol: "SSH", Service: "ssh", Banner: "SSH-2.0-OpenSSH_8.9", Mid: "OpenSSH"},
		{IP: "10.0.0.2", Port: 80, Protocol: "HTTP", Service: "http", Title: "管理后台",
			StatusCode: "200", ContentLength: 123, Response: "<html>ok</html>", Fingerprint: "nginx"},
	}
	w.WriteBatch(batch)
	if w.failed != 0 {
		t.Fatalf("写入失败 %d 次", w.failed)
	}
	if w.written != 2 {
		t.Fatalf("written=%d 期望 2", w.written)
	}
	w.Close()

	if n := countRows(t, dbPath, "ipscan", "unit-audit"); n != 2 {
		t.Fatalf("ipscan 行数=%d 期望 2", n)
	}
	// 只有 HTTP 结果进 urlscan
	if n := countRows(t, dbPath, "urlscan", "unit-audit"); n != 1 {
		t.Fatalf("urlscan 行数=%d 期望 1", n)
	}

	// Host 列口径 = "ip:port"（与原 tscan 表一致，extractHost 可解析）
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var host string
	if err := db.QueryRow(`SELECT Host FROM urlscan WHERE Project='unit-audit'`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if host != "10.0.0.2:80" {
		t.Fatalf("Host=%s 期望 10.0.0.2:80", host)
	}
	if extractHost(host) != "10.0.0.2" {
		t.Fatalf("extractHost(%s) 应解析出纯 IP", host)
	}

	// ingest 完成后按 Project 清行（防占空间）
	if err := ClearAuditProject(dbPath, "unit-audit"); err != nil {
		t.Fatalf("清行失败: %v", err)
	}
	if n := countRows(t, dbPath, "ipscan", "unit-audit"); n != 0 {
		t.Fatalf("清理后 ipscan 仍 %d 行", n)
	}
	if n := countRows(t, dbPath, "urlscan", "unit-audit"); n != 0 {
		t.Fatalf("清理后 urlscan 仍 %d 行", n)
	}
}

func TestAuditDisabledWithoutEnable(t *testing.T) {
	// server/bench 进程不调 EnableAudit → 不建库（防中心容器磁盘无界增长）
	if AuditEnabled() {
		t.Skip("包内其他测试已 EnableAudit，跳过默认态断言")
	}
	if w := newAuditWriter("x", nil); w != nil {
		t.Fatal("未 EnableAudit 时不应创建 writer")
	}
}

func TestAuditResponseTruncatedAt200KB(t *testing.T) {
	w, dbPath := openTestAudit(t, "trunc")
	long := strings.Repeat("a", 201000)
	w.WriteBatch([]ScanResult{{IP: "1.1.1.1", Port: 80, Protocol: "HTTP", Service: "http", Response: long}})
	w.Close()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT LENGTH(Response) FROM ipscan WHERE Project='trunc'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 200000 {
		t.Fatalf("响应体应截断到 200000，实际 %d", n)
	}
}

func TestAuditWriterNilSafe(t *testing.T) {
	var w *auditWriter
	w.WriteBatch([]ScanResult{{IP: "1.1.1.1", Port: 1}}) // 不 panic
	w.Close()
}

func TestEngineDBPathIsExeAdjacent(t *testing.T) {
	p := EngineDBPath()
	if filepath.Base(p) != "config.db" || filepath.Base(filepath.Dir(p)) != "config" {
		t.Fatalf("路径口径应为 <dir>/config/config.db，实际 %s", p)
	}
}

// TestPruneAuditProjects 会话型 Project 的过期清理：
// 在途会话保留、已结束会话删除、非队列 Project（人工 Default）不动。
func TestPruneAuditProjects(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "config.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(auditSchema); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	projects := []string{"agent-q-600-10.0.0.1", "agent-q-601-10.0.0.2", "Default", "weird"}
	for _, p := range projects {
		for _, tbl := range []string{"ipscan", "urlscan"} {
			if _, err := db.Exec("INSERT INTO "+tbl+" (Project, Host, Port) VALUES (?, ?, ?)", p, "10.0.0.1", "80"); err != nil {
				t.Fatalf("插入 %s 失败: %v", tbl, err)
			}
		}
	}
	db.Close()

	got := AuditProjectsIn(dbPath)
	if len(got) != len(projects) {
		t.Fatalf("AuditProjectsIn 期望 %d 个 Project，得到 %v", len(projects), got)
	}

	removed := PruneAuditProjectsIn(dbPath, "agent-q-", map[uint]bool{601: true})
	if len(removed) != 1 || removed[0] != "agent-q-600-10.0.0.1" {
		t.Fatalf("期望只清掉 600 会话，实际 removed=%v", removed)
	}
	if n := countRows(t, dbPath, "ipscan", "agent-q-601-10.0.0.2"); n != 1 {
		t.Fatalf("在途会话 601 的行不该被删，实际 %d", n)
	}
	if n := countRows(t, dbPath, "ipscan", "Default"); n != 1 {
		t.Fatalf("人工 Project 不该被删，实际 %d", n)
	}
	if n := countRows(t, dbPath, "urlscan", "agent-q-600-10.0.0.1"); n != 0 {
		t.Fatalf("过期会话的行应清空，实际 %d", n)
	}
	// 空 keep 集合 + 非法前缀：全部不动
	if r := PruneAuditProjectsIn(dbPath, "", map[uint]bool{}); r != nil {
		t.Fatalf("空前缀应直接返回 nil，实际 %v", r)
	}
	if n := countRows(t, dbPath, "ipscan", "weird"); n != 1 {
		t.Fatalf("形态不认识的 Project 不该被删，实际 %d", n)
	}
}
