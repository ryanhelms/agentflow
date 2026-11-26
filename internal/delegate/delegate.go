// Package delegate hands a task to an agent session in another repository. A
// session never changes another repository itself (see internal/boundary); it
// sends the task to a session already working there, or starts one in that
// repository's checkout. Transports: tmux panes, the client's native session
// tools (returned as a hand-off for the agent to carry out), and new tmux sessions.
package delegate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nscaledev/agentflow/internal/boundary"
)

// Statuses of a delegation.
const (
	// StatusSend types the task into a running session's tmux pane.
	StatusSend = "send"
	// StatusStart starts a new tmux session in the checkout with the task.
	StatusStart = "start"
	// StatusNative asks the calling agent to use its client's session tools.
	StatusNative = "native"
	// StatusSent and StatusStarted report a delegation that ran.
	StatusSent    = "sent"
	StatusStarted = "started"
)

// PaneOption tags tmux panes that agentflow started with the common Git
// directory of their repository.
const PaneOption = "@agentflow_repo"

// Runner runs a command and returns its standard output. Tests replace it.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// Run is the Runner used by Plan and Apply.
var Run Runner = func(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		err = fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
	}
	return string(out), err
}

// LookPath finds a program. Tests replace it.
var LookPath = exec.LookPath

// Request is what to delegate and where.
type Request struct {
	// Repo names the repository: NAME or OWNER/NAME.
	Repo string
	// Task is the work, in words the receiving session can act on.
	Task string
	// Agent is the client a new session runs: claude or codex.
	Agent string
	// Start always starts a new session.
	Start bool
	// DryRun plans without sending or starting anything.
	DryRun bool
}

// Pane is a tmux pane that runs an agent session.
type Pane struct {
	ID      string `json:"id"`
	Session string `json:"session"`
	Path    string `json:"path"`
	Command string `json:"command"`
	// Managed is true for panes agentflow started.
	Managed bool `json:"managed"`
}

// Plan is a delegation, before or after it ran.
type Plan struct {
	Status   string   `json:"status"`
	Repo     string   `json:"repo"`
	Checkout string   `json:"checkout"`
	From     string   `json:"from"`
	Message  string   `json:"message"`
	Pane     *Pane    `json:"pane,omitempty"`
	Agent    string   `json:"agent,omitempty"`
	Session  string   `json:"session,omitempty"`
	Commands []string `json:"commands,omitempty"`
	DryRun   bool     `json:"dry_run,omitempty"`
	Next     string   `json:"next,omitempty"`

	target boundary.Repo
	argv   [][]string
}

// Agents are the clients a new session can run.
var Agents = []string{"claude", "codex"}

// DefaultAgent picks the agent for a new session: the requested one, else the
// calling client, else claude.
func DefaultAgent(requested, client string) string {
	for _, c := range []string{requested, client} {
		if slices.Contains(Agents, c) {
			return c
		}
	}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "CODEX_") && name != "CODEX_HOME" {
			return "codex"
		}
	}
	return "claude"
}

// NativeSessions reports whether the calling client has its own session tools
// (Claude Code desktop exposes a session messaging socket).
func NativeSessions() bool {
	return os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET") != ""
}

