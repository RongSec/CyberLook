//go:build !windows

package main

import (
	"os"
	"syscall"
)

// syscallExec 原子替换当前进程（Linux/macOS 自升级交接重启）
func syscallExec(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
