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
// 置信度随样本量上升：样本 < 3 个 → low（前端显示"估算中"），>=3 → high。
// 为什么是 3 而不是 1：队列刚起来的那一两个窗口还在爬坡（在途单元才领了几个），
// 用它算 ETA 会给出比实际大一个量级的数字，宁可先显示"估算中"。

import (
	"strconv"
	"strings"
	"sync"
	"time"

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

	// 存活探测阶段的独立采样（与扫描阶段互不污染：探测以 IP/秒 计）
	pLastOff  int    // 上次采样时的 probe_offset
	pEwma     float64 // 短期速率（IP/秒）
	pSamples  int     // 有效采样次数
	pEta      float64 // 平滑后的 ETA（秒）
	pLastMove time.Time // 最后一次进度真正变化的时刻
	pSeen     bool    // 是否已建立基线
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

// aliveBurstRate 存活探测阶段的"ICMP 爆发"判据（IP/秒）。
//
// 探测是两个阶段串起来的：ICMP 一批 1000 台、发完就收，94k 个 IP 两秒内就能把
// probe_offset 推到一半；随后 TCP 阶段逐台 connect，速率掉到 200 IP/秒 量级。
// 拿爆发期的速率算 ETA 会给出"剩余 2 秒"这种差两个数量级的假数字（实测就是这个问题），
// 所以突发的窗口一律不计入样本，只认 TCP 阶段的稳态速率。
const aliveBurstRate = 500.0

// aliveEtaStall 进度多久没变化就认为"暂时估不出来"（秒）。
// 探测收尾的抽样复核阶段会短时间冻结偏移量，这时候继续报一个不断缩小的 ETA 就是骗人。
const aliveEtaStall = 45

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
			st = &etaState{lastDone: s.ProbesDone, lastAt: now, started: now, startDon: s.ProbesDone,
				pLastMove: now, pSeen: true, pLastOff: t.ProbeOffset}
			etaStates[t.ID] = st
			etaMu.Unlock()
			continue
		}
		// 存活探测阶段（还没有单元）：按 IP 进度单独采样
		if s.Total == 0 && t.TotalIPs > 0 {
			sampleAliveETA(st, t.ProbeOffset, now)
			etaMu.Unlock()
			continue
		}
		dt := now.Sub(st.lastAt).Seconds()
		if dt >= etaSampleInterval.Seconds()/2 {
			inst := float64(s.ProbesDone-st.lastDone) / dt
			if inst < 0 {
				inst = 0 // 重跑清零
			}
			// 只有真正出活了的窗口才算样本：探测阶段（还没有单元）与刚开始那一两个
			// tick 的 inst 恒为 0，把它们算进来会让 confidence 提前变 high，
			// 界面就会拿着"0 探针/秒"算出一个 1.5 小时的假 ETA。
			if inst > 0 {
				const alpha = 0.3
				st.ewma = alpha*inst + (1-alpha)*st.ewma
				st.samples++
			}
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

// sampleAliveETA 存活探测阶段的速率采样（调用方持 etaMu）。
//
// 只采"稳态"窗口：探测脚本的进度是 ICMP 与 TCP 两段归一化后的值，
// ICMP 段是毫秒级批量回包（瞬时几千 IP/秒），TCP 段是逐台 connect（200 IP/秒 量级）。
// 两段混在一起做平均必然算错，所以爆发窗口直接丢弃，只用 TCP 段的样本。
func sampleAliveETA(st *etaState, off int, now time.Time) {
	if !st.pSeen {
		st.pSeen, st.pLastOff, st.pLastMove = true, off, now
		return
	}
	dt := now.Sub(st.pLastMove).Seconds()
	if dt < 1 {
		return
	}
	delta := off - st.pLastOff
	if delta > 0 {
		inst := float64(delta) / dt
		if inst > 0 && inst <= aliveBurstRate {
			const alpha = 0.4
			if st.pEwma == 0 {
				st.pEwma = inst
			} else {
				st.pEwma = alpha*inst + (1-alpha)*st.pEwma
			}
			st.pSamples++
		}
		// 爆发窗口也要推进基线：否则同一个爆发会在下一 tick 被再算一遍
		st.pLastOff = off
		st.pLastMove = now
	}
}


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
			snap.UnitsDone = int64(t.ProbeOffset)
			snap.UnitsTotal = int64(t.TotalIPs)
			// ETA 只用 TCP 阶段的稳态速率（见 sampleAliveETA）。
			// 早期实现用 probe_offset/elapsed，ICMP 批量回包那两秒会把速率放大几百倍，
			// 于是刚开探就显示"剩余 2 秒"，随后真实耗时却是 7 分 36 秒。
			etaMu.Lock()
			st := etaStates[taskID]
			var rate, eta float64
			samples, stalled := 0, false
			if st != nil {
				rate, samples = st.pEwma, st.pSamples
				if !st.pLastMove.IsZero() && time.Since(st.pLastMove).Seconds() > aliveEtaStall {
					stalled = true
				}
			}
			etaMu.Unlock()
			if rate > 0 && samples >= 2 {
				snap.PPS = rate // 探测阶段的速率单位是 IP/秒，前端按此展示
				rem := float64(t.TotalIPs - t.ProbeOffset)
				eta = rem / rate
				// ETA 自身也做平滑：单窗口抖动不该让剩余时间上下横跳
				etaMu.Lock()
				if st != nil {
					if st.pEta > 0 {
						st.pEta = 0.5*eta + 0.5*st.pEta
					} else {
						st.pEta = eta
					}
					eta = st.pEta
				}
				etaMu.Unlock()
				if stalled {
					// 进度冻结（抽样复核/网络卡顿）：不报不断缩小的假数字
					eta, samples = 0, 0
				}
			}
			if eta > 0 {
				if eta < 1 {
					eta = 1
				}
				snap.ETASeconds = int64(eta)
			}
			if samples >= 2 {
				snap.Confidence = ConfHigh
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
	if samples >= 3 {
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
