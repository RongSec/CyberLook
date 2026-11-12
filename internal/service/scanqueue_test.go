package service

// IP 租约队列并发正确性单测（需要 Postgres：SELECT FOR UPDATE SKIP LOCKED 语义）。
//
// 运行：TEST_PG_URL=postgres://user:pass@127.0.0.1:5433/queue_test?sslmode=disable \
//       go test ./internal/service/ -run TestQueue -v
// 未设置 TEST_PG_URL 时整体跳过（CI/无本地库环境不阻塞；服务器实测时跑）。

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupQueueDB(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL 未设置，跳过 IP 租约队列并发测试（需要 Postgres）")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	config.DB = db
	// 表结构以模型为准（与 migrations/041 列一致；测试环境直接 AutoMigrate 免手工漂移）
	if err := db.AutoMigrate(&model.TaskUnit{}, &model.ScanTask{}, &model.AgentTask{}, &model.TaskRun{}); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}
}

// newTestTask 建一个 running 的队列任务（含 N 个 pending 单元）。
func newTestTask(t *testing.T, name string, ips []string, portCount int) uint {
	t.Helper()
	uniq := name + "-" + fmt.Sprint(time.Now().UnixNano())
	task := model.ScanTask{Name: uniq, IPRanges: "manual", Status: "running",
		QueueMode: true, PortMode: "top100", NodeIDs: "all"}
	if err := config.DB.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	rows := make([]model.TaskUnit, 0, len(ips))
	for _, ip := range ips {
		rows = append(rows, model.TaskUnit{TaskID: task.ID, IP: ip,
			Status: model.UnitPending, ProbesTotal: int64(portCount)})
	}
	if err := config.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return task.ID
}

func cleanupTask(taskID uint) {
	config.DB.Where("task_id = ?", taskID).Delete(&model.TaskUnit{})
	config.DB.Where("task_id = ?", taskID).Delete(&model.AgentTask{})
	config.DB.Where("id = ?", taskID).Delete(&model.ScanTask{})
	ClearTaskCtrl(taskID)
	DropETAState(taskID)
	queueNotifyOnce.Delete(taskID)
}

// TestQueueClaimNoDoubleClaim 并发无双领：16 个 goroutine 抢 40 个单元，
// 每个 IP 必须恰好被领到一次（FOR UPDATE SKIP LOCKED 的核心保证）。
func TestQueueClaimNoDoubleClaim(t *testing.T) {
	setupQueueDB(t)
	var ips []string
	for i := 0; i < 40; i++ {
		ips = append(ips, fmt.Sprintf("10.99.%d.%d", i/256, i%256))
	}
	taskID := newTestTask(t, "nodouble", ips, 100)
	defer cleanupTask(taskID)

	var mu sync.Mutex
	claimed := map[string]int{}
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(nodeID uint) {
			defer wg.Done()
			for {
				u, err := ClaimUnit(taskID, nodeID, 0)
				if err != nil {
					t.Errorf("claim 出错: %v", err)
					return
				}
				if u == nil {
					return // 队列空
				}
				mu.Lock()
				claimed[u.IP]++
				mu.Unlock()
			}
		}(uint(g + 1))
	}
	wg.Wait()

	if len(claimed) != 40 {
		t.Fatalf("应领到 40 个不同 IP，实际 %d", len(claimed))
	}
	for ip, n := range claimed {
		if n != 1 {
			t.Fatalf("IP %s 被领了 %d 次（并发双领！）", ip, n)
		}
	}
	var leased, pending int64
	config.DB.Model(&model.TaskUnit{}).Where("task_id = ? AND status = ?", taskID, model.UnitLeased).Count(&leased)
	config.DB.Model(&model.TaskUnit{}).Where("task_id = ? AND status = ?", taskID, model.UnitPending).Count(&pending)
	if leased != 40 || pending != 0 {
		t.Fatalf("状态计数错误 leased=%d pending=%d", leased, pending)
	}
}

// TestQueueLeaseExpireReclaim 租约过期 → reaper 打回 pending；attempts 累计；可重领。
func TestQueueLeaseExpireReclaim(t *testing.T) {
	setupQueueDB(t)
	taskID := newTestTask(t, "expire", []string{"10.88.0.1"}, 100)
	defer cleanupTask(taskID)

	u, err := ClaimUnit(taskID, 7, 0)
	if err != nil || u == nil {
		t.Fatalf("首次领取失败: %v", err)
	}
	// 手动把租约改成已过期（模拟节点崩溃 60s 后）
	past := time.Now().Add(-time.Second)
	config.DB.Model(&model.TaskUnit{}).Where("task_id = ? AND ip = ?", taskID, u.IP).
		Update("lease_expire", past)

	if n := ReapExpiredUnits(); n != 1 {
		t.Fatalf("reaper 应回收 1 行，实际 %d", n)
	}
	var unit model.TaskUnit
	config.DB.Where("task_id = ? AND ip = ?", taskID, u.IP).First(&unit)
	if unit.Status != model.UnitPending {
		t.Fatalf("过期单元应回 pending，实际 %s", unit.Status)
	}
	if unit.Attempts != 1 {
		t.Fatalf("attempts 应为 1，实际 %d", unit.Attempts)
	}
	// 回池后可再次领取
	u2, err := ClaimUnit(taskID, 8, 0)
	if err != nil || u2 == nil {
		t.Fatalf("回收后重领失败: %v", err)
	}
}

