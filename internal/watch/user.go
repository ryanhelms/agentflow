package watch

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Phase 2 sources: the personal configuration layer, the approval journal
// and client settings files. The contract is in the ADR, section "Phase 2
// watch sources". This file holds the paths, the classification tables and
// the pure state-transition rule. UserTranslator (translate_user.go)
// translates them and FileWatcher.AddUser (watcher_user.go) watches them;
// the daemon registers both from internal/webui/stream_runtime_user.go.

// UserTarget is the one per-daemon set of user-level directories. The
// daemon watches only directories its own OS user owns (FileWatcher.AddUser
// checks the owner and skips any other), so one user's events never come
// from another user's files.
type UserTarget struct {
	// Home is agentflow's user directory, config.HomeDir() in the daemon
	// process (normally ~/.agentflow).
	Home string
	// UserHome is the OS user's home directory, from os/user, never from
	// $HOME, for the client settings files.
	UserHome string
}

// UserSignal is what a user-level path signals.
type UserSignal int

// User-level signals.
const (
	// SignalPersonalConfig: ~/.agentflow/config.yml or providers/*.y{a,}ml.
	// Translates to config.layer.changed{layer: personal} in GlobalRepo.
	SignalPersonalConfig UserSignal = iota + 1
	// SignalApproval: an approval record. Translates to approval.* in the
	// record's repository (ApprovalRouter), or nothing.
	SignalApproval
	// SignalClientSettings: a user-level client settings file. Translates
	// to client.settings.changed{scope: user} in GlobalRepo.
	SignalClientSettings
)

// UserPaths lists the directories watched once per daemon. None is
// recursive.
func UserPaths(u UserTarget) []Path {
	out := []Path{
		{Dir: u.Home},
		{Dir: filepath.Join(u.Home, "providers")},
		{Dir: filepath.Join(u.Home, "approvals")},
	}
	seen := map[string]bool{}
	for _, f := range UserClientSettings {
		dir := filepath.Dir(filepath.Join(u.UserHome, filepath.FromSlash(f.Path)))
		if !seen[dir] {
			seen[dir] = true
			out = append(out, Path{Dir: dir})
		}
	}
	return out
}

// ClientSettingsFile is one client settings file agentflow cares about.
type ClientSettingsFile struct {
	Client string
	// Path is slash-separated, relative to the OS home (UserClientSettings)
	// or the worktree root (RepoClientSettings).
	Path string
}

// UserClientSettings are the user-level files that change how a client
// runs agentflow. ~/.claude.json is left out on purpose: the client
// rewrites it constantly and it holds account state.
var UserClientSettings = []ClientSettingsFile{
	{"claude", ".claude/settings.json"},
	{"codex", ".codex/config.toml"},
	{"cursor", ".cursor/mcp.json"},
	{"gemini", ".gemini/settings.json"},
	{"grok", ".grok/settings.json"},
}

// RepoClientSettings are the per-worktree files, the ones `agentflow init`
// and `agentflow setup` write (internal/setup). WorktreePaths watches their
// directories and Classify maps them to client.settings.changed{scope: repo}
// for the worktree (ClientSettingsTranslator).
var RepoClientSettings = []ClientSettingsFile{
	{"claude", ".mcp.json"},
	{"claude", ".claude/settings.json"},
	{"claude", ".claude/settings.local.json"},
	{"codex", ".codex/config.toml"},
	{"cursor", ".cursor/mcp.json"},
	{"gemini", ".gemini/settings.json"},
	{"grok", ".grok/settings.json"},
	{"vscode", ".vscode/mcp.json"},
}

