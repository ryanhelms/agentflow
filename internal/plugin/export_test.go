package plugin

import (
	"context"
	"os"
	"syscall"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// BackoffForTest exposes backoff to the external tests.
var BackoffForTest = backoff

// OpenDeadLetterForTest exposes the persistent Dead Letter Channel.
func OpenDeadLetterForTest(path string) bus.DeadLetter { return openDeadLetter(path) }

// ApprovalEventForTest hands e to the host's approval watcher, as its
// subscription would.
func (h *Host) ApprovalEventForTest(ctx context.Context, e bus.Event) { h.approvalEvent(ctx, e) }

// InodeForTest is the inode of st, to count rewrites.
func InodeForTest(st os.FileInfo) uint64 { return st.Sys().(*syscall.Stat_t).Ino }