// RepoName is the name part of NAME or OWNER/NAME.
func RepoName(repo string) string {
	repo = strings.TrimSuffix(strings.Trim(repo, "/"), ".git")
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

// Message is the text the receiving session gets.
func Message(from boundary.Repo, task string) string {
	task = strings.Join(strings.Fields(task), " ")
	return fmt.Sprintf("[agentflow delegate from %s] %s", from.Label(), task)
}

// Build plans a delegation from home to the target repository checked out at
// checkout. native says whether the caller can use its client's session tools.
func Build(ctx context.Context, home, target boundary.Repo, checkout string, req Request, native bool, resolve boundary.Resolver) (*Plan, error) {
	if strings.TrimSpace(req.Task) == "" {
		return nil, errors.New("delegate needs a task: say what the other session should do")
	}
	p := &Plan{
		Repo: target.Label(), Checkout: checkout, From: home.Label(),
		Message: Message(home, req.Task), DryRun: req.DryRun, target: target,
	}
	if !req.Start {
		if pane := FindPane(ctx, target, resolve); pane != nil {
			p.Status, p.Pane = StatusSend, pane
			p.argv = [][]string{
				{"tmux", "send-keys", "-t", pane.ID, "-l", p.Message},
				{"tmux", "send-keys", "-t", pane.ID, "Enter"},
			}
			p.Commands = commandLines(p.argv)
			p.Next = fmt.Sprintf("The session in tmux pane %s (%s) receives the task. Check on it with `tmux capture-pane -p -t %s`.", pane.ID, pane.Session, pane.ID)
			return p, nil
		}
		if native {
			p.Status = StatusNative
			p.Next = fmt.Sprintf("No agentflow tmux session works in %s. Use your client's session tools: "+
				"send the message to a session whose folder is %s or one of its worktrees; if there is none, "+
				"start a new session in %s with the message as its first prompt. "+
				"To start a tmux session instead, delegate again with start.", p.Repo, checkout, checkout)
			return p, nil
		}
	}
	if _, err := LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux is not installed, so agentflow cannot start a session; start one in %s yourself and give it this task: %s", checkout, p.Message)
	}
	p.Status, p.Agent = StatusStart, DefaultAgent(req.Agent, "")
	p.Session = sessionName(RepoName(target.Name()))
	p.argv = [][]string{
		{"tmux", "new-session", "-d", "-P", "-F", "#{pane_id}", "-s", p.Session, "-c", checkout,
			"-e", boundary.SessionEnv + "=" + target.CommonDir, p.Agent, p.Message},
		{"tmux", "set-option", "-p", "-t", "<pane>", PaneOption, target.CommonDir},
	}
	p.Commands = commandLines(p.argv)
	p.Next = fmt.Sprintf("A new %s session in tmux session %s works on the task in %s. Attach with `tmux attach -t %s`.", p.Agent, p.Session, checkout, p.Session)
	return p, nil
}

var unsafeSession = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func sessionName(repo string) string {
	return "agentflow-" + unsafeSession.ReplaceAllString(repo, "-") + "-" + time.Now().Format("20060102-150405")
}

// FindPane returns the tmux pane of a running agent session in the target
// repository or one of its worktrees: a pane agentflow started first, then any
// pane running claude, codex or node there. It returns nil without tmux.
func FindPane(ctx context.Context, target boundary.Repo, resolve boundary.Resolver) *Pane {
	out, err := Run(ctx, "tmux", "list-panes", "-a", "-F", "#{pane_id}\t#{session_name}\t#{pane_current_path}\t#{pane_current_command}\t#{"+PaneOption+"}")
	if err != nil {
		return nil
	}
	var found []Pane
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 || f[0] == os.Getenv("TMUX_PANE") {
			continue
		}
		p := Pane{ID: f[0], Session: f[1], Path: f[2], Command: f[3]}
		if len(f) > 4 && f[4] != "" {
			p.Managed = f[4] == target.CommonDir
			if !p.Managed {
				continue // started by agentflow for another repository
			}
		}
		if !p.Managed && !slices.Contains([]string{"claude", "codex", "node"}, p.Command) {
			continue
		}
		if r, ok := resolve(p.Path); !ok || !boundary.Same(r, target) {
			continue
		}
		found = append(found, p)
	}
	slices.SortStableFunc(found, func(a, b Pane) int {
		switch {
		case a.Managed == b.Managed:
			return 0
		case a.Managed:
			return -1
		}
		return 1
	})
	if len(found) == 0 {
		return nil
	}
	return &found[0]
}

// Apply sends or starts what the plan describes and returns the plan with its
// final status. A native plan has nothing to run.
func Apply(ctx context.Context, p *Plan) (*Plan, error) {
	switch p.Status {
	case StatusSend:
		for _, argv := range p.argv {
			if _, err := Run(ctx, argv[0], argv[1:]...); err != nil {
				return p, err
			}
		}
		p.Status = StatusSent
	case StatusStart:
		out, err := Run(ctx, p.argv[0][0], p.argv[0][1:]...)
		if err != nil {
			return p, err
		}
		id := strings.TrimSpace(out)
		p.Pane = &Pane{ID: id, Session: p.Session, Path: p.Checkout, Command: p.Agent, Managed: true}
		tag := slices.Clone(p.argv[1])
		tag[slices.Index(tag, "<pane>")] = id
		if _, err := Run(ctx, tag[0], tag[1:]...); err != nil {
			return p, fmt.Errorf("started %s but could not tag its pane: %w", p.Session, err)
		}
		p.Status = StatusStarted
	}
	return p, nil
}

func commandLines(argv [][]string) []string {
	lines := make([]string, len(argv))
	for i, a := range argv {
		q := make([]string, len(a))
		for j, w := range a {
			q[j] = quote(w)
		}
		lines[i] = strings.Join(q, " ")
	}
	return lines
}

func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`#&;|<>(){}*?[]!~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
