// Package policy decides whether an action may run (autonomy levels), issues
// single-use confirmation tokens, keeps the audit log, and enforces the
// "never" list that applies at every autonomy level.
package policy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/boundary"
	"github.com/ryanhelms/agentflow/internal/config"
)

// Class is the kind of side effect an action has.
type Class string

const (
	Read   Class = "read"
	Local  Class = "local"
	Remote Class = "remote"
)

// Outcome of a gate check.
type Outcome string

const (
	Proceed           Outcome = "proceed"
	SuggestOnly       Outcome = "suggest"
	NeedsConfirmation Outcome = "needs_confirmation"
	AwaitingApproval  Outcome = "awaiting_approval"
	Prepared          Outcome = "prepared"
)

// TokenTTL is also the lifetime of an existing native provider write lease.
const TokenTTL = config.DefaultApprovalWindow

// Gate applies the effective policy and shares pending approvals within a repository.
type Gate struct {
	Autonomy config.Autonomy
	Client   string
	// PreviewOnly is set by a CLI that displays an informational preview and exits.
	// Such a preview neither dispatches the operation nor acquires the prompt.
	PreviewOnly bool
	secret      []byte
	owner       string
	auditDir    string
	scope       string
	user        *config.User
	repo        *config.Repo
	initial     config.Autonomy
	now         func() time.Time
	// ceiling caps every level Explain resolves (WithCeiling); "" for none.
	ceiling config.Level
}

// Configure scopes operations to the repository and enables action-specific policy.
// The caller uses the common Git directory as the identity for linked worktrees.
// Configure is called before the gate begins serving requests.
func (g *Gate) Configure(repository string, user *config.User, repo *config.Repo) {
	if resolved, err := filepath.EvalSymlinks(repository); err == nil {
		repository = resolved
	}
	g.scope, g.user, g.repo = repository, user, repo
}

// WithConfiguration keeps the prompt owner and journal while resolving each
// operation against a new on-disk policy. Existing callers keep their snapshot.
func (g *Gate) WithConfiguration(repository string, user *config.User, repo *config.Repo) *Gate {
	current := *g
	current.Autonomy = config.Effective(user, repo)
	current.initial = current.Autonomy
	current.Configure(repository, user, repo)
	return &current
}

// Decision is the result of a policy check.
type Decision struct {
	Outcome  Outcome                  `json:"status"`
	Token    string                   `json:"confirm_token,omitempty"`
	Reason   string                   `json:"reason,omitempty"`
	Approval *Approval                `json:"approval,omitempty"`
	Policy   *config.AutonomyDecision `json:"policy,omitempty"`
}

// Explain returns the strictest rule among the effects of the operation.
func (g *Gate) Explain(class Class, action string, related []string, force bool) config.AutonomyDecision {
	u := config.User{Autonomy: g.Autonomy}
	if g.user != nil {
		u = *g.user
		// Honor callers that override the class level after constructing a gate.
		if g.initial.Local != g.Autonomy.Local {
			u.Autonomy.Local = g.Autonomy.Local
		}
		if g.initial.Remote != g.Autonomy.Remote {
			u.Autonomy.Remote = g.Autonomy.Remote
		}
	}
	rules := config.ResolveOperationAutonomy(&u, g.repo, string(class), action, related, force)
	result := rules[0]
	for _, candidate := range rules[1:] {
		if candidate.Level != result.Level && config.Min(candidate.Level, result.Level) == candidate.Level {
			result = candidate
		}
	}
	return g.clamp(result)
}

func (g *Gate) Check(class Class, action string, payload any, token string, force bool) (Decision, error) {
	return g.CheckActions(class, action, nil, payload, token, force, false)
}

func (g *Gate) CheckActions(class Class, action string, related []string, payload any, token string, force, prepareOnly bool) (Decision, error) {
	return g.CheckActionsUntil(class, action, related, payload, token, force, prepareOnly, time.Time{})
}

