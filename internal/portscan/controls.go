package portscan

// Controls 扫描运行期调控句柄：动态并发 + 动态超时 + 暂停/继续。
//
// 实现要点（2026-11-11 评审拍板，用户验收硬项）：
//   - **可伸缩许可**：atomic 目标值 + sync.Cond 唤醒，无 busy-wait。
//     SetLimit 调高→Broadcast 唤醒挂起的 worker；调低→在途探针自然结束后
//     新 acquire 被挡，收敛到目标值（不强杀在途连接，避免半开误报）。
//   - Pause()：目标许可置 0，worker 完成当前探针后全部挂起（pps→0）；
//     Resume()：恢复到最近一次 SetLimit 的值。暂停期间在途 dial 自然结束。
//   - SetTimeout 只改 atomic 毫秒值，每次 dial 前读取，下一个探针即生效。
//   - 取消（ctx）与暂停正交：ctx 取消时 Broadcast 唤醒所有 cond 等待者，
//     acquire 返回 false，扫描立即收敛（不会卡在暂停态里退不出来）。

import (
	"context"
	"sync"
	"sync/atomic"
)

// SetLimit 允许范围；超出钳制（与前端滑条一致）
const (
	MinLimit = 10
	MaxLimit = 8000
	MinTimeoutMs = 100
	MaxTimeoutMs = 30000
)

type Controls struct {
	mu        sync.Mutex
	cond      *sync.Cond
	target    int  // 当前允许的最大在途探针数（0=暂停）
	want      int  // 恢复时使用的目标（最近一次非暂停 SetLimit）
	inflight  int  // 已发放未归还的许可数
	paused    bool
	timeoutMs atomic.Int64
}

// NewControls 创建句柄。limit≤0 取默认 4096；timeoutMs≤0 取默认 500。
func NewControls(limit, timeoutMs int) *Controls {
	if limit <= 0 {
		limit = 4096
	}
	if limit < MinLimit {
		limit = MinLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if timeoutMs <= 0 {
		timeoutMs = 500
	}
	c := &Controls{target: limit, want: limit}
	c.cond = sync.NewCond(&c.mu)
	c.timeoutMs.Store(int64(timeoutMs))
	return c
}

// acquire 领取一个许可；ctx 取消或永久失败返回 false。
// 暂停（target=0）或到达上限时挂起在 cond.Wait，由 Broadcast 唤醒。
func (c *Controls) acquire(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	c.mu.Lock()
	for {
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return false
		}
		if c.inflight < c.target {
			c.inflight++
			c.mu.Unlock()
			return true
		}
		// ctx 取消时由 Scan 的 watcher 调 Broadcast 唤醒，这里不会永久挂起
		c.cond.Wait()
	}
}

// release 归还许可并唤醒一个等待者
func (c *Controls) release() {
	c.mu.Lock()
	if c.inflight > 0 {
		c.inflight--
	}
	c.mu.Unlock()
	c.cond.Signal()
}

// SetLimit 动态调整并发（钳制到 10..8000）。暂停中只记 want，不立即恢复。
func (c *Controls) SetLimit(n int) {
	if n < MinLimit {
		n = MinLimit
	}
	if n > MaxLimit {
		n = MaxLimit
	}
	c.mu.Lock()
	c.want = n
	if !c.paused {
		c.target = n
	}
	c.mu.Unlock()
	c.cond.Broadcast() // 调高时唤醒挂起的 worker
}

// Pause 暂停派发新探针（在途自然结束）。幂等。
func (c *Controls) Pause() {
	c.mu.Lock()
	if !c.paused {
		c.paused = true
		c.target = 0
	}
	c.mu.Unlock()
}

// Resume 恢复到最近目标值。幂等。
func (c *Controls) Resume() {
	c.mu.Lock()
	if c.paused {
		c.paused = false
		c.target = c.want
	}
	c.mu.Unlock()
	c.cond.Broadcast()
}

// Paused 当前是否处于暂停态
func (c *Controls) Paused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused
}

// Limit 当前目标并发（暂停时返回 0）
func (c *Controls) Limit() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.target
}

// SetTimeoutMs 动态调整连接超时（毫秒，钳制 100..30000），下一个探针即生效。
func (c *Controls) SetTimeoutMs(ms int) {
	if ms < MinTimeoutMs {
		ms = MinTimeoutMs
	}
	if ms > MaxTimeoutMs {
		ms = MaxTimeoutMs
	}
	c.timeoutMs.Store(int64(ms))
}

// TimeoutMs 当前连接超时（毫秒）
func (c *Controls) TimeoutMs() int { return int(c.timeoutMs.Load()) }

// wakeAll 唤醒全部 cond 等待者（Scan 在 ctx 取消与收尾时调用，
// 保证暂停态中挂起的 worker 能观察到 ctx.Done / jobs 关闭并退出）。
func (c *Controls) wakeAll() { c.cond.Broadcast() }
