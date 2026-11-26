package ops

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/provider/codexhost"
	"github.com/ryanhelms/agentflow/internal/provider/native"
)

// Shared pieces of the task operations (start, task create and link,
// transition, comment, ac, scope, context, work, standup). Each is a copy of
// what internal/cli and internal/mcpserver did for these tools, so the
// operations behave as before; the legacy copies are deleted by the phase 3
// cleanup. Names carry a "task" prefix so other lanes' files in this package
// do not collide with them.

// taskKeyIn is the input of the tools that take only an optional task key.
type taskKeyIn struct {
	Key string `json:"key,omitempty" jsonschema:"task key or qualified reference; defaults to the branch task"`
}

// taskNativeReadIn is the part of an input that lets a client complete
// provider reads through its own connection (native_read_required).
type taskNativeReadIn struct {
	ProviderMCP   bool           `json:"provider_mcp,omitempty" jsonschema:"This client can complete provider MCP read requests"`
	NativeResults []native.Reply `json:"native_results,omitempty" jsonschema:"Return results for native_read_required from the connected provider MCP"`
}

// taskInteractiveClient reports whether the calling client can prompt its
// user, which lets a Jira write use a signed-in CLI.
func taskInteractiveClient(a *app.App) bool {
	switch a.Client {
	case "claude", "claude-desktop", "codex", "cursor", "gemini", "grok", "vscode":
		return true
	default:
		return false
	}
}

// taskCLITerminal reports whether a is the command line run from a terminal,
// which lets a Jira write use a signed-in CLI.
func taskCLITerminal(a *app.App) bool {
	return a.Client == "cli" && term.IsTerminal(int(os.Stdin.Fd()))
}

// taskProviderContext gives ctx the client's provider connection for reads:
// the replies it returned for an earlier native_read_required, and for Codex
// a direct host. The command line has neither, so it returns ctx unchanged.
func taskProviderContext(ctx context.Context, a *app.App, in taskNativeReadIn) context.Context {
	if !in.ProviderMCP && len(in.NativeResults) == 0 && !taskInteractiveClient(a) {
		return ctx
	}
	client := native.Client{Replies: in.NativeResults}
	if a.Client == "codex" {
		client.Direct = taskCodexHost{dir: a.Repo.Dir}
	}
	return native.WithHost(ctx, client)
}

type taskCodexHost struct{ dir string }

func (h taskCodexHost) Read(ctx context.Context, call native.Call) (json.RawMessage, error) {
	raw, err := codexhost.Call(ctx, h.dir, call.Server, call.Tool, call.Arguments)
	var hostError *codexhost.Error
	if !errors.As(err, &hostError) {
		return raw, err
	}
	switch hostError.Kind {
	case codexhost.Unavailable:
		return nil, native.ErrUnavailable
	case codexhost.Authentication:
		return nil, fmt.Errorf("%s: %w; start the client's provider OAuth flow", call.Provider, native.ErrSignIn)
	case codexhost.Permission:
		return nil, fmt.Errorf("%s: %w", call.Provider, native.ErrDenied)
	default:
		return nil, err
	}
}

// taskAsk prints prompt and reads one line from standard input.
func taskAsk(prompt string) string {
	fmt.Print(prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line)
}

// taskPrintApproval shows what a person approves: the policy line and the
// reason on standard error, the reviewed payload on standard output.
func taskPrintApproval(env command.CLIEnv, p *app.Pending) error {
	if p.Policy != nil {
		fmt.Fprintf(os.Stderr, "Agentflow %s: %s (%s)", p.Policy.Action, p.Policy.Level, p.Policy.Source)
		if p.Approval != nil {
			fmt.Fprintf(os.Stderr, "; request %s, owner %s, expires %s", p.Approval.OperationID, p.Approval.PromptOwner, p.Approval.ExpiresAt.Format("15:04:05 MST"))
		}
		fmt.Fprintln(os.Stderr)
	}
	fmt.Fprintln(os.Stderr, p.Reason)
	if p.Status == "awaiting_approval" {
		return env.Print(p, nil)
	}
	return env.Print(p.Summary, nil)
}

// taskParseInterleaved parses flags that may appear before, between or after
// positional arguments. Everything after the flag terminator is literal,
// including a comment body or checklist item spelled "--yes".
func taskParseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
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
