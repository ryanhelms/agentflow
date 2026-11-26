// Package watch turns file-system changes in git directories and
// repository configuration into bus events.
//
// The contract is fixed by the ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md. Package
// watchtest holds a fake Source; the fsnotify and polling sources, the
// debouncing Aggregator and the Watcher arrive with phase 1 lane 1.3, which
// also adds the github.com/fsnotify/fsnotify dependency.
//
// Pattern names (Enterprise Integration Patterns):
//
//   - A Source is an Event-Driven Consumer (ModeNotify, fsnotify) or a
//     Polling Consumer (ModePoll, stat polling).
//   - The debounce step is an Aggregator: it collects a burst of Changes
//     per Target and releases one Batch.
//   - Translator is a Message Translator from a Batch to bus events.
//   - Classify and Excluded are the Message Filter in front of both.
package watch

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Timing defaults. Quiet plus MaxDelay keeps a change visible in the UI
// within one second.
const (
	// Quiet is how long a Target must see no new Change before its Batch
	// is released.
	Quiet = 100 * time.Millisecond
	// MaxDelay releases a Batch even while Changes keep arriving.
	MaxDelay = 400 * time.Millisecond
	// PollInterval is how often a polling Source stats its files.
	PollInterval = 500 * time.Millisecond
	// PollRefsInterval is how often a polling Source walks refs/.
	PollRefsInterval = 2 * time.Second
	// PollRefsMaxEntries caps a refs/ walk; beyond it a polling Source
	// watches only refs/heads and packed-refs.
	PollRefsMaxEntries = 10000
)

// Mode is how a Source learns about changes.
type Mode string

const (
	// ModeNotify uses fsnotify (inotify, kqueue, ReadDirectoryChangesW).
	ModeNotify Mode = "notify"
	// ModePoll stats files on an interval. A Target falls back to it when
	// ModeNotify fails to add a directory, or when AGENTFLOW_WATCH=poll.
	ModePoll Mode = "poll"
)

// ErrUnsupported is returned (wrapped) by Source.Add when the directory
// cannot be watched in the Source's mode: watch limits reached (ENOSPC,
// EMFILE), or a file system without notifications (network mounts). The
// Watcher then moves that Target to a ModePoll Source.
var ErrUnsupported = errors.New("directory cannot be watched in this mode")

// Kind is what happened to a path.
type Kind uint8

// Change kinds. Renames arrive as Removed for the old path and Created for
// the new one; git replaces files by renaming a lock file over them.
const (
	Created Kind = iota + 1
	Modified
	Removed
)

// Change is one observed file-system change.
type Change struct {
	// Path is absolute and cleaned.
	Path string
	Kind Kind
	Time time.Time
}

// Source delivers Changes for the directories added to it. Directories are
// watched non-recursively; the Watcher adds subdirectories of a Recursive
// Path as they appear. A Source must never be given a directory under the
// event store (see Excluded).
type Source interface {
	Mode() Mode
	// Add starts watching dir. It returns an error wrapping
	// ErrUnsupported when dir cannot be watched in this mode.
	Add(dir string) error
	// Remove stops watching dir.
	Remove(dir string) error
	// Changes delivers changes until Close.
	Changes() <-chan Change
	// Errors delivers non-fatal errors (for example an fsnotify queue
	// overflow, after which the Watcher rescans every Target).
	Errors() <-chan error
	Close() error
}

// Target is one worktree to watch.
type Target struct {
	Repo     bus.RepoID
	Worktree bus.WorktreeID
	// Root is the worktree's top-level directory.
	Root string
	// GitDir is the worktree's git dir: CommonDir for the primary
	// checkout, CommonDir/worktrees/<name> for a linked worktree.
	GitDir string
	// CommonDir is the repository's common git dir.
	CommonDir string
}

// Batch is a debounced group of Changes for one Target. Changes are in
// arrival order; repeated changes to one path are kept.
type Batch struct {
	Target  Target
	Changes []Change
}

// Translator is the Message Translator from a Batch to events. It is
// stateful per Target: it remembers HEAD's ref and OID and the current git
// operation so it can fill Previous fields and skip no-op changes. After
// git.refs.changed it re-resolves HEAD and also emits git.head.changed when
// the OID moved (a commit on the current branch writes only the ref).
type Translator interface {
	Translate(ctx context.Context, b Batch) ([]bus.Event, error)
}

