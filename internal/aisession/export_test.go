package aisession

import (
	"path/filepath"
	"testing"
)

// SetMaxUpdateBytesForTest lowers the limit_output bound for one test.
func SetMaxUpdateBytesForTest(tb testing.TB, n int64) {
	old := maxUpdateBytes
	maxUpdateBytes = n
	tb.Cleanup(func() { maxUpdateBytes = old })
}

// SetFoldForTest makes the folding platforms' path rules (case, non-ASCII
// names) apply, or not, for one test.
func SetFoldForTest(tb testing.TB, on bool) {
	old := foldCase
	foldCase = func() bool { return on }
	tb.Cleanup(func() { foldCase = old })
}

// read is readChecked against the file rel resolves to now (the tests of
// the os.Root rules, which run before the SameFile check).
func (f *files) read(rel string, line, limit *uint32) (string, int64, error) {
	checked, err := filepath.EvalSymlinks(filepath.Join(f.root.Name(), filepath.FromSlash(rel)))
	if err != nil {
		checked = ""
	}
	return f.readChecked(rel, checked, line, limit)
}

// Fingerprint is the ask fingerprint, for tests.
var Fingerprint = fingerprintOf

// SetToolCapsForTest lowers the tool-call and diff-byte caps for one test.
func SetToolCapsForTest(tb testing.TB, calls int, diffBytes int64) {
	oc, od := maxToolCalls, maxDiffBytes
	maxToolCalls, maxDiffBytes = calls, diffBytes
	tb.Cleanup(func() { maxToolCalls, maxDiffBytes = oc, od })
}

// HideClaimForTest makes this process forget session id's claim token, as
// another process would not know it, and returns the function that puts
// it back.
func HideClaimForTest(id ID) func() {
	t, ok := claimTokens.LoadAndDelete(id)
	return func() {
		if ok {
			claimTokens.Store(id, t)
		}
	}
}
