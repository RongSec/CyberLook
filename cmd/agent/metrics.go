package main

// 运行状态自报（每 60s 一行）—— 用于判断"有没有泄漏"，而不是靠猜。
//
// 为什么必须有：内存/goroutine 泄漏的特点是"慢"，出事时通常已经跑了几小时。
// 等到 OOM 才回头看日志，什么证据都没有。这行日志给出四个直接指标的趋势：
//
//	goroutines  持续单调上涨 = 协程没退出（最常见的是阻塞在 channel/Cond 上）
//	heap        持续上涨不回落 = 有引用没释放（缓存/切片/闭包捕获）
//	rss         比 heap 涨得快很多 = 有非 Go 堆的占用（cgo/sqlite/大量 socket 缓冲）
//	fds         持续上涨 = 连接或文件没关（每个在途探针也占一个，空闲时应接近基线）
//
// 只在 agent 侧打（server 侧用 docker stats 足够），且不引入 pprof 暴露面。

import (
	"log"
	"os"
	"runtime"
	"sync"
	"time"
)

var metricsOnce sync.Once

func startMetricsLogger() {
	metricsOnce.Do(func() {
		go func() {
			tk := time.NewTicker(60 * time.Second)
			defer tk.Stop()

			var lastG, lastFd int
			var warnG, warnFd int
			for range tk.C {
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				g := runtime.NumGoroutine()
				fd := countFDs()

				log.Printf("[agent] 运行状态 goroutines=%d heap=%.1fMB rss=%.1fMB fds=%d 在途上限=%d",
					g, float64(ms.HeapAlloc)/1048576, rssMB(), fd, autoInflight())

				// 连续 5 次（5 分钟）单调上涨才警告：避免被突发任务量的正常波动误报
				if lastG > 0 && g > lastG {
					warnG++
				} else {
					warnG = 0
				}
				if lastFd > 0 && fd > lastFd {
					warnFd++
				} else {
					warnFd = 0
				}
				if warnG >= 5 {
					log.Printf("[agent] ⚠️ goroutines 连续 5 分钟上涨（%d→%d），疑似协程泄漏，请抓 goroutine dump", lastG, g)
					warnG = 0
				}
				if warnFd >= 5 {
					log.Printf("[agent] ⚠️ fds 连续 5 分钟上涨（%d→%d），疑似连接/文件未关闭", lastFd, fd)
					warnFd = 0
				}
				lastG, lastFd = g, fd
			}
		}()
	})
}

// countFDs 当前进程打开的 fd 数（Linux）。非 Linux 返回 0（不显示）。
func countFDs() int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	return len(ents)
}

// rssMB 常驻内存（Linux /proc/self/status 的 VmRSS）。读不到返回 0。
func rssMB() float64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	s := string(b)
	const key = "VmRSS:"
	for i := 0; i+len(key) <= len(s); i++ {
		if s[i:i+len(key)] != key {
			continue
		}
		j := i + len(key)
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		k := j
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k > j {
			kb := 0
			for _, c := range s[j:k] {
				kb = kb*10 + int(c-'0')
			}
			return float64(kb) / 1024
		}
		break
	}
	return 0
}
