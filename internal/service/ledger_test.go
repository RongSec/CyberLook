package service

// 台账导入 / 多归属冲突判定单测（需要 Postgres，与 scanqueue_test 同一套 TEST_PG_URL 约定）。
//
// 运行：TEST_PG_URL=postgres://user:pass@127.0.0.1:5433/ledger_test?sslmode=disable \
//       go test ./internal/service/ -run TestImportLedger -v
//
// 验证的核心口径：一个 IP 允许多归属，只有「同一 (IP, 系统名, 负责人) 这条归属
// 已被人工确认、而新台账改了它的其余字段」才算冲突；不同系统/负责人是合法新增。

import (
	"os"
	"testing"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupLedgerDB(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL 未设置，跳过台账导入测试（需要 Postgres）")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	config.DB = db
	if err := db.AutoMigrate(&model.Asset{}, &model.ImportConflict{}, &model.Department{}); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}
	db.Exec("TRUNCATE assets, import_conflicts, departments RESTART IDENTITY")
}

func TestImportLedgerMultiOwner(t *testing.T) {
	setupLedgerDB(t)

	const ip = "10.99.0.1"
	// 已人工确认的第一条归属
	config.DB.Create(&model.Asset{
		IP: ip, SystemName: "OA系统", Owner: "张三", Department: "信息中心",
		Source: "ledger", Status: "confirmed",
	})

	// 1) 完全相同的行 → 更新计数，不产生冲突，不新增行
	imp, upd, cf, fail, errs := ImportLedger([]LedgerRow{
		{IP: ip, SystemName: "OA系统", Owner: "张三", Department: "信息中心"},
	})
	if imp != 0 || upd != 1 || cf != 0 || fail != 0 {
		t.Fatalf("相同行应 updated=1，实际 imported=%d updated=%d conflict=%d failed=%d %v", imp, upd, cf, fail, errs)
	}

	// 2) 同 IP 不同系统/负责人 → 合法新增第二归属，不是冲突
	imp, upd, cf, fail, errs = ImportLedger([]LedgerRow{
		{IP: ip, SystemName: "ERP系统", Owner: "李四", Department: "财务部"},
	})
	if imp != 1 || cf != 0 || fail != 0 {
		t.Fatalf("第二归属应新增，实际 imported=%d updated=%d conflict=%d failed=%d %v", imp, upd, cf, fail, errs)
	}
	var cnt int64
	config.DB.Model(&model.Asset{}).Where("ip = ?", ip).Count(&cnt)
	if cnt != 2 {
		t.Fatalf("同 IP 应有两行归属，实际 %d", cnt)
	}

	// 3) 已确认归属的部门被改 → 冲突（同 (IP,系统,负责人) 命中已确认行）
	imp, upd, cf, fail, errs = ImportLedger([]LedgerRow{
		{IP: ip, SystemName: "OA系统", Owner: "张三", Department: "数字化部"},
	})
	if cf != 1 || imp != 0 || fail != 0 {
		t.Fatalf("改部门应记为冲突，实际 imported=%d updated=%d conflict=%d failed=%d %v", imp, upd, cf, fail, errs)
	}
	var conflicts []model.ImportConflict
	config.DB.Find(&conflicts)
	if len(conflicts) != 1 || conflicts[0].NewDept != "数字化部" {
		t.Fatalf("冲突表内容不对: %+v", conflicts)
	}

	// 4) 同一归属重复导入冲突值 → 只更新那条冲突，不重复记录
	ImportLedger([]LedgerRow{{IP: ip, SystemName: "OA系统", Owner: "张三", Department: "数字化二部"}})
	config.DB.Find(&conflicts)
	if len(conflicts) != 1 || conflicts[0].NewDept != "数字化二部" {
		t.Fatalf("冲突应被就地更新为一条，实际 %d 条 %+v", len(conflicts), conflicts)
	}

	// 5) 确认后行数仍为 2，第二归属不受影响
	config.DB.Model(&model.Asset{}).Where("ip = ?", ip).Count(&cnt)
	if cnt != 2 {
		t.Fatalf("导入冲突不应新增行，实际 %d", cnt)
	}
}
