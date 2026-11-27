//go:build linux || darwin

package aisession

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// A host that crashed leaves its terminals and its asks behind; the reader
// that ends the session host_lost kills the terminals' process groups
// (only when the PID still has its recorded start time) and removes the
// asks, the TMPDIR and the answer key.
func TestEndLostKillsTerminalsAndRemovesAsks(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := openStore(home, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	old := time.Now().Add(-time.Hour).UTC()
	sess := Session{ID: id, State: StateRunning, Worktree: home, CreatedAt: old, Host: HostInfo{StartedAt: old, Heartbeat: old}, EnvNames: []string{}}
	dir := st.Dir(id)
	for _, d := range []string{dir, filepath.Join(dir, asksDir), filepath.Join(dir, "tmp")} {
		if err := mkdirPrivate(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.write(sess); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(asksDir, "ask_0123456789abcdef.json"), filepath.Join("tmp", "x"), answerKeyFile} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/bin/sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	addTerminalProc(st.path(id, terminalsFile), cmd.Process.Pid)
	// A record whose start time does not match is never killed.
	other := exec.Command("/bin/sleep", "60")
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-other.Process.Pid, syscall.SIGKILL); _ = other.Wait() })
	b, _ := os.ReadFile(st.path(id, terminalsFile))
	b = append(b[:len(b)-1], []byte(`,{"pid":`+itoa(other.Process.Pid)+`,"start":"not its start"}]`)...)
	if err := os.WriteFile(st.path(id, terminalsFile), b, 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := st.List()
	if err != nil || len(list) != 1 || list[0].Code != CodeHostLost {
		t.Fatalf("list = %+v, %v", list, err)
	}
	select {
	case err := <-done:
		_ = err // killed: the wait error is expected
	case <-time.After(10 * time.Second):
		t.Fatal("the lost session's terminal still runs")
	}
	if err := other.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("a process whose start time did not match was killed: %v", err)
	}
	for _, f := range []string{asksDir, "tmp", answerKeyFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s is still there (%v)", f, err)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