// CheckActionsUntil also enforces the expiry of an already-bound provider session.
func (g *Gate) CheckActionsUntil(class Class, action string, related []string, payload any, token string, force, prepareOnly bool, deadline time.Time) (Decision, error) {
	rule := g.Explain(class, action, related, force)
	d := Decision{Policy: &rule, Outcome: Proceed}
	if !deadline.IsZero() && !g.now().Before(deadline) {
		return Decision{}, ErrExpired
	}
	if class == Read {
		return d, nil
	}
	if rule.Level == config.Suggest {
		d.Outcome, d.Reason = SuggestOnly, fmt.Sprintf("%s sets %s to suggest. Run it yourself", rule.Source, rule.Action)
		return d, nil
	}
	if g.PreviewOnly {
		d.Outcome, d.Reason = Prepared, "informational preview only"
		return d, nil
	}
	directAuto := rule.Level == config.Auto && token == "" && !prepareOnly
	if directAuto && rule.Source != config.PRPublicationPolicySource {
		return d, nil
	}
	digest, err := g.fingerprint(class, action, related, payload)
	if err != nil {
		return Decision{}, err
	}
	err = g.locked(func() error {
		if token != "" {
			if prepareOnly {
				return fmt.Errorf("preparing an operation cannot redeem an approval")
			}
			return g.consume(&d, token, digest)
		}
		if err := g.prepare(&d, class, action, related, digest, force, deadline); err != nil || !directAuto {
			return err
		}
		// A direct automatic PR can race another agent's preview. Redeem the
		// same snapshot under this lock so an older receipt cannot replay it.
		id := d.Approval.OperationID
		d = Decision{Policy: &rule, Outcome: Proceed}
		return g.consume(&d, g.token(id), digest)
	})
	return d, err
}

// Audit appends one JSON line to ~/.agentflow/audit.log.
func (g *Gate) Audit(class Class, action string, payload any, result string) {
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	entry := map[string]any{
		"time": time.Now().UTC().Format(time.RFC3339), "client": g.Client, "class": class,
		"action": action, "payload_sha256": fmt.Sprintf("%x", sum[:8]), "result": result,
	}
	g.appendAudit(entry)
}

