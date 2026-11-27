//go:build !windows

package aisession

import (
	"os/exec"
	"syscall"
)

// detachAttr starts the detached host in a new session (Setsid).
func detachAttr(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
