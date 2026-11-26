package bus

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// SchemaVersion is the envelope version written by this build. Payloads
// change only by adding fields; a breaking payload change needs a new event
// type, added by amending the ADR.
const SchemaVersion = 1

// MaxPayloadBytes caps one event's payload. Larger events are rejected at
// Publish so that one store line always stays below 1 MiB.
const MaxPayloadBytes = 256 << 10

// RepoID names one repository (one common git dir). It is 16 lowercase hex
// characters, read from <common-git-dir>/agentflow/events/repo-id, which the
// store creates once with random bytes. GlobalRepo is reserved for events
// that belong to no repository.
type RepoID string

// GlobalRepo is the RepoID of events that belong to no repository, such as
// changes to ~/.agentflow (phase 2) or harness detection (phase 6).
const GlobalRepo RepoID = "global"

// WorktreeID names one worktree of a repository. It is the console's worktree
// ID: "main" for the primary checkout, otherwise "<dns-label>-<8 hex>" (see
// inspectWorktree in internal/webui). Empty means the event concerns the
// whole repository.
type WorktreeID string

var (
	repoIDRe     = regexp.MustCompile(`^[0-9a-f]{16}$`)
	worktreeIDRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Valid reports whether r is a well-formed repository ID.
func (r RepoID) Valid() bool { return r == GlobalRepo || repoIDRe.MatchString(string(r)) }

// Valid reports whether w is a well-formed worktree ID. The empty ID is valid.
func (w WorktreeID) Valid() bool { return w == "" || worktreeIDRe.MatchString(string(w)) }

// Event is the envelope every message on the bus, in the store and on the
// SSE stream carries. Its JSON form is the store line format and the "event"
// object of an SSE delta frame.
type Event struct {
	// ID is a UUIDv7 from NewID. It is unique across repositories.
	ID string `json:"id"`
	// Type is a registered event type, for example "git.head.changed".
	Type Type `json:"type"`
	// Source names the producer: "<component>[:<detail>]", for example
	// "watch.git", "watch.config", "daemon", "cli", "mcp:claude-code" or
	// "hook:pre-bash".
	Source string `json:"source"`
	// Repo is the repository the event belongs to. It is empty only for
	// transient events such as stream.overflow.
	Repo RepoID `json:"repo,omitempty"`
	// Worktree is the worktree the event belongs to, or empty for
	// repository-wide events such as git.refs.changed.
	Worktree WorktreeID `json:"worktree,omitempty"`
	// CorrelationID groups every event of one operation or causal chain.
	// It is the ID of the first event of the chain, or an operation ID.
	CorrelationID string `json:"correlation_id,omitempty"`
	// CausationID is the ID of the event that directly caused this one.
	CausationID string `json:"causation_id,omitempty"`
	// Seq is the event's position in its repository's store, starting at 1.
	// Only the store assigns it. Zero means not stored (transient events,
	// or an event that has not passed the store yet).
	Seq uint64 `json:"seq,omitempty"`
	// Time is when the producer observed the change, in UTC.
	Time time.Time `json:"time"`
	// SchemaVersion is the envelope version, currently SchemaVersion.
	SchemaVersion int `json:"schema_version"`
	// Payload is the type-specific body, always a JSON object.
	Payload json.RawMessage `json:"payload"`
}

// Errors returned by Validate and by Publish.
var (
	ErrUnknownType     = errors.New("unknown event type")
	ErrInvalidEnvelope = errors.New("invalid event envelope")
	ErrPayloadTooLarge = errors.New("event payload too large")
)

// Validate checks e against the envelope rules in the ADR. It does not
// fill defaults; Publish does that first (see Normalize).
func (e Event) Validate() error {
	info, ok := Lookup(e.Type)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownType, e.Type)
	}
	switch {
	case e.ID == "" || e.Source == "" || e.Time.IsZero() || e.SchemaVersion < 1:
		return fmt.Errorf("%w: id, source, time and schema_version are required", ErrInvalidEnvelope)
	case !e.Worktree.Valid():
		return fmt.Errorf("%w: worktree %q", ErrInvalidEnvelope, e.Worktree)
	case info.Transient && (e.Repo != "" && !e.Repo.Valid()):
		return fmt.Errorf("%w: repo %q", ErrInvalidEnvelope, e.Repo)
	case !info.Transient && !e.Repo.Valid():
		return fmt.Errorf("%w: repo %q", ErrInvalidEnvelope, e.Repo)
	case info.Scope == ScopeWorktree && e.Worktree == "":
		return fmt.Errorf("%w: %s needs a worktree", ErrInvalidEnvelope, e.Type)
	case info.Scope == ScopeRepo && e.Worktree != "":
		return fmt.Errorf("%w: %s is repository-wide", ErrInvalidEnvelope, e.Type)
	case len(e.Payload) > MaxPayloadBytes:
		return ErrPayloadTooLarge
	case len(e.Payload) == 0 || e.Payload[0] != '{' || !json.Valid(e.Payload):
		return fmt.Errorf("%w: payload must be a JSON object", ErrInvalidEnvelope)
	}
	return nil
}

// Normalize fills the defaults Publish applies before validation: a new ID,
// the current UTC time and SchemaVersion, and an empty object payload.
func (e Event) Normalize(now time.Time) Event {
	if e.ID == "" {
		e.ID = NewID(now)
	}
	if e.Time.IsZero() {
		e.Time = now
	}
	e.Time = e.Time.UTC()
	if e.SchemaVersion == 0 {
		e.SchemaVersion = SchemaVersion
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage(`{}`)
	}
	return e
}

// NewID returns a UUIDv7 (RFC 9562) in canonical lowercase form: 48 bits of
// Unix milliseconds from now, then random bits from crypto/rand. IDs sort by
// creation time to the millisecond.
func NewID(now time.Time) string {
	var b [16]byte
	_, _ = rand.Read(b[6:]) // Since Go 1.24, crypto/rand.Read never returns an error.
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(now.UnixMilli()))
	copy(b[:6], ms[2:])
	b[6] = 0x70 | b[6]&0x0f // version 7
	b[8] = 0x80 | b[8]&0x3f // RFC 9562 variant
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
