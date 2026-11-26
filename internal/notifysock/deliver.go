package notifysock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
)

// Appender is the part of a store.Store that Deliver uses: a store opened
// with store.RoleAppender.
type Appender interface {
	Append(ctx context.Context, e bus.Event) (bus.Event, error)
}

// Delivery says where Deliver put an event.
type Delivery string

// Deliveries.
const (
	// Stored: no daemon owns the store, so the producer appended the event
	// itself. `agentflow events --follow` and the next daemon see it; no
	// live subscriber does, because there is no bus.
	Stored Delivery = "stored"
	// Sent: the daemon owns the store and accepted the event over the
	// socket. It is stored and published live.
	Sent Delivery = "sent"
	// Dropped: the event was lost. Deliver called warn with the reason.
	Dropped Delivery = "dropped"
)

// Deliver hands e to the event store by the single-writer rule. It replaces
// the phase 1 rule "drop on store.ErrOwned":
//
//  1. Append through app (a store opened with store.RoleAppender). With no
//     daemon owning the store this succeeds: Stored.
//  2. On store.ErrOwned the daemon owns the store, so send e over the
//     socket: Sent.
//  3. When the socket reports ErrNoDaemon (the daemon is starting or
//     stopping, or predates the socket), or rejects e, or times out, drop
//     e and call warn once: Dropped.
//
// Every producer that is not the daemon (hooks, and the command Invoker's
// Emitter in CLI and MCP processes) calls Deliver only when store.Exists:
// it never creates a store, so agentflow commands add no files under a
// consuming repository's .git. Without a store no daemon has owned the
// repository, so the socket would refuse the event as unknown_repo; the
// producer drops it without dialing (ADR, phase 3 decision 6).
//
// app may be nil when the store could not be opened (step 1 is skipped);
// send may be nil when no Locator is available (step 2 drops). warn may be
// nil: hooks pass nil, because git and agents read their output. Deliver
// normalizes e and drops it without fallbacks when it is invalid. Events
// are notifications, so a dropped one only delays an update until the next
// event or snapshot.
func Deliver(ctx context.Context, e bus.Event, app Appender, send Sender, warn func(error)) Delivery {
	drop := func(err error) Delivery {
		if warn != nil {
			warn(fmt.Errorf("event %s dropped: %w", e.Type, err))
		}
		return Dropped
	}
	e = e.Normalize(time.Now())
	if err := e.Validate(); err != nil {
		return drop(err)
	}
	if app != nil {
		_, err := app.Append(ctx, e)
		if err == nil {
			return Stored
		}
		if !errors.Is(err, store.ErrOwned) {
			return drop(err)
		}
	}
	if send == nil {
		return drop(ErrNoDaemon)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, SendTimeout)
		defer cancel()
	}
	if err := send.Send(ctx, e); err != nil {
		return drop(err)
	}
	return Sent
}
