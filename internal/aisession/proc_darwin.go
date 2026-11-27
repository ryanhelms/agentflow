//go:build darwin

package aisession

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// processStart is the process's start time as ps reports it (the
// kern.proc.pid start time, read without a dependency beyond the standard
// library).
func processStart(pid int) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	s := strings.TrimSpace(string(out))
	if err != nil || s == "" {
		return "", false
	}
	return s, true
}

func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }
