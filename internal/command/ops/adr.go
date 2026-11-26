package ops

import (
	"context"
	"errors"
	"strings"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type adrDraftIn struct {
	Title        string `json:"title"`
	Context      string `json:"context,omitempty"`
	Decision     string `json:"decision,omitempty"`
	Consequences string `json:"consequences,omitempty"`
	ConfirmToken string `json:"confirm_token,omitempty"`
}

var adrDraftSpec = command.Spec[adrDraftIn, any]{
	Name:    "adr.draft",
	CLIPath: []string{"adr", "new"},
	MCP:     &command.MCP{Tool: "agentflow_adr_draft", Description: "Write a new ADR under docs/adr from the standard template.", Annotations: command.LocalWrite},
	Policy:  command.Policy{Class: policy.Local, Action: "adr_draft"},
	Token:   func(in *adrDraftIn) *string { return &in.ConfirmToken },
	Describe: func(in adrDraftIn) command.Detail {
		return command.Detail{Subject: "draft ADR " + in.Title}
	},
	Run: func(ctx context.Context, a *app.App, in adrDraftIn) (any, error) {
		return a.ADRDraft(ctx, in.Title, in.Context, in.Decision, in.Consequences, in.ConfirmToken)
	},
}

func init() {
	command.Register(adrDraftSpec)
	// A verb, not a Spec CLI: every word after "adr new" is the title, flags
	// included, and the prompt is never skipped (there is no --yes).
	command.RegisterVerb(command.Verb{
		Path:    []string{"adr", "new"},
		Usage:   "  adr new TITLE                  draft docs/adr/NNNN-title.md",
		Section: command.SectionWork,
		Order:   5800,
		Run: func(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
			if len(args) < 1 {
				return errors.New("usage: agentflow adr new TITLE")
			}
			a, err := env.Open(ctx)
			if err != nil {
				return err
			}
			origin := env.Origin(a)
			title := strings.Join(args, " ")
			return env.Confirm(false, func(token string) (any, error) {
				return command.Invoke(ctx, inv, a, adrDraftSpec, adrDraftIn{Title: title, ConfirmToken: token}, origin)
			})
		},
	})
}
