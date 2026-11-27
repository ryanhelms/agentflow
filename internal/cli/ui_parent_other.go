//go:build !linux && !darwin

package cli

// On other systems the parent's executable is not read: the check passes
// (recorded in the ADR as a gap).
const parentUnknownAllowed = true

func parentExecutable(int) (string, bool) { return "", false }
