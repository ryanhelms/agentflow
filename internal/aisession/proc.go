package aisession

import (
	"encoding/json"
	"os"
	"sync"
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

// terminalProcs is terminals.json: every terminal process agentflow
// started for the session, with its start time, so a reader that ends a
// lost session can kill their process groups the same careful way.
func addTerminalProc(path string, pid int) {
	terminalProcsMu.Lock()
	defer terminalProcsMu.Unlock()
	var list []harnessProc
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	start, _ := processStart(pid)
	list = append(list, harnessProc{PID: pid, Start: start})
	b, _ := json.Marshal(list)
	_ = writePrivate(path, b)
}

var terminalProcsMu sync.Mutex

// killLostTerminals kills each recorded terminal's process group whose
// process still has the recorded start time; a reused PID is left alone.
func killLostTerminals(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var list []harnessProc
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, p := range list {
		if p.PID <= 1 || p.Start == "" {
			continue
		}
		if start, ok := processStart(p.PID); ok && start == p.Start {
			killGroup(p.PID)
		}
	}
}
