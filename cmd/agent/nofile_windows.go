//go:build windows

package main

// Windows 没有 RLIMIT_NOFILE：句柄上限只受内存与内核对象配额约束，
// 不存在"nofile=1024 撞墙"的同类问题，这里返回一个足够大的值让自检走"充足"分支。

// nofileSoft 返回 Windows 下的等效上限（固定值）。
func nofileSoft() (int, error) {
	return 65535, nil
}
