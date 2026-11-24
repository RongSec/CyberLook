//go:build linux || darwin

package scanner

import (
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// CmdGuard 保护「受 exec.Cmd 管理的子进程」的退出状态。
//
// 为什么需要它：容器里 PID 1 是 asset-server 本身，孤儿进程会被 reparent 到它，
// 需要主动 wait4 回收，否则会累积成僵尸（实测一轮压测后 54 个 <defunct>）。
// 但 wait4(-1, ...) 会连带抢走 exec.Cmd 正在管理的子进程（pg_dump / psql /
// chromium 主进程）的退出状态，导致 cmd.Wait() 报 "no child processes"，
// 备份会被误判成失败。
//
// 所以两边约定：执行受管子进程时持有这把锁，回收循环 TryLock 抢不到就跳过本轮，
// 等下一次 SIGCHLD 或兜底 tick 再收。
var CmdGuard sync.Mutex

// StartChildReaper 在进程作为 PID 1 时启动僵尸回收。
//
// 只在 PID 1 生效：非 1 的进程有上级 init 负责回收，自己 wait4 反而会抢走
// 本进程 exec.Cmd 的退出状态。
func StartChildReaper() {
	if os.Getpid() != 1 {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGCHLD)
	// 兜底 tick：备份期间抢不到锁会跳过，若之后没有新的 SIGCHLD，
	// 那一轮僵尸要等到下一个信号才收。10 秒一次的轮询把这个窗口封住。
	tick := time.NewTicker(10 * time.Second)
	go func() {
		defer tick.Stop()
		for {
			select {
			case <-ch:
			case <-tick.C:
			}
			reapChildren()
		}
	}()
	log.Printf("[proc] 已启用子进程回收（PID 1，容器内无 init）")
}

// reapChildren 回收所有已退出且未被 wait 的子进程，非阻塞。
func reapChildren() {
	if !CmdGuard.TryLock() {
		return // 有受管子进程在跑，本轮让路
	}
	defer CmdGuard.Unlock()
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}
