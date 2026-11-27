//go:build !linux && !darwin

package aisession

// processStart: no portable start time, so a lost harness is never killed
// by a reader; ai status says so.
func processStart(int) (string, bool) { return "", false }

func killGroup(int) {}
