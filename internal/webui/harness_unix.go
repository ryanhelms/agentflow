//go:build !windows

package webui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Read only the launch chain of this server, never the environment of another
// process. No process information is logged or sent over MCP.
func InteractiveHarness(ctx context.Context, client string, hook bool) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for pid, depth := os.Getppid(), 0; pid > 1 && depth < 5; depth++ {
		id := strconv.Itoa(pid)
		data, err := exec.CommandContext(ctx, "/bin/ps", "-p", id, "-o", "ppid=", "-o", "comm=").Output()
		if err != nil {
			break
		}
		parent, executable, ok := strings.Cut(strings.TrimSpace(string(data)), " ")
		if !ok {
			break
		}
		executable = strings.TrimSpace(executable)
		if desktopHost(client, executable) {
			return true
		}
		if (depth == 0 || hook) && hostInputIsTerminal(ctx, id) {
			command, err := exec.CommandContext(ctx, "/bin/ps", "-ww", "-p", id, "-o", "command=").Output()
			if err == nil && terminalHost(client, executable, strings.TrimSpace(string(command))) {
				return true
			}
		}
		// A new command inside a shell can share a desktop ancestor with a real
		// chat. Only the harness or its own helper should qualify.
		switch filepath.Base(executable) {
		case "bash", "sh", "fish", "zsh", "tmux":
			if !hook || depth > 1 {
				return false
			}
		}
		pid, _ = strconv.Atoi(parent)
	}
	return false
}

func hostInputIsTerminal(ctx context.Context, id string) bool {
	var path string
	switch runtime.GOOS {
	case "linux":
		path, _ = os.Readlink("/proc/" + id + "/fd/0")
	case "darwin":
		info, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-b", "-n", "-P", "-a", "-p", id, "-d", "0", "-Fn").Output()
		if err == nil {
			for _, line := range strings.Split(string(info), "\n") {
				if strings.HasPrefix(line, "n") {
					path = strings.TrimPrefix(line, "n")
				}
			}
		}
	}
	return strings.HasPrefix(path, "/dev/tty") || strings.HasPrefix(path, "/dev/pts/")
}
