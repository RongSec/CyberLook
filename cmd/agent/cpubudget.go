package main

// CPU 预算：节点进程主动让出一部分核给同机的平台组件。
//
// 背景（v0.0.50 实测）：平台中心（web + AI 识别 + chromium 渲染 + PostgreSQL）与扫描节点
// 混部在一台 4 核虚拟机上。扫描一开、智能调速把在途探针推到高位后，Go 运行时默认
// 用满全部核做调度和 GC，加上 chromium 渲染的尖峰，整机被吃满——最直接的表现是
// **平台 web 页面打不开**（连接能建立，但请求排不上队）。
//
// 处理办法两条，互相补充：
//  1. 这里把 GOMAXPROCS 收窄到「本机核数 - 预留核数」，让出完整的一个核
//     （4 核 → 3，8 核 → 6），web/DB 的请求始终有 CPU 可跑；
//  2. 智能调速改成看整机 CPU 并设预算（见 autotune.go），超预算就退在途并发。
//
// 只让一点，不让太多：4 核以上留 25%（1~2 核），2 核及以下不留——小机器再留一核，
// 扫描吞吐会掉到不可用。要调整用环境变量：
//
//	SCAN_CPU_RESERVE_CORES=0   # 完全不让（独占扫描机器）
//	SCAN_CPU_RESERVE_CORES=2   # 多让一核（web 压力特别大的混部场景）
//
// 显式设置 GOMAXPROCS 时本函数不覆盖，尊重运维的显式配置。

import (
	"log"
	"os"
	"runtime"
)

func applyCPUBudget() {
	cpuN := runtime.NumCPU()
	if cpuN < 1 {
		cpuN = 1
	}
	res := cpuReserveCores(cpuN)
	target := cpuN - res
	if target < 1 {
		target = 1
	}
	if _, explicit := os.LookupEnv("GOMAXPROCS"); explicit {
		log.Printf("[节点] CPU 预算：本机 %d 核，检测到显式 GOMAXPROCS=%d，按运维配置执行（预留核数策略跳过）",
			cpuN, runtime.GOMAXPROCS(0))
		return
	}
	if res > 0 && target < runtime.GOMAXPROCS(0) {
		runtime.GOMAXPROCS(target)
	}
	log.Printf("[节点] CPU 预算：本机 %d 核，预留 %d 核给平台（web/DB/识别），扫描进程 GOMAXPROCS=%d，整机占用预算 %.0f%%",
		cpuN, res, runtime.GOMAXPROCS(0), autoBusyBudgetPct(cpuN, res))
}

// autoBusyBudgetPct 与 autoTuner.busyBudget 同口径（未启动调速时的日志用）。
func autoBusyBudgetPct(cpuN, res int) float64 {
	if cpuN < 1 {
		cpuN = 1
	}
	cores := cpuN - res
	if cores < 1 {
		cores = 1
	}
	b := float64(cores) / float64(cpuN) * 100
	if b > 92 {
		b = 92
	}
	return b
}
