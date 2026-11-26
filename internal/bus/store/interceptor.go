package store

import (
	"context"
	"fmt"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Stores maps each repository to its store.
type Stores func(bus.RepoID) (Store, bool)

// Interceptor returns the bus.Interceptor that appends each durable event
// to its repository's store, so subscribers receive it with Seq set
// (Message Store). Transient events pass through unstored. An event for a
// repository without a store fails Publish.
func Interceptor(stores Stores) bus.Interceptor {
	return bus.InterceptorFunc(func(ctx context.Context, e bus.Event) (bus.Event, error) {
		if info, ok := bus.Lookup(e.Type); ok && info.Transient {
			return e, nil
		}
		s, ok := stores(e.Repo)
		if !ok {
			return bus.Event{}, fmt.Errorf("no event store for repository %q", e.Repo)
		}
		return s.Append(ctx, e)
	})
}
