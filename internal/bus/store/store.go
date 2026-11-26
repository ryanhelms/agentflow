// Package store defines the Message Store that keeps every durable bus
// event of one repository as JSON lines, so streams can resume from a
// bus.Cursor after a reconnect and `agentflow events` can read history.
//
// The contract is fixed by the ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md. Open
// returns the file-backed store, Union reads many repositories' stores as
// one, and package storetest holds an in-memory fake.
//
// Layout, under the repository's common git dir:
//
//	<common-git-dir>/agentflow/events/
//	  repo-id                     16 hex chars, created once (O_EXCL)
//	  owner.lock                  held by the single writer (see Role)
//	  00000000000000000001.jsonl  segment: first Seq, 20 digits, zero-padded
//	  00000000000000004711.jsonl  the active segment has the highest name
//
// Events that belong to no repository (bus.GlobalRepo) use the same layout
// in a directory the caller names, opened with OpenGlobal: the daemon's
// private, per-host <registry>/<host-hash>.events/ (ADR, "Phase 2 scoping").
// Its repo-id holds "global".
package store

import (
	"context"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Layout and retention constants.
const (
	// Dir is the store directory relative to the common git dir. The
	// watcher never watches it (see watch.Excluded).
	Dir = "agentflow/events"
	// RepoIDFile holds the repository's bus.RepoID.
	RepoIDFile = "repo-id"
	// OwnerLock is locked with internal/filelock by the single writer.
	OwnerLock = "owner.lock"
	// SegmentExt is the extension of segment files.
	SegmentExt = ".jsonl"
	// SegmentMaxBytes rotates the active segment once it reaches this size.
	SegmentMaxBytes = 8 << 20
	// SegmentMaxAge rotates the active segment once its first event is
	// this old.
	SegmentMaxAge = 24 * time.Hour
	// RetainAge removes a closed segment once its newest event is older.
	RetainAge = 7 * 24 * time.Hour
	// RetainBytes removes the oldest closed segments while all segments
	// together are larger.
	RetainBytes = 50 << 20
	// MaxReplay is the most events a stream replays from a cursor; beyond
	// it the stream sends resync and a snapshot instead.
	MaxReplay = 1000
)

// Path returns the store directory of the repository whose common git dir
// is commonDir.
func Path(commonDir string) string { return filepath.Join(commonDir, filepath.FromSlash(Dir)) }

// Exists reports whether the repository whose common git dir is commonDir
// has an event store: its repo-id file exists. Only the daemon, as
// RoleOwner, creates a store. Every other producer (hooks, and the command
// Invoker's Emitter in CLI and MCP processes) checks Exists first and never
// opens a store that does not exist, so agentflow commands add no files
// under a consuming repository's .git (ADR, phase 3 decision 6).
func Exists(commonDir string) bool {
	_, err := os.Stat(filepath.Join(Path(commonDir), RepoIDFile))
	return err == nil
}

// Errors returned by stores.
var (
	// ErrCursorExpired: the cursor points before the oldest retained event
	// of a repository. Readers resynchronize from a snapshot.
	ErrCursorExpired = errors.New("event cursor is older than the retained events")
	// ErrOwned: another process is the store's owner. Send the event to the
	// owner over the local socket (phase 2) instead of appending.
	ErrOwned = errors.New("event store is owned by another process")
	// ErrReadOnly: Append on a store opened with RoleReader.
	ErrReadOnly = errors.New("event store opened read-only")
	// ErrTransient: Append of a transient event type (stream.overflow).
	ErrTransient = errors.New("transient events are not stored")
	// ErrWrongRepo: Append of an event for another repository.
	ErrWrongRepo = errors.New("event belongs to another repository")
)

// Role is how a process opens a store. It enforces the single-writer rule:
// at any moment at most one process appends to a repository's store.
type Role int

const (
	// RoleOwner holds OwnerLock for the store's lifetime and is the only
	// writer while it runs. Only the webui daemon ever opens a store as
	// owner. Open fails with ErrOwned when another process owns the store.
	// On open the owner truncates a partial last line left by a crash and
	// reads the last Seq.
	RoleOwner Role = iota + 1
	// RoleAppender is for processes that are not the daemon (CLI, MCP
	// servers, hooks). Each Append takes OwnerLock with
	// filelock.TryExclusive, appends and releases it. When the lock is
	// held, Append returns ErrOwned and the caller hands the event to the
	// daemon over the local socket. Until that socket exists (phase 2,
	// lane 2.2, which also adds a test for it) the caller drops the event
	// with a warning on stderr. Events are observations: the next snapshot
	// repairs a dropped one.
	RoleAppender
	// RoleReader never writes and never takes OwnerLock. `agentflow
	// events`, with or without --follow, and the MCP tool read this way;
	// --follow first makes sure the daemon is running.
	RoleReader
)

// Reader reads stored events. Store implements it for one repository;
// Union implements it for many.
type Reader interface {
	// ReadFrom yields, in order, every stored event that from has not
	// Seen, for the repositories the reader covers. Within a repository
	// the order is Seq; across repositories it is (Time, Repo, Seq). A
	// repository missing from from starts at its oldest retained event. If
	// from has a Seq for a repository but events after it are no longer
	// retained, the first yield is (zero Event, ErrCursorExpired) and
	// iteration stops. A partial last line is skipped, never yielded.
	ReadFrom(ctx context.Context, from bus.Cursor) iter.Seq2[bus.Event, error]
	// Head returns the cursor of the newest stored event of each
	// repository the reader covers.
	Head(ctx context.Context) (bus.Cursor, error)
}

// Store is one repository's Message Store.
type Store interface {
	Reader
	// Repo is the repository this store holds.
	Repo() bus.RepoID
	// Append assigns the next Seq to e, writes it as one line, and returns
	// it. e must be normalized and valid, for this store's repository,
	// durable, and have Seq 0. Append rotates and applies retention when a
	// segment closes. It does not fsync each line; it fsyncs a segment on
	// rotation and on Close.
	Append(ctx context.Context, e bus.Event) (bus.Event, error)
	// Close releases OwnerLock (RoleOwner) and open files.
	Close() error
}

// Follower is a Reader that also waits for new events: a Polling Consumer
// for processes that do not run the bus, such as `agentflow events
// --follow`, which reads while the daemon owns the store.
type Follower interface {
	Reader
	// Follow yields like ReadFrom, then keeps yielding new events as the
	// owner appends them, until ctx is done.
	Follow(ctx context.Context, from bus.Cursor) iter.Seq2[bus.Event, error]
}
