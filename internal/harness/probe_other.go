//go:build !unix

package harness

// probeKillGroup does nothing where jsonrpc kills only the child itself
// (Windows: no job objects in phase 6).
func probeKillGroup(int) {}
