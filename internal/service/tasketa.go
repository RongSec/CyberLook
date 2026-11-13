package service

// ===== 进度与剩余时间估算（读数层）=====
//
// 目标：任何时刻都能给出「大致还要多久」，而不是"刚开扫没数据 → 不显示"。
//
// 进度口径：
//   - 扫描阶段：已完成单元 ÷ 单元总数（按 IP 计）。按探针加权会让几万 IP 的任务
//     长时间停在 0%，且单 IP 全端口单元的权重是 top100 的 655 倍，进度会莫名跳。
//   - 存活探测阶段：已探 IP ÷ 总 IP。
//
// 速度与 ETA 用三路融合，解决"冷启动没样本"和"单点抖动"两个问题：
//
//	a) 短期速率 EWMA(10s 采样，α=0.3)：跟得上环境变化，但对单 IP 单元的锯齿敏感；
//	b) 全程平均（自 started_at 起）：冷启动立刻可用，稳，但对"先慢后快"反应迟；
//	c) 理论初值（并发 ÷ 假定单探针延迟）：连一个样本都没有时给个量级，标低置信度。
//
// 有 a 时 rate = 0.65a + 0.35b；只有 b 时用 b；都没有用 c。ETA = 剩余探针 ÷ rate。
// 置信度随样本量上升：样本 < 2 个 → low（前端显示"估算中"），>=2 → high。

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RongSec/CyberLook-Scan/portscan"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// ETAConfidence 估算置信度：low 表示还没攒够样本，前端应显示"估算中"而不是精确值。
type ETAConfidence string

const (
	ConfLow  ETAConfidence = "low"
	ConfHigh ETAConfidence = "high"
)

// ProgressSnapshot 一次取全：阶段、进度、速率、剩余时间。
type ProgressSnapshot struct {
	Phase        string        `json:"phase"`        // probing / scanning / done
	Percent      int           `json:"percent"`      // 0-100
	UnitsDone    int64         `json:"unitsDone"`    // 已完成单元数
	UnitsTotal   int64         `json:"unitsTotal"`   // 单元总数
	ProbesDone   int64         `json:"probesDone"`   // 已完成探针
	ProbesTotal  int64         `json:"probesTotal"`  // 探针总数
	PPS          float64       `json:"pps"`          // 当前估算速率（探针/秒）
	ETASeconds   int64         `json:"etaSeconds"`   // -1=暂停/无法估算
	Confidence   ETAConfidence `json:"confidence"`   // low / high
	InFlight     int64         `json:"inFlight"`     // 正在被节点处理的单元数
	AssumedDelay float64       `json:"-"`            // 理论初值用的假定延迟（秒），仅日志用
}

type etaState struct {
	lastDone int64     // 上次采样时的 probes_done
	ewma     float64   // 短期速率（探针/秒）
	lastAt   time.Time // 上次采样时间
	samples  int       // 有效采样次数
	started  time.Time // 首次采样（近似任务开扫时间）
	startDon int64     // 首次采样时的 probes_done
}

var (
	etaMu     sync.Mutex
	etaStates = map[uint]*etaState{}
)

// etaSampleInterval 与 reaper tick 一致（10s）。
const etaSampleInterval = 10 * time.Second

// assumedProbeDelay 理论初值用的单探针平均耗时：
// 局域网里被拒绝的端口是毫秒级，被过滤的要等满超时，混合下来 0.5~1s 是常见量级。
// 只用于"一个样本都没有"的头几秒，随后立刻被实测速率取代。
const assumedProbeDelay = 0.8

// SampleQueueETA 每 reaper tick 调一次：采样完成增量、平滑速率。
func SampleQueueETA() {
	var tasks []model.ScanTask
	config.DB.Where("queue_mode = ? AND status = ?", true, "running").Find(&tasks)
	now := time.Now()
	live := make(map[uint]bool, len(tasks))

	for _, t := range tasks {
		live[t.ID] = true
		s := QueueStatsOf(t.ID)
		etaMu.Lock()
		st := etaStates[t.ID]
		if st == nil {
			st = &etaState{lastDone: s.ProbesDone, lastAt: now, started: now, startDon: s.ProbesDone}
			etaStates[t.ID] = st
			etaMu.Unlock()
			continue
		}
		dt := now.Sub(st.lastAt).Seconds()
		if dt >= etaSampleInterval.Seconds()/2 {
			inst := float64(s.ProbesDone-st.lastDone) / dt
			if inst < 0 {
				inst = 0 // 重跑清零
			}
			const alpha = 0.3
			st.ewma = alpha*inst + (1-alpha)*st.ewma
			st.samples++
			st.lastDone = s.ProbesDone
			st.lastAt = now
		}
		etaMu.Unlock()
	}

	// 清理不再在途的任务，防 map 无界增长
	etaMu.Lock()
	for id := range etaStates {
		if !live[id] {
			delete(etaStates, id)
		}
	}
	etaMu.Unlock()
}

