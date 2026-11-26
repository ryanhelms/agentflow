package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Event sources set by the built-in Translators.
const (
	SourceGit    = "watch.git"
	SourceConfig = "watch.config"
)

// Tracker is implemented by stateful Translators. The Watcher calls Track
// when it starts watching a Target, before the Target's first Batch, so
// the Translator can record a baseline and skip changes that change
// nothing; Untrack forgets it.
type Tracker interface {
	Track(ctx context.Context, t Target) error
	Untrack(t Target)
}

// Chain runs several Translators on every Batch and concatenates their
// events in order. It forwards Track and Untrack to members that are
// Trackers.
type Chain []Translator

var (
	_ Translator = Chain(nil)
	_ Tracker    = Chain(nil)
)

// NewTranslator returns the phase 1 Translator: git, then repository
// configuration.
func NewTranslator() Chain { return Chain{NewGitTranslator(), NewConfigTranslator()} }

// Translate implements Translator. A failing member does not stop the
// others; their errors are joined.
func (c Chain) Translate(ctx context.Context, b Batch) ([]bus.Event, error) {
	var out []bus.Event
	var errs []error
	for _, t := range c {
		evs, err := t.Translate(ctx, b)
		out = append(out, evs...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return out, errors.Join(errs...)
}

// Track implements Tracker.
func (c Chain) Track(ctx context.Context, t Target) error {
	var errs []error
	for _, m := range c {
		if tr, ok := m.(Tracker); ok {
			if err := tr.Track(ctx, t); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Untrack implements Tracker.
func (c Chain) Untrack(t Target) {
	for _, m := range c {
		if tr, ok := m.(Tracker); ok {
			tr.Untrack(t)
		}
	}
}

// newEvent builds an event for the bus; Publish fills nothing it sets.
func newEvent(typ bus.Type, source string, repo bus.RepoID, wt bus.WorktreeID, at time.Time, payload any) (bus.Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return bus.Event{}, fmt.Errorf("encode %s payload: %w", typ, err)
	}
	return bus.Event{ID: bus.NewID(at), Type: typ, Source: source, Repo: repo, Worktree: wt,
		Time: at.UTC(), SchemaVersion: bus.SchemaVersion, Payload: raw}, nil
}

// batchTime is when the producer saw the batch: its last change.
func batchTime(b Batch) time.Time {
	if n := len(b.Changes); n > 0 && !b.Changes[n-1].Time.IsZero() {
		return b.Changes[n-1].Time
	}
	return time.Now()
}

// stamp is the stat identity of one file; the zero stamp means missing.
type stamp struct {
	exists bool
	mod    int64
	size   int64
	ino    uint64
}

func statStamp(path string) stamp {
	fi, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{exists: true, mod: fi.ModTime().UnixNano(), size: fi.Size(), ino: fileIno(fi)}
}
