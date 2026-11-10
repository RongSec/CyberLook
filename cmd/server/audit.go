package main

// 识别链路审计：拿真实服务跑一遍「规则 → AI 判充分性 → 抓包 → 渲染 → AI」，
// **只输出报告，不写 services/assets 表**，用于评估识别工作流的准确率与耗时分布。
//
// 用法（容器内）：/app/asset-server audit-identify --unclear 100 --clear 30 --workers 4

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sync"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"
)

type auditItem struct {
	IP        string   `json:"ip"`
	Port      int      `json:"port"`
	Protocol  string   `json:"protocol"`
	OldTitle  string   `json:"oldTitle"`
	OldFP     string   `json:"oldFingerprint"`
	WasClear  bool     `json:"wasClear"` // 采样分组：true=清晰组（应停在第 0 级）
	StageHits []string `json:"stageHits"`
	FinalTitle string  `json:"finalTitle"`
	Changed   bool     `json:"changed"` // 最终标题是否会被写回（仅报告，不落库）
	CostMs    int64    `json:"costMs"`
	Err       string   `json:"err,omitempty"`
}

func runAuditIdentify(args []string) {
	fs := flag.NewFlagSet("audit-identify", flag.ExitOnError)
	nUnclear := fs.Int("unclear", 100, "采样多少条「标题不清晰」的服务")
	nClear := fs.Int("clear", 30, "采样多少条「标题清晰」的服务（应停在第 0 级）")
	workers := fs.Int("workers", 4, "并发数")
	poolSize := fs.Int("pool", 3000, "候选池大小（从这个池子里按清晰度分组抽取）")
	outFile := fs.String("out", "", "报告输出文件（默认只打 stdout）")
	_ = fs.Parse(args)

	cfg := config.Load()
	if err := config.InitDB(cfg.PGDSN); err != nil {
		log.Fatalf("[audit] 连数据库失败: %v", err)
	}

	// ---- 采样：先取一批最近的 web 服务，再按 TitleClear 分组 ----
	var pool []model.Service
	config.DB.Where("protocol ILIKE 'http%'").
		Where("COALESCE(status_code,'') <> '' OR COALESCE(title,'') <> ''").
		Order("last_seen desc").Limit(*poolSize).Find(&pool)

	var unclear, clear []model.Service
	for _, s := range pool {
		if service.TitleClear(s.Title) {
			clear = append(clear, s)
		} else {
			unclear = append(unclear, s)
		}
	}
	rnd := rand.New(rand.NewSource(20261109))
	rnd.Shuffle(len(unclear), func(i, j int) { unclear[i], unclear[j] = unclear[j], unclear[i] })
	rnd.Shuffle(len(clear), func(i, j int) { clear[i], clear[j] = clear[j], clear[i] })
	if *nUnclear < len(unclear) {
		unclear = unclear[:*nUnclear]
	}
	if *nClear < len(clear) {
		clear = clear[:*nClear]
	}

	// 混在一起打乱顺序，模拟真实扫描队列（清晰/不清晰交替到达）
	jobs := make([]model.Service, 0, len(unclear)+len(clear))
	jobs = append(jobs, unclear...)
	jobs = append(jobs, clear...)
	rnd.Shuffle(len(jobs), func(i, j int) { jobs[i], jobs[j] = jobs[j], jobs[i] })

	var usageBefore int64
	config.DB.Model(&model.LLMUsage{}).Count(&usageBefore)

	fmt.Printf("[audit] 候选池 %d 条：不清晰 %d / 清晰 %d；本次抽样 不清晰 %d + 清晰 %d = %d，workers=%d\n",
		len(pool), len(unclear), len(clear), minInt(*nUnclear, len(unclear)), minInt(*nClear, len(clear)), len(jobs), *workers)

	results := make([]auditItem, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, *workers)
	t0 := time.Now()
	for i := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, svc model.Service) {
			defer wg.Done()
			defer func() { <-sem }()
			it := auditItem{IP: svc.IP, Port: svc.Port, Protocol: svc.Protocol,
				OldTitle: svc.Title, OldFP: svc.Fingerprint, WasClear: service.TitleClear(svc.Title)}
			st := time.Now()
			defer func() {
				if r := recover(); r != nil {
					it.Err = fmt.Sprintf("panic: %v", r)
				}
				it.CostMs = time.Since(st).Milliseconds()
				results[idx] = it
			}()
			title, fp, stages, _ := service.IdentifyService(svc)
			for _, s := range stages {
				mark := "✗"
				if s.OK {
					mark = "✓"
				}
				it.StageHits = append(it.StageHits, s.Stage+mark)
			}
			it.FinalTitle = title
			it.Changed = service.TitleClear(title) && title != svc.Title
			_ = fp
		}(i, jobs[i])
	}
	wg.Wait()
	elapsed := time.Since(t0)

	var usageAfter int64
	config.DB.Model(&model.LLMUsage{}).Count(&usageAfter)

	// ---- 汇总 ----
	var unclearN, clearN, unclearOK, clearOK, llmCalls0 int
	var sumMs int64
	stageHit := map[string]int{}
	for _, r := range results {
		if r.WasClear {
			clearN++
			if len(r.StageHits) == 1 && r.FinalTitle == r.OldTitle {
				clearOK++
			}
		} else {
			unclearN++
			if r.Changed {
				unclearOK++
			}
		}
		if len(r.StageHits) == 0 {
			llmCalls0++
		}
		sumMs += r.CostMs
		for _, h := range r.StageHits {
			stageHit[h]++
		}
		if r.Err != "" {
			log.Printf("[audit] %s:%d 失败: %s", r.IP, r.Port, r.Err)
		}
	}

	fmt.Printf("\n===== 汇总 =====\n")
	fmt.Printf("总耗时 %s，平均单条 %d ms\n", elapsed.Round(time.Second), sumMs/int64(maxInt(1, len(results))))
	fmt.Printf("清晰组 %d 条：停在第 0 级（0 次 LLM）%d 条（%.0f%%）\n",
		clearN, clearOK, pct(clearOK, clearN))
	fmt.Printf("不清晰组 %d 条：最终拿到可用标题 %d 条（%.0f%%）\n",
		unclearN, unclearOK, pct(unclearOK, unclearN))
	fmt.Printf("LLM 实际调用增量：%d 次（平均 %.2f 次/条）\n",
		usageAfter-usageBefore, float64(usageAfter-usageBefore)/float64(maxInt(1, len(results))))
	fmt.Printf("各级命中次数：\n")
	for k, v := range stageHit {
		fmt.Printf("  %-30s %d\n", k, v)
	}

	// ---- 明细 ----
	b, _ := json.MarshalIndent(results, "", "  ")
	if *outFile != "" {
		if err := os.WriteFile(*outFile, b, 0o644); err != nil {
			log.Printf("[audit] 写报告失败: %v", err)
		} else {
			fmt.Printf("\n明细已写入 %s\n", *outFile)
		}
	} else {
		fmt.Println("\n===== 明细 =====")
		fmt.Println(string(b))
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
