package ops

import (
	"context"
	"errors"
	"flag"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type policyExplainIn struct {
	Action string `json:"action,omitempty" jsonschema:"exact action key or omit to list the available actions"`
}

type approvalStatusIn struct {
	OperationID string `json:"operation_id" jsonschema:"public ID from the shared approval"`
}

type approvalApproveIn struct {
	OperationID  string `json:"operation_id" jsonschema:"public ID of the reviewed operation"`
	ConfirmToken string `json:"confirm_token" jsonschema:"original review token, only after the user agrees to the exact operation"`
}

func init() {
	command.Register(command.Spec[policyExplainIn, any]{
		Name:    "policy.explain",
		CLIPath: []string{"policy", "explain"},
		CLI: &command.CLI[policyExplainIn]{
			Flags: func(fs *flag.FlagSet, _ *policyExplainIn) { jsonFlag(fs) },
			Args: policyArgs("policy explain", func(fs *flag.FlagSet, _ *policyExplainIn) { jsonFlag(fs) },
				func(in *policyExplainIn) *string { return &in.Action }),
			Usage:   "  policy explain [ACTION]          show autonomy, repository ceilings and review windows",
			Section: command.SectionWork,
			Order:   3800,
		},
		MCP: &command.MCP{Tool: "agentflow_policy_explain", Description: "Explain the personal settings, repository ceilings, review requirements and window for an action.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in policyExplainIn) (any, error) {
			return a.PolicyExplain(ctx, in.Action)
		},
	})

	command.Register(command.Spec[approvalStatusIn, any]{
		Name:    "approval.status",
		CLIPath: []string{"policy", "status"},
		CLI: &command.CLI[approvalStatusIn]{
			Flags: func(fs *flag.FlagSet, _ *approvalStatusIn) { jsonFlag(fs) },
			Args: policyArgs("policy status", func(fs *flag.FlagSet, _ *approvalStatusIn) { jsonFlag(fs) },
				func(in *approvalStatusIn) *string { return &in.OperationID }),
			// The ID is checked after the App opens, as the legacy command
			// did, and only on the command line: the tool passes it through.
			Run: func(ctx context.Context, call command.CLICall[approvalStatusIn], in approvalStatusIn) error {
				if in.OperationID == "" {
					return errors.New("usage: agentflow policy status ID")
				}
				return call.Env.Print(call.Invoke(ctx, in))
			},
			Usage:   "  policy status ID                inspect a shared approval",
			Section: command.SectionWork,
			Order:   3900,
		},
		MCP: &command.MCP{Tool: "agentflow_approval_status", Description: "Read shared approval state, prompt owner and expiry without exposing its confirmation token.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Read},
		Run: func(_ context.Context, a *app.App, in approvalStatusIn) (any, error) {
			return a.ApprovalStatus(in.OperationID)
		},
	})

	approveFlags := func(fs *flag.FlagSet, in *approvalApproveIn) {
		jsonFlag(fs)
		fs.StringVar(&in.ConfirmToken, "confirm-token", "", "original review token, after explicit approval")
	}
	command.Register(command.Spec[approvalApproveIn, policy.Approval]{
		Name:    "approval.approve",
		CLIPath: []string{"policy", "approve"},
		CLI: &command.CLI[approvalApproveIn]{
			Flags: approveFlags,
			Args:  policyArgs("policy approve", approveFlags, func(in *approvalApproveIn) *string { return &in.OperationID }),
			Run: func(ctx context.Context, call command.CLICall[approvalApproveIn], in approvalApproveIn) error {
				if in.OperationID == "" || in.ConfirmToken == "" {
					return errors.New("usage: agentflow policy approve ID --confirm-token TOKEN")
				}
				return call.Env.Print(call.Invoke(ctx, in))
			},
			Usage:   "  policy approve ID --confirm-token TOKEN   record a reviewed approval for its worker",
			Section: command.SectionWork,
			Order:   4000,
		},
		MCP: &command.MCP{Tool: "agentflow_approval_approve", Description: "After explicit user approval, authorize one worker to resume the reviewed operation. This only records the decision.",
			Annotations: command.LocalWrite},
		// Recording an approval is not itself gated: the token is the
		// person's decision. Gate.Approve writes no audit line, so the
		// Invoker writes one.
		Policy: command.Policy{Class: policy.Local, Action: "approval_approve", Gate: command.GateNone, AuditByInvoker: true},
		// The token names the operation it approves, so this call joins
		// that operation on the timeline.
		Token: func(in *approvalApproveIn) *string { return &in.ConfirmToken },
		Describe: func(in approvalApproveIn) command.Detail {
			return command.Detail{Subject: "approve " + in.OperationID}
		},
		Run: func(_ context.Context, a *app.App, in approvalApproveIn) (any, error) {
			return a.ApproveOperation(in.OperationID, in.ConfirmToken)
		},
	})
}

// jsonFlag declares the policy commands' --json flag. Their output is always
// JSON; the flag is accepted for symmetry with other commands.
func jsonFlag(fs *flag.FlagSet) { fs.Bool("json", true, "print JSON") }

// policyArgs parses the positional arguments of a policy command exactly as
// the legacy command did: the ID (or action) is the first argument when it
// is not a flag, flags may follow it, and otherwise a single positional
// argument after the flags is the ID. More arguments are an error.
func policyArgs[In any](name string, flags func(*flag.FlagSet, *In), id func(*In) *string) func(*In, []string) error {
	return func(in *In, args []string) error {
		target := id(in)
		if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
			*target, args = args[0], args[1:]
		}
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		flags(fs, in)
		if err := fs.Parse(args); err != nil {
			return err
		}
		switch {
		case *target == "" && fs.NArg() == 1:
			*target = fs.Arg(0)
		case fs.NArg() > 0:
			return errors.New("unexpected policy arguments")
		}
		return nil
	}
}
