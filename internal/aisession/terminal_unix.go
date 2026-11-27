//go:build !windows

package aisession

import (
	"os/exec"
	"syscall"
)

func setTerminalGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTerminalGroup kills the terminal's whole process group.
func killTerminalGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
