package aisession

import (
	"encoding/json"
	"os"
)

// harnessProc is harness.pid: the harness's process ID and its start time
// as the OS reports it, so a reader can tell the group still belongs to
// that harness before it kills it ("Phase 7 session host", heartbeat).
type harnessProc struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

func writeHarnessProc(path string, pid int) {
	start, ok := processStart(pid)
	if !ok {
		start = ""
	}
	b, _ := json.Marshal(harnessProc{PID: pid, Start: start})
	_ = writePrivate(path, b)
}

// killLostHarness kills the harness's process group only when the
// process with the stored PID started when the stored start time says;
// otherwise it leaves it.
func killLostHarness(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var p harnessProc
	if json.Unmarshal(b, &p) != nil || p.PID <= 1 || p.Start == "" {
		return
	}
	if start, ok := processStart(p.PID); ok && start == p.Start {
		killGroup(p.PID)
	}
}
