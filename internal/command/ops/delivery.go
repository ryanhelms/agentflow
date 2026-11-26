package ops

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/internal/command"
)

// This file holds what the delivery operations share: commit, push, pr,
// review, status and collisions (phase 3, lane 3.2). Their names carry the
// family so that other lanes' files in this package cannot clash with them.

// deliveryNoIn is the input of the tools that take none.
type deliveryNoIn struct{}

// branchTokenIn is the input of push and pr.mark-ready: the confirm token.
type branchTokenIn struct {
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"original confirmation token or a shared operation ID after approval"`
}

// appliedMap is the map of a push, a PR or a mark-ready that finished.
//
// The app methods return one Go type, map[string]any, for a success
// ({"status": "pushed"}) and for a refusal ({"status": "not_ready"}), so
// Classify could not tell them apart. Run gives the success its own named
// type (appliedStatus), which marshals exactly as the map does, so the output
// of the CLI and the tool is unchanged; a refusal stays a plain map and
// Classify reports it as op.failed with its status as the code.
type appliedMap map[string]any

// appliedStatus returns a function that converts the result of an app
// method into an appliedMap when it is a map whose status is one of
// statuses. It returns every other result, a Pending, a refusal map or an
// error, unchanged.
func appliedStatus(statuses ...string) func(any, error) (any, error) {
	return func(v any, err error) (any, error) {
		if m, ok := v.(map[string]any); ok && err == nil {
			if s, _ := m["status"].(string); slices.Contains(statuses, s) {
				return appliedMap(m), nil
			}
		}
		return v, err
	}
}

// firstLine returns s up to its first line break.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimRight(line, "\r")
}

// branchWriteVerb is the CLI command of an operation that publishes the
// current branch in two steps, prepare then apply: "push" and
// "pr mark-ready". It parses and answers like the legacy runBranchWrite.
func branchWriteVerb[PrepareOut, ApplyOut any](path []string, usage string, order int,
	prepare command.Spec[deliveryNoIn, PrepareOut], apply command.Spec[branchTokenIn, ApplyOut]) command.Verb {
	name := strings.Join(path, " ")
	return command.Verb{
		Path: path, Usage: usage, Section: command.SectionWork, Order: order,
		Run: func(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
			fs := flag.NewFlagSet(name, flag.ContinueOnError)
			prepareOnly := fs.Bool("prepare", false, "prepare the operation without applying it")
			yes := fs.Bool("yes", false, "approve the displayed operation")
			token := fs.String("confirm-token", "", "resume an exact approval")
			if err := fs.Parse(args); err != nil {
				return err
			}
			if fs.NArg() != 0 {
				return fmt.Errorf("unexpected arguments to %s", name)
			}
			a, err := env.Open(ctx)
			if err != nil {
				return err
			}
			origin := env.Origin(a)
			if *prepareOnly {
				if *yes || *token != "" {
					return errors.New("--prepare cannot be combined with --yes or --confirm-token")
				}
				return env.Print(command.Invoke(ctx, inv, a, prepare, deliveryNoIn{}, origin))
			}
			if *token != "" {
				return env.Print(command.Invoke(ctx, inv, a, apply, branchTokenIn{ConfirmToken: *token}, origin))
			}
			return env.Confirm(*yes, func(t string) (any, error) {
				return command.Invoke(ctx, inv, a, apply, branchTokenIn{ConfirmToken: t}, origin)
			})
		},
	}
}

// parseInterleavedArgs parses flags that may appear before, between or after
// positional arguments. Everything after the flag terminator is literal.
// It is the rule of the legacy parseInterleaved in internal/cli.
func parseInterleavedArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var options, positional []string
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			positional = append(positional, args...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		options = append(options, arg)
		name, _, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if f := fs.Lookup(name); f != nil && !inline && len(args) > 0 {
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !b.IsBoolFlag() {
				options = append(options, args[0])
				args = args[1:]
			}
		}
	}
	return positional, fs.Parse(options)
}

// askLine prints prompt and reads one line from standard input, as the
// legacy ask in internal/cli does.
func askLine(prompt string) string {
	fmt.Print(prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line)
}

// indentBody indents a review comment under its heading.
func indentBody(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ")
}
