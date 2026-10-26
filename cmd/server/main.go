package main

import (
	"path"
	"fmt"
	"asset-platform/internal/model"
	"log"
	"net/http"
	"os"
	"time"

	"asset-platform/internal/api"
	"asset-platform/internal/config"
	"asset-platform/internal/scanner"
	"asset-platform/internal/scheduler"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

func main() {
	cfg := config.Load()

	// 初始化数据库（失败仅告警，便于开发阶段先起服务）
	config.LatestAgentVersion = config.ComputeAgentVersion()
	log.Printf("[agent] 当前控制文件版本: %s", config.LatestAgentVersion)

	if err := config.InitDB(cfg.PGDSN); err != nil {
		log.Printf("[warn] PostgreSQL 连接失败（服务仍会启动，DB 相关接口暂不可用）: %v", err)
	} else {
		config.RunMigrations("migrations")
		if err := service.InitDefaultUser(); err != nil {
			log.Printf("[warn] 默认用户初始化失败: %v", err)
		}
		if cfg.TscanClientPath != "" {
			scanner.SetTscanClientPath(cfg.TscanClientPath)
		}
		service.CleanupStaleRuns()   // 收尾上一轮残留的执行记录
		service.StartFaultsCleanup() // 每日清理过期故障记录（默认 30 天）
		service.CleanupOldFaults()       // 启动时先清一次
		service.CleanupOrphanPipelines() // 清理崩溃残留的孤儿断点
		// 只在「有运行中任务被中断」时才记录故障，正常启动/部署重启不是故障
		var runningCnt int64
		config.DB.Model(&model.ScanTask{}).Where("status = ?", "running").Count(&runningCnt)
		if runningCnt > 0 {
			service.RecordFault("server_restart", 0, "", 0,
				fmt.Sprintf("服务端重启，%d 个运行中任务被中断（已从断点恢复）", runningCnt))
		}
		// 断点续传：启动时把所有 running 任务的流水线恢复起来，
		// 会从 data/pipeline/<id>.json 读回 offset 与待下发池，继续探测/派发
		go func() {
			var running []model.ScanTask
			config.DB.Where("status = ?", "running").Find(&running)
			for _, t := range running {
				log.Printf("[恢复] 任务 #%d（%s）运行中，从断点恢复流水线", t.ID, t.Name)
				service.DispatchNextBatch(t.ID)
			}
		}()
		// 每 30 秒巡检：节点离线/熔断时，把它名下未完成的分片收回重新派发
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for range t.C {
				if n := service.RescheduleOrphanShards(); n > 0 {
					log.Printf("[巡检] 收回 %d 个孤儿分片并重新派发", n)
				}
			}
		}()
		scheduler.Start()
	}

	r := gin.Default()

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "asset-platform"})
	})

	api.RegisterRoutes(r)

	// 静态文件服务（前端构建产物 web/dist 存在时，生产模式单端口部署）
	if _, err := os.Stat("web/dist/index.html"); err == nil {
		r.Static("/static", "web/dist/static")
		r.Static("/fonts", "web/dist/fonts") // 自托管字体（MiSans / 霞鹜文楷）
		for _, f := range []string{"favicon.ico", "favicon.svg", "logo-transparent.png", "logo-dark.png", "logo-256.png"} {
			if _, err := os.Stat("web/dist/" + f); err == nil {
				r.StaticFile("/"+f, "web/dist/"+f)
			}
		}
		r.StaticFile("/", "web/dist/index.html")
		r.NoRoute(func(c *gin.Context) {
			// 带扩展名的资源不存在时必须 404，
			// 否则会把 index.html 当作 css/js/woff2 返回（浏览器静默解析失败）
			if ext := path.Ext(c.Request.URL.Path); ext != "" && ext != ".html" {
				c.Status(http.StatusNotFound)
				return
			}
			c.File("web/dist/index.html") // SPA 回退
		})
		log.Printf("[asset-platform] 已启用前端静态服务 (web/dist)")
	}

	// 后台清理：每天清理 30 天前的扫描报告文件
	go func() {
		for {
			if n := service.CleanOldReports(30); n > 0 {
				log.Printf("[cleanup] 已清理过期扫描报告 %d 个", n)
			}
			time.Sleep(24 * time.Hour)
		}
	}()

	// 后台定期截图 + 定期清理快照（每 6 小时检查一次）
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		lastCapture := time.Now()
		for range ticker.C {
			s := service.GetSnapshotSettings()
			if s.Enabled && time.Since(lastCapture) >= time.Duration(s.IntervalDays)*24*time.Hour {
				if ok, fail, err := service.CaptureWebServices(); err == nil {
					log.Printf("[snapshot] 定期截图完成：成功 %d，失败 %d", ok, fail)
				}
				lastCapture = time.Now()
			}
			if n := service.CleanOldSnapshots(s.RetentionDays); n > 0 {
				log.Printf("[snapshot] 清理过期快照 %d 个", n)
			}
		}
	}()

	// system_settings.port 优先于环境变量（系统配置页可改，改后需重启）
	if config.DB != nil {
		var ss model.SystemSettings
		if err := config.DB.First(&ss, 1).Error; err == nil && ss.Port > 0 {
			cfg.ServerPort = fmt.Sprint(ss.Port)
		}
	}
	addr := ":" + cfg.ServerPort
	log.Printf("[asset-platform] 服务启动于 %s", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second, // 防慢速请求堆积
		ReadTimeout:       0,                // SSE / 大文件上传需要长连接
		WriteTimeout:      0,                // SSE 长连接不能设写超时
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务启动失败: %v", err)
	}
}
