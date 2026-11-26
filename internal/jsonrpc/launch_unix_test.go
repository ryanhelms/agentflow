//go:build unix

package jsonrpc_test

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

func alive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func spawnGrandchild(t *testing.T, p jsonrpc.Process) int {
	t.Helper()
	var pid int
	if err := p.Conn().Call(context.Background(), "spawn", nil, &pid); err != nil {
		t.Fatal(err)
	}
	if pid <= 0 || !alive(pid) {
		t.Fatalf("grandchild %d not running", pid)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

func TestKillEndsTheProcessGroup(t *testing.T) {
	p := launch(t, jsonrpctest.ChildSpec(t, "serve"))
	if pgid, err := syscall.Getpgid(p.PID()); err != nil || pgid != p.PID() {
		t.Fatalf("child pgid = %d, %v; want its own group %d", pgid, err, p.PID())
	}
	gc := spawnGrandchild(t, p)
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	waitExited(t, p)
	waitGone(t, gc)
}

func TestStopEndsTheProcessGroup(t *testing.T) {
	s := jsonrpctest.ChildSpec(t, "serve", "ignore-eof")
	s.ShutdownGrace = 100 * time.Millisecond
	p := launch(t, s)
	gc := spawnGrandchild(t, p)
	_ = p.Stop()
	waitGone(t, gc)
}
