package plugin

import (
	"context"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// BackoffForTest exposes backoff to the external tests.
var BackoffForTest = backoff

// OpenDeadLetterForTest exposes the persistent Dead Letter Channel.
func OpenDeadLetterForTest(path string) bus.DeadLetter { return openDeadLetter(path) }

// ApprovalEventForTest hands e to the host's approval watcher, as its
// subscription would.
func (h *Host) ApprovalEventForTest(ctx context.Context, e bus.Event) { h.approvalEvent(ctx, e) }

// SetDrainTimeoutForTest shortens how long Stop waits for calls in flight.
func (h *Host) SetDrainTimeoutForTest(d time.Duration) { h.o.DrainTimeout = d }
