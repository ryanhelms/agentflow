//go:build windows

package aisession

import (
	"os/exec"
	"syscall"
)

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

// detachAttr starts the detached host with DETACHED_PROCESS and
// CREATE_NEW_PROCESS_GROUP.
func detachAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup}
}
