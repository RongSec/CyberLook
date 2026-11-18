//go:build linux || darwin || freebsd

package scanner

import (
	"os/exec"
	"syscall"
)

// setProcGroup 让 chromium 自成进程组：kill 时能连同 renderer / gpu 子进程一起收掉，
// 否则只杀主进程会留下一堆孤儿（容器里表现为 <defunct>）。
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProc 先礼后兵：整组 SIGTERM
func terminateProc(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// forceKillProc 整组 SIGKILL，兜底用
func forceKillProc(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// procAlive 进程是否还活着（信号 0 只做权限/存在性检查）
func procAlive(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	return syscall.Kill(cmd.Process.Pid, 0) == nil
}
