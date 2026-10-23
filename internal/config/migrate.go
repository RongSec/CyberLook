package config

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RunMigrations 执行 migrations 目录下的 SQL 文件（按文件名排序，幂等）
func RunMigrations(dir string) {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(files) == 0 {
		log.Printf("[migrate] 未找到迁移文件，跳过")
		return
	}
	sort.Strings(files)
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			log.Printf("[migrate] 读取 %s 失败: %v", f, err)
			continue
		}
		// 整体执行（pgx 支持多语句），失败则逐条容错执行
		if err := DB.Exec(string(content)).Error; err != nil {
			for _, stmt := range strings.Split(string(content), ";") {
				stmt = strings.TrimSpace(stmt)
				if stmt == "" || strings.HasPrefix(stmt, "--") {
					continue
				}
				DB.Exec(stmt) // 单条失败忽略（幂等语句）
			}
		}
		log.Printf("[migrate] 已执行 %s", filepath.Base(f))
	}
}
