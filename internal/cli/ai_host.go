package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"regexp"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/config"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/command/ops"
)

// `agentflow ai host --session ID` is runtime plumbing (ADR, "Phase 7 session
// host"): the detached host that `ai run --detach` and agent starts launch.
// It is not a registered operation and not in the usage text. It claims the
// session's record and runs it until it ends; the Manager refuses unless the
// record is starting and unclaimed.

var hostSessionRe = regexp.MustCompile(`^ais_[0-9a-f]{32}$`)

func runAIHost(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ai host", flag.ContinueOnError)
	id := fs.String("session", "", "the session to host")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !hostSessionRe.MatchString(*id) {
		return errors.New("usage: agentflow ai host --session ID (started by agentflow ai run; not for direct use)")
	}
	a, err := openHostApp(ctx, aisession.ID(*id))
	if err != nil {
		return err
	}
	_, err = a.AIHost(ctx, ops.AISessionOptions(a), aisession.ID(*id), aisession.HostOptions{Mode: aisession.HostDetached})
	return err
}

// openHostApp opens the App on the worktree the session record names, never
// on the host's working directory: the record was written by the starter,
// under the store lock, for the worktree it runs in. The Manager still
// refuses unless the record is starting and unclaimed.
func openHostApp(ctx context.Context, id aisession.ID) (*app.App, error) {
	home, err := config.Home()
	if err != nil {
		return nil, err
	}
	st, err := aisession.OpenStore(home)
	if err != nil {
		return nil, err
	}
	s, err := st.Get(id)
	if err != nil {
		return nil, fmt.Errorf("ai host: session %s: %w", id, err)
	}
	if s.Worktree == "" {
		return nil, fmt.Errorf("ai host: session %s names no worktree", id)
	}
	return app.Open(ctx, s.Worktree, "cli")
}
