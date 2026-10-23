package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// SnapshotDir 截图存储目录
const SnapshotDir = "data/snapshots"

// CaptureWebServices 对当前库内所有 web 服务截图
func CaptureWebServices() (int, int, error) {
	var svcs []model.Service
	config.DB.Where("(protocol ILIKE '%http%' OR service ILIKE '%http%' OR title <> '' OR status_code <> '')").
		Where("ip::text NOT LIKE '127.%'").Find(&svcs)

	_ = os.MkdirAll(SnapshotDir, 0o755)
	ok, fail := 0, 0
	for _, svc := range svcs {
		url := fmt.Sprintf("http://%s:%d", svc.IP, svc.Port)
		if err := captureOne(url, svc); err == nil {
			ok++
		} else {
			fail++
		}
	}
	return ok, fail, nil
}

// captureOne 用 chromium 无头模式截图单个 URL
func captureOne(url string, svc model.Service) error {
	now := time.Now()
	name := fmt.Sprintf("%s_%d_%d.png", strings.ReplaceAll(svc.IP, ".", "_"), svc.Port, now.Unix())
	path := filepath.Join(SnapshotDir, name)

	cmd := exec.Command("chromium",
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
		// 跳过自签/过期证书的拦截页（否则截到 "Your connection is not private"）
		"--ignore-certificate-errors",
		"--ignore-certificate-errors-spki-list",
		"--allow-running-insecure-content",
		"--hide-scrollbars",
		// 用 1920x1080 + 固定缩放，避免页面被拉伸或截不全
		"--window-size=1920,1080",
		"--force-device-scale-factor=1",
		"--disable-features=IsolateOrigins,site-per-process",
		"--virtual-time-budget=8000",
		"--screenshot="+path, url)
	cmd.Env = append(os.Environ(), "HOME=/tmp")
	if err := cmd.Run(); err != nil {
		return err
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return fmt.Errorf("截图文件为空")
	}

	config.DB.Create(&model.PageSnapshot{
		AssetID: svc.AssetID, URL: url, Title: svc.Title, ImagePath: path, CapturedAt: now,
	})
	return nil
}

// CleanOldSnapshots 清理 N 天前的截图文件与记录
func CleanOldSnapshots(days int) int {
	cutoff := time.Now().AddDate(0, 0, -days)
	var snaps []model.PageSnapshot
	config.DB.Where("captured_at < ?", cutoff).Find(&snaps)
	removed := 0
	for _, s := range snaps {
		if s.ImagePath != "" {
			_ = os.Remove(s.ImagePath)
		}
		config.DB.Delete(&model.PageSnapshot{}, s.ID)
		removed++
	}
	return removed
}

// DeleteSnapshotFile 删除截图文件
func DeleteSnapshotFile(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

// SnapshotSettings 截图设置
type SnapshotSettings struct {
	Enabled       bool `json:"enabled"`
	IntervalDays  int  `json:"intervalDays"`
	RetentionDays int  `json:"retentionDays"`
}

var currentSnapshotSettings = SnapshotSettings{Enabled: false, IntervalDays: 7, RetentionDays: 30}

// GetSnapshotSettings 获取截图设置
func GetSnapshotSettings() SnapshotSettings { return currentSnapshotSettings }

// SetSnapshotSettings 更新截图设置
func SetSnapshotSettings(s SnapshotSettings) { currentSnapshotSettings = s }
