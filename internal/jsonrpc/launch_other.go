//go:build !unix

package jsonrpc

import (
	"os"
	"os/exec"
)

// setProcessGroup does nothing here: without job objects (left out of
// phase 5) only the child itself can be killed.
func setProcessGroup(*exec.Cmd) {}

// killProcess kills the child only.
func killProcess(p *os.Process) error { return p.Kill() }
