//go:build linux

package cli

import (
	"os"
	"strconv"
)

const parentUnknownAllowed = false

func parentExecutable(pid int) (string, bool) {
	p, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	return p, err == nil && p != ""
}
