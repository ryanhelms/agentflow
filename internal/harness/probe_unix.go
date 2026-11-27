//go:build unix

package harness

import (
	"errors"

	"golang.org/x/sys/unix"
)

// probeKillGroup kills what is left of the process group pid led, after
// the leader exited on its own (a grandchild that kept running).
func probeKillGroup(pid int) {
	if pid <= 1 {
		return
	}
	if err := unix.Kill(-pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		return
	}
}
