package main

import (
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
		r.StaticFile("/favicon.ico", "web/dist/favicon.ico")
		r.StaticFile("/favicon.svg", "web/dist/favicon.svg")
		r.StaticFile("/", "web/dist/index.html")
		r.NoRoute(func(c *gin.Context) {
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
