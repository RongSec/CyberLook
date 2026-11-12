package service

// 任务级运行调控状态（执行层，不改任务配置、不动调度状态机）
//
// 语义（2026-11-11 用户拍板：取消 + 实时进度 + 动态调速）：
//   - 状态只存内存（进程重启后回到默认：执行中、按任务参数速率）；
//   - agent 执行的任务：每条 /api/agent/tasks/:id/progress 心跳响应把当前状态
//     **完整投影**（cmd 字段）带下去，agent 端幂等应用——指令不丢、不重、乱序也收敛；
//   - 本地执行的任务：ExecuteTask 里的轮询器直接调 scanner.ControlScan 应用；
//   - cancel 不进这里：取消收敛进既有 StopTask 链路（分片 cancelled → 心跳
//     cancelled:true → agent StopScan），这里只承载 pause/resume/limit。

import (
	"sync"
)

// TaskCtrlCmd 下发给执行端（agent 或本进程）的调控指令（幂等状态，非事件）。
type TaskCtrlCmd struct {
	Action      string `json:"action"` // none | pause | resume | limit（cancel 由 cancelled 字段承载）
	Paused      bool   `json:"paused"`
	Concurrency int    `json:"concurrency"` // 0=未调整（用节点/任务默认）
	TimeoutMs   int    `json:"timeoutMs"`   // 0=未调整
	// Seq 单调递增的调控版本号（每次 SetTaskPause/SetTaskLimit 自增）。
	// K 路单元并发心跳时，晚到的老心跳会带更小的 Seq，执行端据此丢弃，
	// 避免把新速率盖回旧值（"偶发回弹"的旋钮）。0 = 无调控态（未发生过变更）。
	Seq int64 `json:"seq"`
}

type taskCtrl struct {
	mu          sync.Mutex
	paused      bool
	concurrency int // 0 = 未设
	timeoutMs   int // 0 = 未设
	seq         int64
}

var taskCtrls sync.Map // taskID(uint) -> *taskCtrl

func getTaskCtrl(taskID uint) *taskCtrl {
	if v, ok := taskCtrls.Load(taskID); ok {
		return v.(*taskCtrl)
	}
	c := &taskCtrl{}
	actual, _ := taskCtrls.LoadOrStore(taskID, c)
	return actual.(*taskCtrl)
}

// SetTaskPause 暂停/继续某任务的扫描执行（幂等）。
// 暂停 = 引擎许可清零挂起（进度冻结、分片保持占用）；继续 = 恢复原速率。
func SetTaskPause(taskID uint, paused bool) {
	c := getTaskCtrl(taskID)
	c.mu.Lock()
	c.paused = paused
	c.seq++
	c.mu.Unlock()
}

// SetTaskLimit 调整某任务当前速率（0 表示不改该项）。limit 指令顺带解除暂停。
func SetTaskLimit(taskID uint, concurrency, timeoutMs int) {
	c := getTaskCtrl(taskID)
	c.mu.Lock()
	if concurrency > 0 {
		c.concurrency = concurrency
	}
	if timeoutMs > 0 {
		c.timeoutMs = timeoutMs
	}
	c.paused = false // 设了新值就要按新值跑
	c.seq++
	c.mu.Unlock()
}

// TaskCtrlSnapshot 取当前调控状态投影（随心跳下发 / 本地轮询应用）。
// 没有任何调整且未暂停时返回 action=none。
func TaskCtrlSnapshot(taskID uint) TaskCtrlCmd {
	v, ok := taskCtrls.Load(taskID)
	if !ok {
		return TaskCtrlCmd{Action: "none"}
	}
	c := v.(*taskCtrl)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.paused:
		return TaskCtrlCmd{Action: "pause", Paused: true, Concurrency: c.concurrency, TimeoutMs: c.timeoutMs, Seq: c.seq}
	case c.concurrency > 0 || c.timeoutMs > 0:
		return TaskCtrlCmd{Action: "limit", Concurrency: c.concurrency, TimeoutMs: c.timeoutMs, Seq: c.seq}
	}
	// 无调控态：带当前 seq（可能 >0，表示"已解除暂停/已复位"），
	// 执行端据此 Resume 收敛到"按默认速率运行"。
	return TaskCtrlCmd{Action: "none", Seq: c.seq}
}

// TaskPausedNow 该任务当前是否处于暂停投影（队列领取闸门用）。
func TaskPausedNow(taskID uint) bool {
	v, ok := taskCtrls.Load(taskID)
	if !ok {
		return false
	}
	c := v.(*taskCtrl)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused
}

// ClearTaskCtrl 任务收敛（done/failed/stopped/重跑）时清掉内存态，
// 下次执行用干净状态。
func ClearTaskCtrl(taskID uint) {
	taskCtrls.Delete(taskID)
}