// ClassifyUser maps a changed path to its signal and, for client settings,
// the client. Paths outside u, and every other file in the watched
// directories (audit logs, confirm.key, locks, request index files,
// events.jsonl), are ignored.
func ClassifyUser(u UserTarget, path string) (UserSignal, string, bool) {
	if strings.HasSuffix(path, ".lock") {
		return 0, "", false
	}
	if rel, ok := under(u.Home, path); ok {
		switch {
		case rel == "config.yml":
			return SignalPersonalConfig, "", true
		case strings.HasPrefix(rel, "providers/") && strings.Count(rel, "/") == 1 && yamlName(rel):
			return SignalPersonalConfig, "", true
		case strings.HasPrefix(rel, "approvals/") && strings.Count(rel, "/") == 1:
			if _, _, ok := ParseApprovalRecord(filepath.Base(path)); ok {
				return SignalApproval, "", true
			}
		}
		return 0, "", false
	}
	if rel, ok := under(u.UserHome, path); ok {
		for _, f := range UserClientSettings {
			if rel == f.Path {
				return SignalClientSettings, f.Client, true
			}
		}
	}
	return 0, "", false
}

func yamlName(p string) bool { return strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml") }

var approvalRecordRe = regexp.MustCompile(`^([0-9a-f]{32})-(op_[0-9a-f]{32})\.json$`)

// ParseApprovalRecord splits an approval record's file name
// ("<policy.ScopeKey>-<operation ID>.json") into its scope key and
// operation ID.
func ParseApprovalRecord(name string) (scopeKey, operationID string, ok bool) {
	m := approvalRecordRe.FindStringSubmatch(name)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// ApprovalRouter maps a record's scope key to the repository the daemon
// serves under it. The daemon computes policy.ScopeKey of each served
// repository's common git dir (symbolic links resolved, as Gate.Configure
// does). Records of repositories the daemon does not serve produce no
// event, so approvals of one repository never reach another's stream.
type ApprovalRouter interface {
	RepoForScope(scopeKey string) (bus.RepoID, bool)
}

// ApprovalTransition is the event a record's state change produces, given
// the state the translator last saw (empty for a record it has not seen)
// and the record's state now. Prepared operations, and records first seen
// in a final state (the daemon started after they finished), produce
// nothing.
func ApprovalTransition(previous, current bus.ApprovalState) (bus.Type, bool) {
	if previous == current {
		return "", false
	}
	switch current {
	case bus.StatePending:
		if previous == "" {
			return bus.TypeApprovalRequested, true
		}
	case bus.StateApproved, bus.StateCancelled, bus.StateConsumed:
		if previous == bus.StatePending || previous == bus.StateApproved {
			return bus.TypeApprovalResolved, true
		}
	case bus.StateExpired:
		if previous == bus.StatePending || previous == bus.StateApproved {
			return bus.TypeApprovalExpired, true
		}
	}
	return "", false
}

// ApprovalTransitions is the sequence of events a record's state change
// produces. It is ApprovalTransition, plus one case: a record the
// translator first sees after it started serving the repository (previous
// is empty), already approved, cancelled, consumed or expired, was created
// and answered within one debounce window. When it asked a person (asked:
// the record has a prompt owner, which a prepared operation never has), it
// produces approval.requested and then the event of its current state, with
// previous StatePending, so a stream never sees a resolution without its
// request.
func ApprovalTransitions(previous, current bus.ApprovalState, asked bool) []bus.Type {
	if previous == "" && asked && current != bus.StatePending {
		if typ, ok := ApprovalTransition(bus.StatePending, current); ok {
			return []bus.Type{bus.TypeApprovalRequested, typ}
		}
		return nil
	}
	if typ, ok := ApprovalTransition(previous, current); ok {
		return []bus.Type{typ}
	}
	return nil
}

// ObservedExpiry reports whether a record that is still pending or
// approved has passed expiresAt at now. The journal records an expiry only
// when a process next touches the record, so UserTranslator.Tick reports
// approval.expired with Observed set at the earliest expiry of an open record.
// A later "expired" write for the same record then produces nothing,
// because the translator already moved it to StateExpired.
func ObservedExpiry(state bus.ApprovalState, expiresAt, now time.Time) bool {
	return (state == bus.StatePending || state == bus.StateApproved) && !expiresAt.IsZero() && !now.Before(expiresAt)
}
