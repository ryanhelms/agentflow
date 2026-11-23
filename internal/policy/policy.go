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
	"strings"
	"time"

	"github.com/nscaledev/devflow/internal/config"
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
	return result
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

// Audit appends one JSON line to ~/.devflow/audit.log.
func (g *Gate) Audit(class Class, action string, payload any, result string) {
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	entry := map[string]any{
		"time": time.Now().UTC().Format(time.RFC3339), "client": g.Client, "class": class,
		"action": action, "payload_sha256": fmt.Sprintf("%x", sum[:8]), "result": result,
	}
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
	switch {
	case forcePushRe.MatchString(cmd):
		return "force-push is never allowed through devflow-managed agents"
	case deleteRefRe.MatchString(cmd):
		return "deleting remote branches is never allowed through devflow-managed agents"
	case ghMergeRe.MatchString(cmd):
		return "merging PRs is a human decision; ask the user to merge"
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
