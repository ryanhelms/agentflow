package app

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/ryanhelms/agentflow/internal/bus/store"
)

// OpenEventStore opens the Message Store of the repository whose common git
// dir is commonDir, in role. A reader gets an error wrapping fs.ErrNotExist
// when the repository has no store yet. Tests replace it.
var OpenEventStore = store.Open

// OpenGlobalEventStore opens, in role, the store of events that belong to no
// repository (bus.GlobalRepo): the personal configuration layer and
// user-level client settings. A reader gets an error wrapping fs.ErrNotExist
// when the console daemon has not created it, which callers read as "no
// global events". Tests replace it.
//
// The global store lives in the console registry, not under a repository's
// common git dir (ADR "Phase 2, Scoping"), and this package cannot import
// webui, which imports it. So package webui sets this in its init, to
// webui.OpenGlobalEventStore. Until then, and in tests that do not import
// webui, no global store exists.
var OpenGlobalEventStore = func(context.Context, store.Role) (store.Store, error) {
	return nil, fmt.Errorf("global event store: %w", fs.ErrNotExist)
}
