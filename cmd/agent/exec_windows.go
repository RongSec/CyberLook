//go:build windows

package main

import "errors"

// syscallExec Windows 不支持进程内替换；
// 自升级路径在 main.go 里已用 exec.Command 起新进程 + os.Exit(0) 兜底，
// 这里只是保证编译通过（正常情况下不会被调用）。
func syscallExec(exe string) error {
	return errors.New("windows: use re-exec path")
}
