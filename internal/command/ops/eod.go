package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type eodIn struct {
	Notes        map[string]string `json:"notes" jsonschema:"map of Jira key to a one-line progress note"`
	ConfirmToken string            `json:"confirm_token,omitempty"`
}

const eodUsage = "usage: agentflow eod post KEY=NOTE [KEY=NOTE...] [--yes]"

func init() {
	command.Register(command.Spec[eodIn, any]{
		Name:    "eod.post",
		CLIPath: []string{"eod", "post"},
		CLI: &command.CLI[eodIn]{
			// Declared here to keep the legacy command's help text.
			Flags:        func(fs *flag.FlagSet, _ *eodIn) { fs.Bool("yes", false, "accept the confirmation prompt") },
			Args:         parseNotes,
			Interspersed: true,
			Confirm:      true,
			Usage:        "  eod post KEY=NOTE [KEY=NOTE...] [--yes]   post end-of-day progress comments",
			Section:      command.SectionWork,
			Order:        5300,
		},
		MCP:    &command.MCP{Tool: "agentflow_eod_post", Description: "Post end-of-day progress comments on tickets.", Annotations: command.RemoteWrite},
		Policy: command.Policy{Class: policy.Remote, Action: "eod"},
		Token:  func(in *eodIn) *string { return &in.ConfirmToken },
		Describe: func(in eodIn) command.Detail {
			return command.Detail{Subject: fmt.Sprintf("end-of-day notes on %d tasks", len(in.Notes))}
		},
		Run: func(ctx context.Context, a *app.App, in eodIn) (any, error) {
			return a.PostEOD(ctx, in.Notes, in.ConfirmToken)
		},
	})
}

// parseNotes reads KEY=NOTE pairs, with the legacy command's errors.
func parseNotes(in *eodIn, args []string) error {
	if len(args) == 0 {
		return errors.New(eodUsage)
	}
	in.Notes = make(map[string]string, len(args))
	for _, p := range args {
		key, note, ok := strings.Cut(p, "=")
		key, note = strings.TrimSpace(key), strings.TrimSpace(note)
		if !ok || key == "" || note == "" {
			return fmt.Errorf("%q is not KEY=NOTE; %s", p, eodUsage)
		}
		in.Notes[key] = note
	}
	return nil
}
