//go:build !mipsle

package scanner

// sqlite 驱动注册：除 mipsle 外全部架构用 modernc.org/sqlite（纯 Go 无 CGO）。
import _ "modernc.org/sqlite"