// appendAudit writes one JSON line to audit.log. Audit lines are best
// effort: a write error never changes the operation's result.
func (g *Gate) appendAudit(entry map[string]any) {
	line, _ := json.Marshal(entry)
	f, err := os.OpenFile(filepath.Join(g.auditDir, "audit.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// ---- never list ----

var (
	forcePushRe  = regexp.MustCompile(`\bgit\s+push\b[^|;&]*(\s--force\b|\s-f\b|\s--force-with-lease\b|\s\+\S+)`)
	deleteRefRe  = regexp.MustCompile(`\bgit\s+push\b[^|;&]*(\s--delete\b|\s-d\b|\s:\S+)`)
	ghMergeRe    = regexp.MustCompile(`\bgh\s+pr\s+merge\b`)
	pushTargetRe = regexp.MustCompile(`\bgit\s+push\s+(?:-\S+\s+)*\S+\s+(?:\S+:)?(\S+)`)
)

// Forbidden returns a reason if a shell command is on the never list.
func Forbidden(cmd string, protected []string, currentBranch string) string {
	return ForbiddenWith(cmd, protected, currentBranch, NeverOptions{})
}

// NeverOptions relax the never-list for configured roles.
type NeverOptions struct {
	// CoordinatorMerge (policy.coordinator_merge) allows `gh pr merge` when
	// MergeBase reports a base other than main or master.
	CoordinatorMerge bool
	// MergeBase returns the base branch of the PR a `gh pr merge` command names.
	MergeBase func(cmd string) (string, error)
}

// ReleaseBranches are the bases a coordinator may never merge into.
var ReleaseBranches = []string{"main", "master"}

// ForbiddenWith is Forbidden with the configured relaxations.
func ForbiddenWith(cmd string, protected []string, currentBranch string, opt NeverOptions) string {
	if reason := ForbiddenAlways(cmd); reason != "" {
		return reason
	}
	switch {
	case forcePushRe.MatchString(cmd):
		return "force-push is never allowed through agentflow-managed agents"
	case deleteRefRe.MatchString(cmd):
		return "deleting remote branches is never allowed through agentflow-managed agents"
	case ghMergeRe.MatchString(cmd):
		if reason := coordinatorMerge(cmd, opt); reason != "" {
			return reason
		}
		return ""
	}
	target := ""
	if m := pushTargetRe.FindStringSubmatch(cmd); m != nil {
		target = m[1]
	} else if regexp.MustCompile(`\bgit\s+push\b`).MatchString(cmd) {
		target = currentBranch
	}
	if target != "" && IsProtected(target, protected) {
		return fmt.Sprintf("pushing to protected branch %q is never allowed; push a feature branch and open a PR", target)
	}
	return ""
}

// ForbiddenAlways returns a reason for the never-list entries that hold in
// every mode and repository, including pre-bash --convention-only. Plugins
// belong to the user, not to a repository, so no repository's mode lets an
// agent add, remove or approve one.
func ForbiddenAlways(cmd string) string {
	if changesPlugins(cmd) {
		return "adding, removing and approving plugins is a human decision; ask the user to run agentflow plugin add, remove or approve in their own terminal"
	}
	return ""
}

// namesSomething reports whether args hold more than options: a plugin source
// or an ID. Without one, as in "plugin add --help", nothing can be decided.
func namesSomething(args []string) bool {
	return slices.ContainsFunc(args, func(a string) bool { return a == "-" || !strings.HasPrefix(a, "-") })
}

// pluginDecisions are the plugin subcommands that only a person may run:
// installing, removing, and approving what a plugin asks to write.
var pluginDecisions = []string{"add", "remove", "approve"}

// changesPlugins reports whether cmd runs one of agentflow's plugin
// decisions. It finds the program by name, by the path of a launcher or of a
// cached build, or through go run, also inside a shell, script or another
// wrapper (boundary.SimpleCommands). It does not see every form: not a command
// built while it runs, piped into a shell or written to a file first, nor one
// behind an option that takes a separate value (sudo -u me). This stops
// ordinary agent use, not a deliberate way around. Unlike the patterns above, a command
// that only mentions the words in an argument of another program does not
// match.
func changesPlugins(cmd string) bool {
	for _, words := range boundary.SimpleCommands(cmd) {
		program, args := words[0], words[1:]
		switch name := boundary.ProgramName(program); {
		case name == "agentflow":
		// The Claude Code plugin's launcher is bin/launch.
		case name == "launch" && (strings.Contains(strings.ToLower(program), "agentflow") || strings.Contains(program, "CLAUDE_PLUGIN_ROOT")):
		case name == "go" && len(args) > 1 && args[0] == "run" && boundary.ProgramName(args[1]) == "agentflow":
			args = args[2:]
		default:
			continue
		}
		if len(args) > 1 && args[0] == "--native-client" {
			args = args[2:]
		}
		if len(args) > 1 && args[0] == "plugin" && slices.Contains(pluginDecisions, args[1]) && namesSomething(args[2:]) {
			return true
		}
	}
	return false
}

// IsProtected reports whether branch matches any protected pattern.
func IsProtected(branch string, patterns []string) bool {
	branch = strings.TrimPrefix(branch, "refs/heads/")
	for _, p := range patterns {
		if ok, _ := path.Match(p, branch); ok {
			return true
		}
	}
	return false
}

func coordinatorMerge(cmd string, opt NeverOptions) string {
	const human = "merging PRs is a human decision; ask the user to merge"
	if !opt.CoordinatorMerge || opt.MergeBase == nil {
		return human
	}
	// A newline runs a second command just like ";" does.
	if strings.ContainsAny(cmd, ";&|`$\n\r") {
		return human + " (policy.coordinator_merge allows only a single gh pr merge command)"
	}
	base, err := opt.MergeBase(cmd)
	if err != nil || base == "" {
		return human + " (policy.coordinator_merge could not determine the PR base)"
	}
	if slices.Contains(ReleaseBranches, base) {
		return fmt.Sprintf("merging into %s is a release decision; ask the user (agentflow promote opens that PR)", base)
	}
	return ""
}
