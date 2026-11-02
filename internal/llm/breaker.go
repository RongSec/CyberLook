package llm

import (
	"fmt"
	"sync"
	"time"
)

// BreakerState 单个 LLM 的熔断器状态
type BreakerState struct {
	Fails     int       // 连续失败次数
	Trips     int       // 累计熔断次数
	OpenUntil time.Time // 熔断到期时间（零值 = 关闭）
	LastError string
	LastAt    time.Time
	LatencyMs int // 最近一次成功调用耗时
}

// Open 是否处于熔断打开状态
func (s BreakerState) Open() bool {
	return time.Now().Before(s.OpenUntil)
}

// Breaker 进程级熔断器注册表（所有 profile 共享）
type Breaker struct {
	mu sync.Mutex
	m  map[uint]*BreakerState
}

var defaultBreaker = &Breaker{m: map[uint]*BreakerState{}}

// GlobalBreaker 返回全局熔断器
func GlobalBreaker() *Breaker { return defaultBreaker }

// Available 该 profile 是否可用（未熔断）
func (b *Breaker) Available(id uint) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st, ok := b.m[id]; ok {
		return !st.Open()
	}
	return true
}

// Snapshot 读取状态快照
func (b *Breaker) Snapshot(id uint) BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st, ok := b.m[id]; ok {
		return *st
	}
	return BreakerState{}
}

// Success 记录一次成功（清空连续失败）
func (b *Breaker) Success(id uint, latency time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.get(id)
	st.Fails = 0
	st.LastError = ""
	st.LatencyMs = int(latency.Milliseconds())
	st.LastAt = time.Now()
}

// Fail 记录一次失败；达到阈值则熔断
//
// deterministic=true 表示确定性失败（密钥无效、无余额、模型不存在），首次即熔断；
// false 表示瞬时失败（429/5xx/网络），连续 failThreshold 次才熔断。
func (b *Breaker) Fail(id uint, err error, failThreshold, cooldownSeconds int, deterministic bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	st := b.get(id)
	st.Fails++
	st.LastError = err.Error()
	st.LastAt = time.Now()

	threshold := failThreshold
	if threshold <= 0 {
		threshold = 3
	}
	shouldTrip := deterministic || st.Fails >= threshold

	if shouldTrip {
		cd := cooldownSeconds
		if cd <= 0 {
			// 阶梯退避：1min → 5min → 30min
			ladder := []int{60, 300, 1800}
			idx := st.Trips
			if idx >= len(ladder) {
				idx = len(ladder) - 1
			}
			cd = ladder[idx]
		}
		st.Trips++
		st.OpenUntil = time.Now().Add(time.Duration(cd) * time.Second)
		st.Fails = 0
	}
}

// Reset 手动恢复（清除熔断）
func (b *Breaker) Reset(id uint) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.m, id)
}

// ResetAll 全部恢复
func (b *Breaker) ResetAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m = map[uint]*BreakerState{}
}

func (b *Breaker) get(id uint) *BreakerState {
	st, ok := b.m[id]
	if !ok {
		st = &BreakerState{}
		b.m[id] = st
	}
	return st
}

// IsDeterministic 判断错误是否确定性（不该重试）
func IsDeterministic(statusCode int, msg string) bool {
	switch statusCode {
	case 401, 403: // 密钥无效/无权限
		return true
	case 404: // 模型不存在
		return true
	case 402: // 无余额
		return true
	}
	// 消息里包含关键字的也算确定性
	for _, kw := range []string{
		"invalid_api_key", "insufficient_quota", "account_deactivated",
		"model_not_found", "no such model", "billing",
	} {
		if containsFold(msg, kw) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	ls, lsub := lower(s), lower(sub)
	for i := 0; i+len(lsub) <= len(ls); i++ {
		if ls[i:i+len(lsub)] == lsub {
			return true
		}
	}
	return false
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// ErrNoProfile 没有可用配置
var ErrNoProfile = fmt.Errorf("没有可用的 LLM 配置（全部熔断或未启用）")
