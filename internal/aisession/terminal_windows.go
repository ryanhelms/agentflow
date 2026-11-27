//go:build windows

package aisession

import "os/exec"

func setTerminalGroup(*exec.Cmd) {}

// killTerminalGroup kills only the child on Windows (phase 5 decision 9).
func killTerminalGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
