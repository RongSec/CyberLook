package service

// 活跃/睡眠结算口径单测（需要 Postgres，同 TEST_PG_URL 约定）。
//
// 运行：TEST_PG_URL=postgres://asset:asset123@127.0.0.1:5432/ledger_test?sslmode=disable \
//       go test ./internal/service/ -run TestSettleActivity -v
//
// 锁定三条口径：
//  1. 计数单位是「该 IP 被完整扫描一轮」，不是任务条数：同一任务重复收尾只算一次。
//  2. 领取失败/隔离的单元（status=failed_unit）不算一轮 —— 没扫到不等于没服务。
//  3. 一个端口都没开的主机按缺席累积，连续 3 轮 → 旗下服务睡眠；再现即回活跃。

import (
	"os"
	"testing"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupActivityDB(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL 未设置，跳过活跃度结算测试（需要 Postgres）")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	config.DB = db
	if err := db.AutoMigrate(&model.Asset{}, &model.Service{}, &model.HostState{},
		&model.TaskUnit{}, &model.ScanTask{}); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}
	db.Exec("TRUNCATE assets, services, host_states, task_units, scan_tasks RESTART IDENTITY")
}

func newTask(t *testing.T, name string) model.ScanTask {
	t.Helper()
	task := model.ScanTask{Name: name, IPRanges: "10.99.0.2", Status: "running"}
	if err := config.DB.Create(&task).Error; err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	return task
}

func TestSettleActivityRounds(t *testing.T) {
	setupActivityDB(t)

	const ip = "10.99.0.2"
	asset := model.Asset{IP: ip, Source: "scan", Status: "confirmed"}
	config.DB.Create(&asset)
	svc := model.Service{
		AssetID: &asset.ID, IP: ip, Port: 80, Protocol: "HTTP",
		Activity: model.ActivityActive, MissCount: 0,
	}
	config.DB.Create(&svc)

	// --- 第 1 个任务：单元被隔离（没扫过），不能算缺席一轮 ---
	t1 := newTask(t, "failed-unit")
	config.DB.Create(&model.TaskUnit{TaskID: t1.ID, IP: ip, Status: "failed_unit"})
	SettleActivityForTask(t1.ID)
	var got model.Service
	var hs model.HostState
	config.DB.First(&got, svc.ID)
	if got.MissCount != 0 {
		t.Fatalf("领取失败的单元不该计入缺席，实际 miss_count=%d", got.MissCount)
	}
	if config.DB.Where("ip = ?", ip).First(&hs).Error == nil && hs.MissCount != 0 {
		t.Fatalf("领取失败的单元不该写主机缺席，实际 %d", hs.MissCount)
	}

	// --- 第 2 个任务：扫完但一个端口都没开 → 主机缺席 +1（端口级计数不动）---
	// 主机级缺席由 host_states 承载：主机整体失联时不需要给每个端口单独计数，
	// 达到阈值一次性把旗下服务标睡眠。
	t2 := newTask(t, "round-1")
	config.DB.Create(&model.TaskUnit{TaskID: t2.ID, IP: ip, Status: "done"})
	SettleActivityForTask(t2.ID)
	config.DB.First(&got, svc.ID)
	config.DB.Where("ip = ?", ip).First(&hs)
	if hs.MissCount != 1 || hs.Alive {
		t.Fatalf("第 1 轮主机缺席应为 1，实际 miss=%d alive=%v", hs.MissCount, hs.Alive)
	}
	if got.Activity != model.ActivityActive {
		t.Fatalf("第 1 轮还不该睡眠，实际 %s", got.Activity)
	}

	// 同一任务重复收尾（幂等）：不再加 1
	SettleActivityForTask(t2.ID)
	config.DB.Where("ip = ?", ip).First(&hs)
	if hs.MissCount != 1 {
		t.Fatalf("同一任务重复结算不应双计，实际 miss=%d", hs.MissCount)
	}

	// --- 第 3、4 个任务继续缺席，累计 3 次 → 睡眠 ---
	for i := 0; i < 2; i++ {
		tk := newTask(t, "round-next")
		config.DB.Create(&model.TaskUnit{TaskID: tk.ID, IP: ip, Status: "done"})
		SettleActivityForTask(tk.ID)
	}
	config.DB.First(&got, svc.ID)
	config.DB.Where("ip = ?", ip).First(&hs)
	if hs.MissCount != 3 || got.Activity != model.ActivitySleeping {
		t.Fatalf("连续 3 轮缺席应睡眠，实际 host miss=%d activity=%s", hs.MissCount, got.Activity)
	}

	// --- 第 5 个任务扫到该端口 → 立即回活跃，计数清零 ---
	t5 := newTask(t, "round-alive")
	config.DB.Create(&model.TaskUnit{TaskID: t5.ID, IP: ip, Status: "done"})
	config.DB.Model(&model.Service{}).Where("id = ?", svc.ID).
		Updates(map[string]interface{}{"last_task_id": t5.ID})
	SettleActivityForTask(t5.ID)
	config.DB.First(&got, svc.ID)
	config.DB.Where("ip = ?", ip).First(&hs)
	if got.MissCount != 0 || got.Activity != model.ActivityActive {
		t.Fatalf("重新扫到应回活跃并清零，实际 miss=%d activity=%s", got.MissCount, got.Activity)
	}
	if !hs.Alive || hs.MissCount != 0 {
		t.Fatalf("重新扫到主机应回存活，实际 alive=%v miss=%d", hs.Alive, hs.MissCount)
	}
}
