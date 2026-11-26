//go:build unix

package jsonrpc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup makes the child lead a new process group, so Kill can
// end the child and everything it started.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcess sends SIGKILL to the child's process group, then to the
// child itself in case it left the group.
func killProcess(p *os.Process) error {
	groupErr := syscall.Kill(-p.Pid, syscall.SIGKILL)
	err := p.Kill()
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		if groupErr != nil && !errors.Is(groupErr, syscall.ESRCH) {
			return groupErr
		}
		return nil
	}
	return err
}
