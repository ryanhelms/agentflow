//go:build linux

package aisession

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processStart is field 22 of /proc/<pid>/stat (start time in clock ticks).
func processStart(pid int) (string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return "", false
	}
	return f[19], true
}

func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }
