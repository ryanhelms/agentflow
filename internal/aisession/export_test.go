package aisession

import "testing"

// SetMaxUpdateBytesForTest lowers the limit_output bound for one test.
func SetMaxUpdateBytesForTest(tb testing.TB, n int64) {
	old := maxUpdateBytes
	maxUpdateBytes = n
	tb.Cleanup(func() { maxUpdateBytes = old })
}
