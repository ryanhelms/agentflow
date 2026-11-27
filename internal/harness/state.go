package harness

import (
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// ResolveInput is what Resolve decides a state from.
type ResolveInput struct {
	Kind harnessdef.Kind
	// Found: the command was found. BaseFound: a base command was found.
	Found, BaseFound bool
	// Record is the last probe record for the current identity, if any.
	Record *ProbeRecord
	// Version is the version read for the current identity; MinVersion the
	// definition's.
	Version, MinVersion string
	// Auth is the sign-in verdict after the credential-variable rule.
	Auth SignInStatus
	// ProbeCode is the harness's probe code (Harness.Probe.Code).
	ProbeCode string
}

// Resolve applies the state table ("Phase 6 detection states"); the first
// matching row wins. It is a decision table, not a state machine.
//
// A probe that answered initialize with -32000 is recorded by the Prober
// with Status ProbeFailed and Code CodeAuthRequired (row 6). A session's
// later auth_required comes from Store.MarkAuthRequired (AuthRequiredAt
// after ProbedAt).
func Resolve(in ResolveInput) (State, string) {
	r := in.Record
	switch {
	case !in.Found && in.Kind == harnessdef.KindAdapter && in.BaseFound:
		return StateAdapterAvailable, CodeBaseOnly
	case !in.Found:
		return StateNotInstalled, CodeMissing
	case r != nil && r.Status == ProbeFailed && r.Code == CodeProtocolVersion:
		return StateIncompatible, CodeProtocolVersion
	case r != nil && r.Status == ProbeFailed && r.Code == CodeProtocol:
		return StateIncompatible, CodeProtocol
	}
	if in.MinVersion != "" && in.Version != "" {
		if c, ok := compareVersions(in.Version, in.MinVersion); ok && c < 0 {
			return StateIncompatible, CodeVersionTooOld
		}
	}
	switch {
	case in.Auth.Status == SignedOut:
		return StateNeedsAuth, CodeSignedOut
	case r != nil && r.Code == CodeAuthRequired:
		return StateNeedsAuth, CodeAuthRequired
	case r != nil && r.AuthRequiredAt != nil && r.AuthRequiredAt.After(r.ProbedAt):
		return StateNeedsAuth, CodeAuthRequired
	}
	code := in.ProbeCode
	switch {
	case in.MinVersion != "" && in.Version == "":
		code = CodeVersionUnknown
	case code == CodeOK:
		code = ""
	}
	if in.Kind == harnessdef.KindAdapter {
		return StateAdapterInstalled, code
	}
	return StateNative, code
}

// stateGroup orders a report: ready, needs attention, not installed.
func stateGroup(h Harness) int {
	switch {
	case h.State == StateNotInstalled:
		return 2
	case (h.State == StateNative || h.State == StateAdapterInstalled) && probeOK(h.Probe):
		return 0
	}
	return 1
}

func probeOK(p Probe) bool {
	return p.Code == CodeOK && (p.Status == ProbeOK || p.Status == ProbeCached)
}
