package service

// ===== 精确进度 + ETA（IP 租约队列的读数层） =====
//
// 进度按**单元（IP）数**算：已完成单元 ÷ 单元总数。
// 早期版本用探针加权（Σprobes_done ÷ Σprobes_total），在队列模式下有两个毛病：
//   ① 几万个 IP 的任务，前几十分钟永远显示 0%，看着像卡死；
//   ② 单 IP 全端口（65535）的单元权重远大于 top100 的单元，进度会莫名跳变。
// 存活探测阶段（还没有单元）单独按「已探 IP ÷ 总 IP」显示。
//
// ETA = 剩余探针 ÷ 滚动 pps 均值。pps 用 10s 采样间隔的完成增量做 EWMA 平滑
// （α=0.3，抗单 IP 单元的锯齿波动：单元间速度差异大，瞬时 pps 会疯狂抖）。
// 暂停中或 pps≤0 → eta=-1（前端显示「已暂停」而不是转圈的假倒计时）。

import (
	"sync"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

type etaState struct {
	lastDone int64
	ewma     float64 // 平滑后的 pps
	lastAt   time.Time
}

var (
	etaMu     sync.Mutex
	etaStates = map[uint]*etaState{}
)

// etaSampleInterval 与 reaper tick 一致（10s）；滚动窗口 ≈ 3 个采样点（30s）的 EWMA。
const etaSampleInterval = 10 * time.Second

// SampleQueueETA 每 reaper tick 调一次：对全部在途队列任务采样完成增量并平滑。
func SampleQueueETA() {
	var tasks []model.ScanTask
	config.DB.Where("queue_mode = ? AND status = ?", true, "running").Find(&tasks)
	now := time.Now()
	for _, t := range tasks {
		s := QueueStatsOf(t.ID)
		etaMu.Lock()
		st := etaStates[t.ID]
		if st == nil {
			st = &etaState{lastDone: s.ProbesDone, lastAt: now}
			etaStates[t.ID] = st
		}
		dt := now.Sub(st.lastAt).Seconds()
		if dt >= etaSampleInterval.Seconds()/2 {
			inst := float64(s.ProbesDone-st.lastDone) / dt
			if inst < 0 {
				inst = 0 // 重跑清零
			}
			const alpha = 0.3
			st.ewma = alpha*inst + (1-alpha)*st.ewma
			st.lastDone = s.ProbesDone
			st.lastAt = now
		}
		etaMu.Unlock()
	}
	// 清理不再在途的任务，防 map 无界增长
	etaMu.Lock()
	for id := range etaStates {
		live := false
		for _, t := range tasks {
			if t.ID == id {
				live = true
				break
			}
		}
		if !live {
			delete(etaStates, id)
		}
	}
	etaMu.Unlock()
}

// QueueProgress 队列任务的精确进度与 ETA。
// 返回 percent(0-100)、etaSeconds（-1=已暂停或无速率样本）。
func QueueProgress(taskID uint) (percent int, etaSeconds int64) {
	s := QueueStatsOf(taskID)

	// 还没有单元 = 还在存活探测阶段（或者刚启动）。此时按"已探 IP / 总 IP"算，
	// 否则界面会显示一个永远 0% 的进度条，让人以为卡死了。
	if s.Total == 0 {
		var t model.ScanTask
		if err := config.DB.Select("id", "total_ips", "probe_offset").First(&t, taskID).Error; err == nil &&
			t.TotalIPs > 0 {
			percent = t.ProbeOffset * 100 / t.TotalIPs
			if percent > 99 {
				percent = 99 // 探测完还要等入队，别提前显示 100%
			}
			return percent, -1
		}
		return 0, -1
	}

	// 扫描阶段：按**单元数**算，不按探针数算。
	// 探针口径下 94239 个 IP × 100 端口 = 942 万探针，前几十分钟都显示 0%；
	// 而且单 IP 全端口（65535）的一个单元就占 0.6% 的权重，进度会莫名跳。
	finished := s.Done + s.FailedUnit
	percent = int(finished * 100 / s.Total)
	if percent > 100 {
		percent = 100
	}
	rem := s.ProbesTotal - s.ProbesDone
	if rem <= 0 {
		return percent, 0
	}
	if TaskPausedNow(taskID) {
		return percent, -1
	}
	etaMu.Lock()
	st := etaStates[taskID]
	ewma := 0.0
	if st != nil {
		ewma = st.ewma
		// 采样过期（>3 个周期没更新=采样循环停过）按速率衰减到 0，避免陈旧高速率假 ETA
		if age := time.Since(st.lastAt); age > 3*etaSampleInterval {
			ewma = 0
		}
	}
	etaMu.Unlock()
	if ewma <= 0 {
		return percent, -1 // 尚无速率样本（刚开扫/暂停/pps=0）
	}
	eta := int64(float64(rem) / ewma)
	if eta > 86400 {
		eta = 86400 // 封顶一天，早期抖动/极慢目标不显示荒诞值
	}
	return percent, eta
}

// DropETAState 任务收敛/删除时清理采样态。
func DropETAState(taskID uint) {
	etaMu.Lock()
	delete(etaStates, taskID)
	etaMu.Unlock()
}
