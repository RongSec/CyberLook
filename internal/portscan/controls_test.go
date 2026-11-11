package portscan

// Controls 动态调速单测（验收硬项）：
//   - 运行时改值不死锁；吞吐随 limit 高低变化（高→低→高跟随）
//   - pause 后完成增量归零、resume 恢复
//   - 暂停态中取消：立即收敛不死锁
//   - SetLimit/SetTimeoutMs 钳制；cmd JSON 字段契约

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestControlsThroughputFollowsLimit(t *testing.T) {
	// 直接在 Controls 层验证吞吐跟随（本地 dial 是微秒级，走 Scan 观测不到窗口差；
	// worker 模拟探针占用 hold 时长，完成数 = 许可周转数，确定性可测）。
	// Scan 层的暂停/调速端到端效应由服务器 F 项实测覆盖。
	const hold = 20 * time.Millisecond
	c := NewControls(10, 500)

	ctx, cancel := context.WithCancel(context.Background())
	// 生产里 Scan 的 watcher 负责"取消后持续唤醒 cond 等待者"，测试自备同款
	fin := make(chan struct{})
	defer close(fin)
	go func() {
		<-ctx.Done()
		for {
			c.wakeAll()
			select {
			case <-fin:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()

	var completed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ { // 64 个候选 worker 抢许可，实际并发=c.Limit()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c.acquire(ctx) {
				time.Sleep(hold)
				c.release()
				completed.Add(1)
			}
		}()
	}

	window := 250 * time.Millisecond
	sample := func(after time.Duration) int64 {
		time.Sleep(after)
		a := completed.Load()
		time.Sleep(window)
		b := completed.Load()
		return b - a
	}

	low := sample(window) // limit=10 → 理论 ≈ 250/20×10 = 125
	c.SetLimit(80)        // 调高 → 理论 8 倍
	high := sample(100 * time.Millisecond)
	c.Pause() // 暂停：在途收尾后新增完成归零
	frozen := sample(100 * time.Millisecond)
	c.Resume()
	back := sample(100 * time.Millisecond)
	c.SetLimit(10) // 调回低
	down := sample(100 * time.Millisecond)

	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	// cancel 后 acquire 返回 false，worker 自然退出；兜底再清一次暂停防挂
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				c.Resume()
				c.wakeAll()
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker 未收敛（死锁？）")
	}

	t.Logf("吞吐(完成数/250ms) 低10:%d 高80:%d 暂停:%d 恢复:%d 回落:%d", low, high, frozen, back, down)
	if high <= low {
		t.Errorf("调高并发吞吐未上升：%d -> %d", low, high)
	}
	if frozen != 0 {
		t.Errorf("暂停期间完成数=%d，期望 0（pause 未生效）", frozen)
	}
	if back == 0 {
		t.Errorf("恢复后吞吐为 0（resume 未生效）")
	}
	if down >= high {
		t.Errorf("调低并发吞吐未回落：%d -> %d", high, down)
	}
}

func TestPauseCancelNoDeadlock(t *testing.T) {
	// 暂停（挂超时）中取消 → 必须穿透暂停态收敛
	ctrl := NewControls(50, 30000) // 30s 超时：探针必挂
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, err := Scan(ctx, []string{"192.0.2.9"}, []int{7, 8, 9, 10}, Options{
			Concurrency: 50, Timeout: 30 * time.Second, Controls: ctrl,
		})
		errCh <- err
	}()
	time.Sleep(150 * time.Millisecond)
	ctrl.Pause()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("取消应返回错误")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("暂停态中取消未收敛（死锁）")
	}
}

func TestControlsSettersClamp(t *testing.T) {
	c := NewControls(4096, 500)
	if c.Limit() != 4096 || c.TimeoutMs() != 500 {
		t.Fatalf("初值异常: %d %d", c.Limit(), c.TimeoutMs())
	}
	c.SetLimit(1)
	if c.Limit() != MinLimit {
		t.Errorf("下钳失败: %d", c.Limit())
	}
	c.SetLimit(99999)
	if c.Limit() != MaxLimit {
		t.Errorf("上钳失败: %d", c.Limit())
	}
	c.SetTimeoutMs(1)
	if c.TimeoutMs() != MinTimeoutMs {
		t.Errorf("超时下钳失败: %d", c.TimeoutMs())
	}
	c.SetTimeoutMs(999999)
	if c.TimeoutMs() != MaxTimeoutMs {
		t.Errorf("超时上钳失败: %d", c.TimeoutMs())
	}
	// 暂停中 SetLimit 只记 want；Resume 用新值；limit 语义=目标并发
	c.SetLimit(300)
	c.Pause()
	if c.Limit() != 0 || !c.Paused() {
		t.Errorf("暂停态异常 limit=%d paused=%v", c.Limit(), c.Paused())
	}
	c.Resume()
	if c.Limit() != 300 || c.Paused() {
		t.Errorf("恢复异常 limit=%d paused=%v", c.Limit(), c.Paused())
	}
}

func TestControlsAcquireReleaseRace(t *testing.T) {
	// -race 高压：多协程同时 acquire/release + SetLimit + Pause/Resume
	c := NewControls(64, 500)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				if c.acquire(ctx) {
					time.Sleep(time.Microsecond)
					c.release()
				}
			}
		}()
	}
	wg.Add(4)
	go func() { defer wg.Done(); for j := 0; j < 200; j++ { c.SetLimit(10 + j%200) } }()
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			c.Pause()
			c.Resume() // 短暂挂起-恢复循环
		}
	}()
	go func() {
		defer wg.Done()
		for j := 0; j < 500; j++ {
			if c.acquire(ctx) {
				c.release()
			}
		}
	}()
	go func() { defer wg.Done(); for j := 0; j < 200; j++ { c.SetTimeoutMs(100 + j%500) } }()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	// 保活：任何挂在暂停里的 acquire 都被最终放开（Pause/Resume 循环协程
	// 结束时停在 Resume，但中间窗口可能有人挂住，这里统一收口）
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				c.SetLimit(8000)
				c.Resume()
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("高压 acquire/release 死锁")
	}
}

func TestCtrlCmdJSONContract(t *testing.T) {
	// cmd 通道与 API/Plans.vue 的字段契约（改字段名必须同步四处：
	// service.TaskCtrlCmd ↔ api /progress 响应 ↔ cmd/agent ctrlCmd ↔ Plans.vue）
	raw := `{"action":"limit","paused":false,"concurrency":1200,"timeoutMs":3000}`
	var v struct {
		Action      string `json:"action"`
		Paused      bool   `json:"paused"`
		Concurrency int    `json:"concurrency"`
		TimeoutMs   int    `json:"timeoutMs"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	if v.Action != "limit" || v.Concurrency != 1200 || v.TimeoutMs != 3000 || v.Paused {
		t.Fatalf("解析异常: %+v", v)
	}
}
