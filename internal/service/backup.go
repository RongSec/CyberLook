package service

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// 备份目录：compose 已把 ${HOST_PROJECT_DIR}/data/backup 挂到 /app/backup
func BackupDirPath() string {
	dir := os.Getenv("BACKUP_DIR")
	if dir == "" {
		dir = "/app/backup"
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func pgDSN() string { return os.Getenv("PG_DSN") }

// RunBackup 用 pg_dump 打一个 gzip 备份，返回文件名与大小
func RunBackup(reason string) (string, int64, error) {
	dsn := pgDSN()
	if dsn == "" {
		return "", 0, fmt.Errorf("PG_DSN 未配置")
	}
	name := fmt.Sprintf("asset-%s.sql.gz", time.Now().Format("20060102-150405"))
	path := filepath.Join(BackupDirPath(), name)
	// pg_dump 支持直接传连接串；gzip -9 压缩（数据量大时能省一半以上）
	cmd := exec.Command("sh", "-c", fmt.Sprintf("set -o pipefail; pg_dump '%s' | gzip -9 > '%s'", dsn, path))
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(path)
		return "", 0, fmt.Errorf("pg_dump 失败: %v %s", err, strings.TrimSpace(string(out)))
	}
	st, err := os.Stat(path)
	if err != nil {
		return name, 0, err
	}
	log.Printf("[backup] 已生成 %s（%s，%d KB）", name, reason, st.Size()/1024)
	return name, st.Size(), nil
}

// BackupItem 备份文件信息
type BackupItem struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// ListBackups 列出备份（按时间倒序）
func ListBackups() []BackupItem {
	entries, err := os.ReadDir(BackupDirPath())
	if err != nil {
		return nil
	}
	out := make([]BackupItem, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupItem{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime().Format("2006-01-02 15:04:05")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

func backupPath(name string) (string, error) {
	// 防目录穿越：只允许纯文件名
	if name == "" || strings.ContainsAny(name, "/\\") || !strings.HasSuffix(name, ".sql.gz") {
		return "", fmt.Errorf("非法的备份文件名")
	}
	p := filepath.Join(BackupDirPath(), name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("备份不存在")
	}
	return p, nil
}

// DeleteBackup 删除一个备份
func DeleteBackup(name string) error {
	p, err := backupPath(name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// RestoreBackup 恢复到指定备份。恢复前先自动备份当前库，避免误操作不可回退。
func RestoreBackup(name string) (string, error) {
	p, err := backupPath(name)
	if err != nil {
		return "", err
	}
	dsn := pgDSN()
	if dsn == "" {
		return "", fmt.Errorf("PG_DSN 未配置")
	}
	safety, _, berr := RunBackup("恢复前自动备份")
	if berr != nil {
		return "", fmt.Errorf("恢复前自动备份失败，已中止：%v", berr)
	}
	// pg_dump 的产物带 CREATE TABLE（无 IF NOT EXISTS），直接灌进"还有表"的库必然冲突。
	// 所以恢复 = 清空 schema → 灌入。清空前已经做了安全备份，失败可再从安全备份回来。
	reset := exec.Command("sh", "-c", fmt.Sprintf(
		"psql '%s' -v ON_ERROR_STOP=1 -c \"DROP SCHEMA public CASCADE; CREATE SCHEMA public;\" > /dev/null", dsn))
	if out, err := reset.CombinedOutput(); err != nil {
		return safety, fmt.Errorf("清空当前库失败（已中止，未影响数据）: %v %s", err, strings.TrimSpace(string(out)))
	}
	cmd := exec.Command("sh", "-c", fmt.Sprintf("gunzip -c '%s' | psql '%s' -v ON_ERROR_STOP=1 > /dev/null", p, dsn))
	if out, err := cmd.CombinedOutput(); err != nil {
		return safety, fmt.Errorf("恢复失败: %v %s", err, strings.TrimSpace(string(out)))
	}
	log.Printf("[backup] 已从 %s 恢复（恢复前备份：%s）", name, safety)
	return safety, nil
}

// CleanOldBackups 删除超过保留天数的备份，返回删除数量
func CleanOldBackups(keepDays int) int {
	if keepDays <= 0 {
		return 0
	}
	deadline := time.Now().AddDate(0, 0, -keepDays)
	n := 0
	for _, b := range ListBackups() {
		t, err := time.Parse("2006-01-02 15:04:05", b.ModTime)
		if err == nil && t.Before(deadline) {
			if err := DeleteBackup(b.Name); err == nil {
				n++
			}
		}
	}
	return n
}

// StartBackupScheduler 每天到点自动备份（按 backup_settings.hour）
func StartBackupScheduler() {
	go func() {
		lastRun := ""
		for {
			var s model.BackupSettings
			if err := config.DB.First(&s, 1).Error; err == nil && s.Enabled {
				now := time.Now()
				today := now.Format("2006-01-02")
				if now.Hour() == s.Hour && lastRun != today {
					if name, size, err := RunBackup("每日定时"); err == nil {
						lastRun = today
						CleanOldBackups(s.KeepDays)
						log.Printf("[backup] 定时备份完成 %s（%d KB）", name, size/1024)
					}
				}
			}
			time.Sleep(5 * time.Minute)
		}
	}()
}
