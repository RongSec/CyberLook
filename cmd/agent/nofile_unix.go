//go:build !windows

package main

// 本机可打开文件数上限（Linux/macOS：RLIMIT_NOFILE）。
//
// 单独成文件是因为 syscall.Rlimit/Getrlimit/RLIMIT_NOFILE 只在类 Unix 平台存在，
// Windows 侧见 nofile_windows.go（无对应概念，返回一个足够大的常量）。

import "syscall"

// nofileSoft 返回当前进程的 soft limit；读取失败返回错误。
func nofileSoft() (int, error) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return 0, err
	}
	return int(lim.Cur), nil
}
