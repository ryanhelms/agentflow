//go:build unix

package jsonrpc_test

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunGrandchildDoesNotHoldItOpen(t *testing.T) {
	res, took := run(t, runSpec(t, "run-spawn-exit"), 64, 30*time.Second)
	if res.TimedOut || res.ExitCode != 0 {
		t.Fatalf("result = %+v", res)
	}
	if took > 5*time.Second {
		t.Fatalf("Run took %s: the grandchild held stdout open", took)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		t.Fatalf("stdout %q: %v", res.Stdout, err)
	}
	waitGone(t, pid)
}

func TestRunTimeoutKillsTheGroup(t *testing.T) {
	res, _ := run(t, runSpec(t, "run-spawn-exit", "hang"), 64, 2*time.Second)
	if !res.TimedOut {
		t.Fatalf("result = %+v", res)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		t.Fatalf("stdout %q: %v", res.Stdout, err)
	}
	waitGone(t, pid)
}
