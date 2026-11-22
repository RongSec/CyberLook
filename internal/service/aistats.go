package service

// 识别链路的运行指标与队列可见性（v0.0.51）
//
// 背景：识别队列积压在界面上完全不可见——用户只看到"有些服务没名字"，看不到
// "还有多少条在队列里、一条要多久、卡在哪一步"。而识别本身是 4 步串行
// （抓包 → 模型 → 渲染 → 模型），最慢的渲染能占 60% 以上，单 worker 下吞吐
// 只有 1~3 条/分钟，几千条 backlog 就是几十小时。
//
// 这里只做两件事：
//  1. 计数器：入队/完成/走了渲染/渲染起了决定性作用/模型失败，以及四步平均耗时；
//  2. 队列快照：内存队列长度 + 库里待识别条数 + 最近一小时完成数（用于算 ETA）。
//
// 不落库、不写文件：进程内统计，重启归零。目的是回答"为什么慢"，不是做长周期报表。

import (
	"sync"
	"sync/atomic"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

type aiMetrics struct {
	enqueued   atomic.Int64
	done       atomic.Int64
	dropped    atomic.Int64 // 队列满丢弃
	renderUsed    atomic.Int64
	renderSkipped atomic.Int64 // 闸门判定"不值得渲染"而跳过
	renderWon  atomic.Int64 // 渲染后拿到了名字（说明渲染这一步有价值）
	llmFail    atomic.Int64
	nonHTTP    atomic.Int64

	traceMs  atomic.Int64
	llm1Ms   atomic.Int64
	renderMs atomic.Int64
	llm2Ms   atomic.Int64
	totalMs  atomic.Int64
	slowest  atomic.Int64 // 单条最长耗时

	// 最近若干次完成的时刻，用来算「当前」吞吐（每 60 秒的滑动窗口）。
	// 不这样做就只能拿"最近一小时写回数 / 60"当速率，积压刚被消化时严重低估。
	recentMu   sync.Mutex
	recentDone []time.Time
}

var aiMetricsRef aiMetrics

// aiMetricEnqueued 入队计数（含被丢弃的）
func aiMetricEnqueued() { aiMetricsRef.enqueued.Add(1) }

// aiMetricDropped 队列满丢弃
func aiMetricDropped() { aiMetricsRef.dropped.Add(1) }

// aiMetricNote 一条识别结束后的记账
func aiMetricNote(r IdentResult, total time.Duration) {
	m := &aiMetricsRef
	m.done.Add(1)
	m.recentMu.Lock()
	m.recentDone = append(m.recentDone, time.Now())
	if n := len(m.recentDone); n > 240 {
		m.recentDone = m.recentDone[n-240:]
	}
	m.recentMu.Unlock()
	m.totalMs.Add(total.Milliseconds())
	if total.Milliseconds() > m.slowest.Load() {
		m.slowest.Store(total.Milliseconds())
	}
	m.traceMs.Add(r.Timing["trace"])
	m.llm1Ms.Add(r.Timing["llm1"])
	m.renderMs.Add(r.Timing["render"])
	m.llm2Ms.Add(r.Timing["llm2"])
	if r.Timing["render"] > 0 {
		m.renderUsed.Add(1)
		if r.Confident {
			m.renderWon.Add(1)
		}
	}
}

func aiMetricLLMFail() { aiMetricsRef.llmFail.Add(1) }

// aiMetricRenderSkipped 渲染闸门跳过的条数（省下来的渲染时间）
func aiMetricRenderSkipped() { aiMetricsRef.renderSkipped.Add(1) }
func aiMetricNonHTTP() { aiMetricsRef.nonHTTP.Add(1) }

// AIQueueStats 队列与吞吐快照（给 /api/services/ai-stats）。
//
// backlog 是**库里**待识别的条数（ai_need 且未识别），不是内存队列长度：
// 进程重启后内存队列清空，但库里那些条目仍然"没名字"，必须一起算进去，
// 否则用户看到的数字会在重启后突然归零。
func AIQueueStats() map[string]interface{} {
	m := &aiMetricsRef
	done := m.done.Load()
	avg := func(v int64) int64 {
		if done == 0 {
			return 0
		}
		return v / done
	}

	qLen, qCap := 0, 0
	// 只读状态：aiQueueOnce 绝不在这里触发，否则会把 worker 启动的 once 提前占掉
	if aiQueue != nil {
		qLen, qCap = len(aiQueue), cap(aiQueue)
	}

	// 只统计 HTTP(S)：AI 识别链路只覆盖 web，非 HTTP 的 ai_need 是引擎按"证据不明"
	// 打上的，识别流程一碰就会直接标 ai_checked（不做任何事）。把它们算进积压会
	// 得到"7,461 条待识别"这种吓人的假数字（实测 6,674/7,461 都是非 HTTP）。
	var backlog int64
	config.DB.Model(&model.Service{}).
		Where("ai_need = ? AND ai_checked_at IS NULL AND protocol ILIKE 'http%'", true).Count(&backlog)

	var backlogNonHTTP int64
	config.DB.Model(&model.Service{}).
		Where("ai_need = ? AND ai_checked_at IS NULL AND protocol NOT ILIKE 'http%'", true).Count(&backlogNonHTTP)

	var recent int64
	config.DB.Model(&model.Service{}).
		Where("ai_checked_at > ?", time.Now().Add(-time.Hour)).Count(&recent)

	workers := aiIdentifyWorkers()
	// 吞吐：优先用最近 5 分钟的滑动窗口（反映"现在多快"），窗口内样本不足再退回
	// "最近一小时写回数 / 60"。前者是给用户看 ETA 的，后者是冷启动兜底。
	perMin := float64(recent) / 60.0
	m.recentMu.Lock()
	cut := time.Now().Add(-5 * time.Minute)
	latest := 0
	for _, t := range m.recentDone {
		if t.After(cut) {
			latest++
		}
	}
	m.recentMu.Unlock()
	if latest >= 5 {
		perMin = float64(latest) / 5.0
	}
	etaSec := int64(-1)
	if perMin > 0.05 {
		// perMin 是"每分钟条数"，换算成秒要乘 60（v0.0.51 修正：原来少乘 60，
		// 界面上会显示"13 分钟"实际要 13 小时）
		etaSec = int64(float64(backlog) / perMin * 60)
	}

	var renderWonPct float64
	if used := m.renderUsed.Load(); used > 0 {
		renderWonPct = float64(m.renderWon.Load()) * 100 / float64(used)
	}

	return map[string]interface{}{
		"workers":          workers,
		"queueLen":         qLen,
		"queueCap":         qCap,
		"backlog":          backlog,        // 库里待识别（仅 HTTP）
		"backlogNonHTTP":   backlogNonHTTP, // 非 HTTP 的 ai_need（识别链路不处理，不计入 ETA）
		"doneThisHour":     recent,      // 最近一小时写回数
		"doneTotal":        done,        // 本进程完成数
		"dropped":          m.dropped.Load(),
		"renderUsed":       m.renderUsed.Load(),
		"renderSkipped":    m.renderSkipped.Load(),
		"renderWonPct":     renderWonPct, // 走了渲染、且渲染后确实给出名字的比例
		"llmFail":          m.llmFail.Load(),
		"nonHTTP":          m.nonHTTP.Load(),
		"avgTotalMs":       avg(m.totalMs.Load()),
		"avgTraceMs":       avg(m.traceMs.Load()),
		"avgLLM1Ms":        avg(m.llm1Ms.Load()),
		"avgRenderMs":      avg(m.renderMs.Load()),
		"avgLLM2Ms":        avg(m.llm2Ms.Load()),
		"slowestMs":        m.slowest.Load(),
		"perMinute":        float64(int(perMin*100)) / 100,
		"etaSeconds":       etaSec,
	}
}