// TestQueueAttemptsIsolation attempts≥3 的过期单元判 failed_unit 隔离，不再回池；
// 任务随之收敛 done（error 记隔离摘要）。
func TestQueueAttemptsIsolation(t *testing.T) {
	setupQueueDB(t)
	taskID := newTestTask(t, "isolate", []string{"10.77.0.1"}, 100)
	defer cleanupTask(taskID)

	for i := 0; i < UnitMaxAttempts; i++ {
		u, err := ClaimUnit(taskID, uint(i+1), 0)
		if err != nil || u == nil {
			t.Fatalf("第 %d 次领取失败: %v", i+1, err)
		}
		past := time.Now().Add(-time.Second)
		config.DB.Model(&model.TaskUnit{}).Where("task_id = ? AND ip = ?", taskID, u.IP).
			Update("lease_expire", past)
		ReapExpiredUnits()
	}
	var unit model.TaskUnit
	config.DB.Where("task_id = ? AND ip = ?", taskID, "10.77.0.1").First(&unit)
	if unit.Status != model.UnitFailedUnit {
		t.Fatalf("attempts=%d 应隔离 failed_unit，实际 status=%s", unit.Attempts, unit.Status)
	}
	if !CheckQueueDone(taskID) {
		t.Fatal("全部单元终态后应收敛")
	}
	var task model.ScanTask
	config.DB.First(&task, taskID)
	if task.Status != "done" {
		t.Fatalf("任务应 done，实际 %s", task.Status)
	}
	if task.Error == "" {
		t.Fatal("含 failed_unit 的任务 error 应有隔离摘要")
	}
}

// TestQueueProgressAndComplete 心跳读数单调不回放 + done 落账 + 迟到心跳不复活。
func TestQueueProgressAndComplete(t *testing.T) {
	setupQueueDB(t)
	taskID := newTestTask(t, "progress", []string{"10.66.0.1", "10.66.0.2"}, 100)
	defer cleanupTask(taskID)

	u1, _ := ClaimUnit(taskID, 1, 0)
	u2, _ := ClaimUnit(taskID, 2, 0)
	if u1 == nil || u2 == nil {
		t.Fatal("应领到 2 个单元")
	}
	// 先报 60，再迟到一条 30：不回卷（乱序心跳单调保护）
	ReportUnit(taskID, 1, u1.IP, UnitReport{ProbesDone: 60, ProbesTotal: 100, Pps: 500})
	ReportUnit(taskID, 1, u1.IP, UnitReport{ProbesDone: 30, ProbesTotal: 100, Pps: 500})
	var unit model.TaskUnit
	config.DB.Where("task_id = ? AND ip = ?", taskID, u1.IP).First(&unit)
	if unit.ProbesDone != 60 {
		t.Fatalf("probes_done 应单调保持 60，实际 %d", unit.ProbesDone)
	}
	// done 落账：probes_done 取 GREATEST
	ReportUnit(taskID, 1, u1.IP, UnitReport{ProbesDone: 100, ProbesTotal: 100, OpenFound: 3, Status: "done"})
	ReportUnit(taskID, 2, u2.IP, UnitReport{ProbesDone: 100, ProbesTotal: 100, Status: "done"})
	deadline := time.Now().Add(3 * time.Second)
	var s QueueStats
	for {
		s = QueueStatsOf(taskID)
		if s.Done == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if s.Done != 2 || s.ProbesDone != 200 || s.ProbesTotal != 200 {
		t.Fatalf("聚合错误 %+v", s)
	}
	percent, eta := QueueProgress(taskID)
	if percent != 100 || eta != 0 {
		t.Fatalf("全完成应 100%%/eta=0，实际 %d%%/%d", percent, eta)
	}
	// 迟到的心跳（行已 done）不得复活单元或改写数字
	ReportUnit(taskID, 1, u1.IP, UnitReport{ProbesDone: 999, Status: "progress"})
	config.DB.Where("task_id = ? AND ip = ?", taskID, u1.IP).First(&unit)
	if unit.Status != model.UnitDone || unit.ProbesDone != 100 {
		t.Fatalf("done 单元被迟到心跳污染: %s %d", unit.Status, unit.ProbesDone)
	}
}

// TestQueuePauseBlocksClaim pause：完成当前单元后拒绝新领取；resume 恢复领取。
func TestQueuePauseBlocksClaim(t *testing.T) {
	setupQueueDB(t)
	taskID := newTestTask(t, "pause", []string{"10.55.0.1", "10.55.0.2"}, 100)
	defer cleanupTask(taskID)

	SetTaskPause(taskID, true)
	u, err := ClaimUnit(taskID, 1, 0)
	if err != ErrQueuePaused || u != nil {
		t.Fatalf("暂停中应拒绝领取: u=%v err=%v", u, err)
	}
	// 暂停不动 pending 行、不碰 leased 语义（已租单元由引擎侧 limiter 冻结）
	var first model.TaskUnit
	config.DB.Where("task_id = ? AND ip = ?", taskID, "10.55.0.1").First(&first)
	if first.Status != model.UnitPending {
		t.Fatal("暂停不该动 pending 行")
	}
	SetTaskPause(taskID, false)
	u, err = ClaimUnit(taskID, 1, 0)
	if err != nil || u == nil {
		t.Fatalf("恢复后应可领取: %v", err)
	}
}
