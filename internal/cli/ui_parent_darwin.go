//go:build darwin

package cli

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const parentUnknownAllowed = false

// parentExecutable is the parent's executable path as ps reports it
// (kern.procargs), without a dependency beyond the standard library.
func parentExecutable(pid int) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	p := strings.TrimSpace(string(out))
	return p, err == nil && strings.HasPrefix(p, "/")
}
