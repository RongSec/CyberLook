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
		// 先整体执行（部分驱动支持多语句）；不行再逐条
		if err := DB.Exec(string(content)).Error; err != nil {
			errs := 0
			for _, stmt := range splitSQL(string(content)) {
				if err := DB.Exec(stmt).Error; err != nil {
					// 幂等语句（IF NOT EXISTS 等）重复执行会报错，属正常；
					// 其它错误打出来，避免"迁移静默失败"（046 踩过）
					if !isIdempotentStmt(stmt) && errs < 5 {
						log.Printf("[migrate] %s 语句失败: %v | %s", filepath.Base(f), err, headLine(stmt, 160))
						errs++
					}
				}
			}
		}
		log.Printf("[migrate] 已执行 %s", filepath.Base(f))
	}
}

// splitSQL 按顶层分号切语句。
//
// 不能直接 strings.Split(s, ";")：`DO $$ ... ; ... $$` 里的分号是块内语法，
// 切开后每段都非法，结果整段静默失败（历史 bug）。这里跟踪单引号、双引号、
// 美元引号（$$ 或 $tag$）和行注释，只在"不在任何引号内"时切。
func splitSQL(s string) []string {
	var out []string
	var b strings.Builder
	inSingle, inDouble, inLineComment := false, false, false
	dollarTag := ""

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]

		if inLineComment {
			b.WriteRune(c)
			if c == '\n' {
				inLineComment = false
			}
			continue
		}
		if len(dollarTag) > 0 {
			// 在美元引号内，等结束标记
			if c == '$' {
				if hasPrefixAt(runes, i, dollarTag) {
					b.WriteString(dollarTag)
					i += len([]rune(dollarTag)) - 1
					dollarTag = ""
					continue
				}
			}
			b.WriteRune(c)
			continue
		}
		if inSingle {
			b.WriteRune(c)
			if c == '\'' {
				// '' 是转义的单引号
				if i+1 < len(runes) && runes[i+1] == '\'' {
					b.WriteRune('\'')
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			b.WriteRune(c)
			if c == '"' {
				inDouble = false
			}
			continue
		}

		switch {
		case c == '-' && i+1 < len(runes) && runes[i+1] == '-':
			inLineComment = true
			b.WriteRune(c)
		case c == '\'':
			inSingle = true
			b.WriteRune(c)
		case c == '"':
			inDouble = true
			b.WriteRune(c)
		case c == '$':
			// $tag$ 形式的美元引号
			if tag, n := dollarTagAt(runes, i); n > 0 {
				dollarTag = tag
				b.WriteString(tag)
				i += n - 1
			} else {
				b.WriteRune(c)
			}
		case c == ';':
			if stmt := strings.TrimSpace(b.String()); stmt != "" {
				out = append(out, stmt)
			}
			b.Reset()
		default:
			b.WriteRune(c)
		}
	}
	if stmt := strings.TrimSpace(b.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}

// dollarTagAt 判断 i 处是否是 $tag$ 起始，返回标记与长度
func dollarTagAt(runes []rune, i int) (string, int) {
	// $xxx$，xxx 只能是字母数字下划线（$$ 即空 tag）
	j := i + 1
	for j < len(runes) {
		c := runes[j]
		if c == '$' {
			return string(runes[i : j+1]), j + 1 - i
		}
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return "", 0
		}
		j++
	}
	return "", 0
}

func hasPrefixAt(runes []rune, i int, tag string) bool {
	t := []rune(tag)
	if i+len(t) > len(runes) {
		return false
	}
	for k, r := range t {
		if runes[i+k] != r {
			return false
		}
	}
	return true
}

// isIdempotentStmt 重复执行必然报错的幂等语句，不必刷日志
func isIdempotentStmt(s string) bool {
	u := strings.ToUpper(s)
	for _, kw := range []string{
		"IF NOT EXISTS", "IF EXISTS", "ADD COLUMN", "DROP INDEX", "DROP TABLE",
	} {
		if strings.Contains(u, kw) {
			return true
		}
	}
	return false
}

func headLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}
