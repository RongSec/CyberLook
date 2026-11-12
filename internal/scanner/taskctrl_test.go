package scanner

import (
	"sync"
	"testing"
)

// TestEnsureTaskCtrlCreateOnceReuse 验证：首次创建用传入并发做初值，
// 之后一律返回同一实例且**不重置** limit（否则就是 v0.0.27 的"limit 被盖回"缺陷）。
func TestEnsureTaskCtrlCreateOnceReuse(t *testing.T) {
	const taskID = uint(9001)
	defer UnregisterTaskCtrl(taskID)

	c1 := EnsureTaskCtrl(taskID, 1500, 1500)
	if c1 == nil {
		t.Fatal("EnsureTaskCtrl 首次应创建实例")
	}
	if got := c1.Limit(); got != 1500 {
		t.Fatalf("创建初值应为 1500，实际 %d", got)
	}

	// 模拟"/control limit 60"下发
	c1.SetLimit(60)

	// 后续每轮 claim 都会再调一次 EnsureTaskCtrl（复用路径），不得把 60 盖回 1500
	c2 := EnsureTaskCtrl(taskID, 1500, 1500)
	if c2 != c1 {
		t.Fatal("复用路径应返回同一实例")
	}
	if got := c2.Limit(); got != 60 {
		t.Fatalf("复用路径不得重置 limit，期望 60，实际 %d", got)
	}
}

// TestApplyTaskCtrlSeqOrdering 验证乱序防护：seq 更小的老心跳必须被丢弃，
// 不能把新速率盖回旧值（这是"偶发回弹旋钮"的根因）。
func TestApplyTaskCtrlSeqOrdering(t *testing.T) {
	const taskID = uint(9002)
	defer UnregisterTaskCtrl(taskID)

	EnsureTaskCtrl(taskID, 1500, 1500)

	// seq=5：limit 60
	if ok := ApplyTaskCtrl(taskID, ScanCtl{Action: "limit", Concurrency: 60, TimeoutMs: 1500}, 5); !ok {
		t.Fatal("seq=5 limit 应被应用")
	}
	if got := TaskCtrl(taskID).Limit(); got != 60 {
		t.Fatalf("期望 limit=60，实际 %d", got)
	}

	// seq=3：老心跳晚到，limit 1500 —— 必须丢弃
	if ok := ApplyTaskCtrl(taskID, ScanCtl{Action: "limit", Concurrency: 1500, TimeoutMs: 1500}, 3); ok {
		t.Fatal("seq=3 是乱序老心跳，应被丢弃")
	}
	if got := TaskCtrl(taskID).Limit(); got != 60 {
		t.Fatalf("乱序丢弃后 limit 应仍为 60，实际 %d", got)
	}

	// seq=6：更新的心跳，limit 300 —— 应生效
	if ok := ApplyTaskCtrl(taskID, ScanCtl{Action: "limit", Concurrency: 300, TimeoutMs: 1500}, 6); !ok {
		t.Fatal("seq=6 limit 应被应用")
	}
	if got := TaskCtrl(taskID).Limit(); got != 300 {
		t.Fatalf("期望 limit=300，实际 %d", got)
	}
}

// TestApplyTaskCtrlPauseResumeAndLifecycle 验证 pause 冻结 + resume 恢复，
// 以及 Unregister 后投影返回 false（句柄已释放）。
func TestApplyTaskCtrlPauseResumeAndLifecycle(t *testing.T) {
	const taskID = uint(9003)
	defer UnregisterTaskCtrl(taskID)

	EnsureTaskCtrl(taskID, 600, 1500)

	if ok := ApplyTaskCtrl(taskID, ScanCtl{Action: "pause"}, 1); !ok {
		t.Fatal("pause 应被应用")
	}
	if !TaskCtrl(taskID).Paused() {
		t.Fatal("pause 后应处于暂停态")
	}
	if ok := ApplyTaskCtrl(taskID, ScanCtl{Action: "resume"}, 2); !ok {
		t.Fatal("resume 应被应用")
	}
	if TaskCtrl(taskID).Paused() {
		t.Fatal("resume 后不应处于暂停态")
	}

	UnregisterTaskCtrl(taskID)
	if TaskCtrl(taskID) != nil {
		t.Fatal("Unregister 后 TaskCtrl 应为 nil")
	}
	if ok := ApplyTaskCtrl(taskID, ScanCtl{Action: "pause"}, 3); ok {
		t.Fatal("Unregister 后投影应返回 false")
	}
}

// TestApplyTaskCtrlConcurrentNoRace 并发投影（K 路单元同时心跳）应无数据竞争，
// 且最终状态由最大 seq 决定（-race 下跑）。
func TestApplyTaskCtrlConcurrentNoRace(t *testing.T) {
	const taskID = uint(9004)
	defer UnregisterTaskCtrl(taskID)

	EnsureTaskCtrl(taskID, 1500, 1500)
	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(seq int64) {
			defer wg.Done()
			// 乱序投递：偶数 seq 是更老的，期望被丢弃
			conc := 60 + int(seq)
			ApplyTaskCtrl(taskID, ScanCtl{Action: "limit", Concurrency: conc, TimeoutMs: 1500}, seq)
		}(int64(i + 1))
	}
	wg.Wait()
	// 最大 seq = workers → limit = 60 + workers
	if got, want := TaskCtrl(taskID).Limit(), 60+workers; got != want {
		t.Fatalf("并发投影后 limit 应由最大 seq 决定，期望 %d，实际 %d", want, got)
	}
}
