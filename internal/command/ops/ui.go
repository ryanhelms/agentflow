package ops

import (
	"context"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/webui"
)

// ui.open has no CLI binding yet: the ui command needs no App (it takes a
// directory and manages the console daemon), and the command runner opens one.
// The operation opens a local page and writes nothing to the repository, so it
// is a read for the gate and the timeline, although its tool is not annotated
// read-only (it opens the user's browser).
func init() {
	command.Register(command.Spec[webui.Destination, any]{
		Name:    "ui.open",
		CLIPath: []string{"ui"},
		MCP: &command.MCP{Tool: "agentflow_ui_open", Description: "Open the local repository console for the current worktree in the developer's browser. Use this when asked to open the provider editor, autonomy settings, configuration, overview or handoff. Reuse the running console and navigate to the requested screen and provider role.",
			Annotations: command.LocalWrite},
		Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in webui.Destination) (any, error) {
			return webui.Open(ctx, a.Repo.Dir, in)
		},
	})
}