// Progress 返回任务当前的进度快照（阶段、百分比、速率、ETA、置信度）。
func Progress(taskID uint) ProgressSnapshot {
	s := QueueStatsOf(taskID)
	snap := ProgressSnapshot{
		UnitsDone:   s.Done + s.FailedUnit,
		UnitsTotal:  s.Total,
		ProbesDone:  s.ProbesDone,
		ProbesTotal: s.ProbesTotal,
		InFlight:    s.Leased,
		ETASeconds:  -1,
		Confidence:  ConfLow,
	}

	// ── 存活探测阶段：还没有单元 ──
	if s.Total == 0 {
		snap.Phase = "probing"
		var t model.ScanTask
		if err := config.DB.Select("id", "total_ips", "probe_offset", "started_at").
			First(&t, taskID).Error; err == nil && t.TotalIPs > 0 {
			snap.Percent = t.ProbeOffset * 100 / t.TotalIPs
			if snap.Percent > 99 {
				snap.Percent = 99 // 探测完还要建队，别提前 100%
			}
			// 探测阶段也估时间：已探 IP 速率 × 剩余 IP 数 × 每 IP 的探测成本。
			// 这里的"速率"是 IP/秒；探测耗时 ≈ IP 数 × 端口数 × 超时 ÷ 并发，
			// 用实测速率反推最准，写死公式会随目标网络丢包情况偏离。
			if t.StartedAt != nil {
				elapsed := time.Since(*t.StartedAt).Seconds()
				if elapsed > 2 && t.ProbeOffset > 0 {
					ipPerSec := float64(t.ProbeOffset) / elapsed
					if ipPerSec > 0 {
						rem := float64(t.TotalIPs - t.ProbeOffset)
						snap.ETASeconds = int64(rem / ipPerSec)
						snap.PPS = ipPerSec * float64(len(portscan.DefaultAlivePorts)) // 折算成探针/秒仅供展示
						if t.ProbeOffset >= t.TotalIPs/20 {                      // 至少探了 5% 才敢报 high
							snap.Confidence = ConfHigh
						}
					}
				}
			}
		}
		return snap
	}

	// ── 扫描阶段 ──
	snap.Phase = "scanning"
	if s.Total > 0 {
		snap.Percent = int(snap.UnitsDone * 100 / s.Total)
		if snap.Percent > 100 {
			snap.Percent = 100
		}
	}
	rem := s.ProbesTotal - s.ProbesDone
	if rem <= 0 {
		snap.Phase = "done"
		snap.Percent = 100
		snap.ETASeconds = 0
		snap.Confidence = ConfHigh
		return snap
	}
	if TaskPausedNow(taskID) {
		return snap // 暂停：ETA 保持 -1，前端显示"已暂停"
	}

	etaMu.Lock()
	st := etaStates[taskID]
	var shortRate, overallRate float64
	samples := 0
	if st != nil {
		samples = st.samples
		// 短期速率：超过 3 个采样周期没更新视为陈旧，丢弃避免假乐观
		if time.Since(st.lastAt) <= 3*etaSampleInterval {
			shortRate = st.ewma
		}
		// 全程平均：从首次采样到现在，冷启动立刻有值
		if el := time.Since(st.started).Seconds(); el > 2 {
			overallRate = float64(s.ProbesDone-st.startDon) / el
		}
	}
	etaMu.Unlock()

	// 三路融合
	var rate float64
	switch {
	case shortRate > 0 && overallRate > 0:
		rate = 0.65*shortRate + 0.35*overallRate
	case shortRate > 0:
		rate = shortRate
	case overallRate > 0:
		rate = overallRate
	default:
		// 一个样本都没有：用理论初值（并发 ÷ 假定延迟），标注低置信度
		if c := int64(NodeConcurrencyOf(taskID)); c > 0 {
			rate = float64(c) / assumedProbeDelay
			snap.AssumedDelay = assumedProbeDelay
		}
	}

	if rate <= 0 {
		return snap
	}
	snap.PPS = rate
	snap.ETASeconds = int64(float64(rem) / rate)
	if snap.ETASeconds < 1 {
		snap.ETASeconds = 1
	}
	if samples >= 2 {
		snap.Confidence = ConfHigh
	}
	return snap
}

// QueueProgress 兼容旧签名：只取百分比与 ETA。
func QueueProgress(taskID uint) (percent int, etaSeconds int64) {
	s := Progress(taskID)
	return s.Percent, s.ETASeconds
}

// DropETAState 任务收敛/删除时清理采样态。
func DropETAState(taskID uint) {
	etaMu.Lock()
	delete(etaStates, taskID)
	etaMu.Unlock()
}

// NodeConcurrencyOf 该任务执行节点的并发上限（用于理论初值）。
// 取节点配置，读不到给 600（默认节点的常见值），只为量级估计。
func NodeConcurrencyOf(taskID uint) int {
	var t model.ScanTask
	if err := config.DB.Select("id", "node_ids").First(&t, taskID).Error; err != nil {
		return 600
	}
	if t.NodeIDs == "" || t.NodeIDs == "all" {
		var n model.Node
		if err := config.DB.Select("id", "concurrency").Order("last_heartbeat desc").First(&n).Error; err == nil {
			if n.Concurrency > 0 {
				return n.Concurrency
			}
		}
		return 600
	}
	var id uint
	for _, part := range strings.Split(t.NodeIDs, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && v > 0 {
			id = uint(v)
			break
		}
	}
	if id == 0 {
		return 600
	}
	var n model.Node
	if err := config.DB.Select("id", "concurrency").First(&n, id).Error; err == nil && n.Concurrency > 0 {
		return n.Concurrency
	}
	return 600
}
