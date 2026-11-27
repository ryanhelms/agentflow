//go:build !unix

package harness

// processAlive is never called off Unix: the process-group test skips.
func processAlive(int) bool { return false }
