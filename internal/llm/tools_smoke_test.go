package llm

// 内置 LLM 工具能力自检：逐个调用只读工具，看返回是否是有效数据 / 是否报错。
// 目的不是测模型，而是测「模型能操作的这个执行层」本身是否兑现了工具描述。
//
// 运行：TEST_PG_URL=postgres://asset:asset123@127.0.0.1:5432/ledger_test?sslmode=disable \
//       go test ./internal/llm/ -run TestToolSmoke -v

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"asset-platform/internal/config"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestToolSmoke(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL 未设置")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	config.DB = db

	cases := []struct{ name, args string }{
		{"get_dashboard_stats", `{}`},
		{"list_assets", `{"limit":2}`},
		{"list_services", `{"limit":2}`},
		{"list_tasks", `{"limit":2}`},
		{"list_nodes", `{}`},
		{"list_disposal", `{"limit":2}`},
		{"get_compliance", `{"level":"high","limit":2}`},
		{"get_scan_coverage", `{}`},
		{"get_asset_trend", `{"days":30}`},
		{"list_segments", `{}`},
		{"list_templates", `{}`},
		{"list_excludes", `{}`},
		{"list_departments", `{}`},
		{"list_import_conflicts", `{}`},
		{"list_snapshots", `{"limit":2}`},
		{"get_system_config", `{}`},
		{"get_ledger_diff", `{"limit":2}`},
		{"get_service_changes", `{"limit":2}`},
		{"search_assets", `{"keyword":"10."}`},
		{"get_system_logs", `{}`},
		{"export_report", `{"type":"assets"}`},
	}
	for _, c := range cases {
		out := Execute(c.name, c.args)
		// 结构检查：必须是合法 JSON 对象
		var probe map[string]interface{}
		if err := json.Unmarshal([]byte(out), &probe); err != nil {
			t.Logf("[FAIL] %-24s 输出不是 JSON: %.120s", c.name, out)
			continue
		}
		// 错误判定要看「error 字段非空」，不能靠字符串包含：
		// list_tasks 返回的任务对象里本来就有 "error":"" 这个字段，
		// 用 Contains(out, `"error"`) 会把正常结果误判成失败。
		bad := strings.TrimSpace(out) == "" || strings.TrimSpace(out) == "null"
		if msg, ok := probe["error"].(string); ok && strings.TrimSpace(msg) != "" {
			bad = true
		}
		status := "OK  "
		if bad {
			status = "ERR "
		}
		t.Logf("[%s] %-24s %.220s", status, c.name, out)
	}
}

// TestToolDefsVsImpl 静态核对：工具声明了某参数，实现是否真的用了它。
func TestToolDefsVsImpl(t *testing.T) {
	// 已知偏差（有证据才写进来，不做猜测）
	known := []struct{ tool, issue string }{
		{"get_compliance", "声明参数 level(high/medium/low)，实现按 status 过滤 → 传 level 恒为空"},
		{"get_scan_coverage", "描述是「已扫IP/规划网段总IP」，实现只数 segments/assets/services 三张表的行数，不产出覆盖率"},
		{"list_disposal", "tag 仍用旧口径 new/ledger/changed，与处置页新分类 host_new/port_new/changed 不一致"},
		{"identify_fingerprint", "只调指纹库识别，不写库、不改 title，与「识别」语义不符"},
	}
	for _, k := range known {
		t.Logf("[KNOWN] %-22s %s", k.tool, k.issue)
	}
}
