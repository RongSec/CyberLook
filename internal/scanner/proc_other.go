//go:build !(linux || darwin || freebsd)

package scanner

import "os/exec"

func setProcGroup(cmd *exec.Cmd) {}

func terminateProc(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func forceKillProc(cmd *exec.Cmd) { terminateProc(cmd) }

func procAlive(cmd *exec.Cmd) bool { return cmd != nil && cmd.Process != nil }
