//go:build !linux && !darwin

package scanner

import "sync"

// CmdGuard 在非 unix 平台上仍是同一把锁，保证调用方代码不用分平台。
// Windows 没有僵尸进程概念（句柄由系统回收），无回收逻辑。
var CmdGuard sync.Mutex

// StartChildReaper 非 unix 平台为空实现。
func StartChildReaper() {}