// Watcher watches Targets and publishes their events. One daemon runs one
// Watcher for every worktree it serves.
type Watcher interface {
	// Add starts watching t. Repository-wide paths are watched once per
	// Repo, however many of its worktrees are added.
	Add(t Target) error
	// Remove stops watching one worktree.
	Remove(repo bus.RepoID, worktree bus.WorktreeID) error
	// Mode reports the mode currently used for one worktree.
	Mode(repo bus.RepoID, worktree bus.WorktreeID) (Mode, bool)
	// Run reads the Sources, aggregates, translates and publishes to pub
	// until ctx is done.
	Run(ctx context.Context, pub bus.Publisher) error
}

// Path is one directory to watch.
type Path struct {
	Dir string
	// Recursive means every subdirectory too.
	Recursive bool
}

// WorktreePaths lists the directories phase 1 watches for one worktree.
func WorktreePaths(t Target) []Path {
	return []Path{
		{Dir: t.GitDir}, // HEAD, index, rebase-*/, *_HEAD, BISECT_LOG
		{Dir: filepath.Join(t.Root, ".agentflow")},              // config.yml, releases.yml
		{Dir: filepath.Join(t.Root, ".agentflow", "providers")}, // providers/*.yml
	}
}

// RepoPaths lists the directories phase 1 watches once per repository.
func RepoPaths(commonDir string) []Path {
	return []Path{
		{Dir: commonDir}, // packed-refs (and, for the primary checkout, its git dir files)
		{Dir: filepath.Join(commonDir, "refs"), Recursive: true},
		{Dir: filepath.Join(commonDir, "worktrees")}, // entries only: worktree added or removed
	}
}

// excludedGitSubdirs are never watched under a common git dir.
var excludedGitSubdirs = []string{filepath.FromSlash("agentflow"), "objects", "logs", "hooks", "lfs", "modules"}

// Excluded reports whether path must never be watched or reported. It
// covers the event store (<common>/agentflow/**, so appending an event can
// never trigger another), object and log directories, and git lock files.
func Excluded(commonDir, path string) bool {
	if strings.HasSuffix(path, ".lock") {
		return true
	}
	rel, err := filepath.Rel(commonDir, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	first := strings.SplitN(rel, string(filepath.Separator), 2)[0]
	for _, ex := range excludedGitSubdirs {
		if first == ex {
			return true
		}
	}
	return false
}

// operationFiles mark an in-progress git operation in a git dir.
var operationFiles = map[string]bool{
	"rebase-merge": true, "rebase-apply": true, "MERGE_HEAD": true,
	"CHERRY_PICK_HEAD": true, "REVERT_HEAD": true, "BISECT_LOG": true,
}

// Classify maps a changed path of t to the event type it signals. It
// returns false for paths that signal nothing (including Excluded ones).
// Checks run in this order because a linked worktree's GitDir lies under
// CommonDir/worktrees.
func Classify(t Target, path string) (bus.Type, bool) {
	if Excluded(t.CommonDir, path) {
		return "", false
	}
	if rel, ok := under(t.GitDir, path); ok {
		first := strings.SplitN(rel, "/", 2)[0]
		switch {
		case rel == "HEAD":
			return bus.TypeGitHeadChanged, true
		case rel == "index":
			return bus.TypeGitIndexChanged, true
		case operationFiles[first]:
			return bus.TypeGitOperationChanged, true
		}
	}
	if rel, ok := under(t.CommonDir, path); ok {
		switch {
		case rel == "packed-refs" || strings.HasPrefix(rel, "refs/"):
			return bus.TypeGitRefsChanged, true
		case strings.HasPrefix(rel, "worktrees/") && strings.Count(rel, "/") == 1:
			return bus.TypeGitWorktreesChanged, true
		}
		return "", false
	}
	if rel, ok := under(filepath.Join(t.Root, ".agentflow"), path); ok {
		ext := filepath.Ext(rel)
		depthOK := !strings.Contains(rel, "/") || (strings.HasPrefix(rel, "providers/") && strings.Count(rel, "/") == 1)
		if depthOK && (ext == ".yml" || ext == ".yaml") {
			return bus.TypeConfigLayerChanged, true
		}
	}
	return "", false
}

// under returns path relative to dir, slash-separated, when path is inside
// dir.
func under(dir, path string) (string, bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
