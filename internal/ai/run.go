package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
)

// The Template Method of one draft ("Phase 8 feature framework"): Prepare
// gathers, filters and builds (steps 1 and 2) and returns the manifest the
// confirmation shows; Start runs the draft session (step 3); Finish, in the
// session host, parses the answer and makes the candidates (steps 4 and
// 5). A feature never calls aisession, the gate or a write.

// NeverSent is the manifest's fixed never_sent list.
var NeverSent = []string{
	"environment values",
	"credentials and tokens agentflow holds",
	"the agentflow home, the console registry and the session store",
	".git contents other than what the diff shows",
	"remote URLs",
	"whole files (only diff hunks)",
	"files outside the worktree",
	"other worktrees and repositories",
	"approval tokens and IDs",
	"your configuration",
	"author e-mail addresses",
}

// Prepared is one prepared draft: the manifest, and what Start sends.
type Prepared struct {
	Manifest Manifest
	feature  FeatureID
	harness  string
	prompt   string
	targets  []byte
	limit    time.Duration
}

// Prompt is the exact prompt. Only the command line shows it, to a person
// at a terminal (--show-prompt); no tool returns it.
func (p Prepared) Prompt() string { return p.prompt }

// Targets is the draft-targets.json content (no data).
func (p Prepared) Targets() []byte { return p.targets }

// manager is the session Manager for o: the app's (tests set a fake) or
// the production one.
func manager(o aisession.Options) aisession.Manager {
	if app.NewAIManager != nil {
		return app.NewAIManager(o)
	}
	return aisession.NewManager(o)
}

// ValidateRequest checks that r names a feature and holds only the inputs
// of that feature, within the limits (usage). It runs before anything is
// read.
func ValidateRequest(r Request) error {
	if !Known(r.Feature) {
		return refuse(CodeUnknownFeature, fmt.Sprintf("There is no draft feature %q.", aisession.SanitizeLine(string(r.Feature), 32)),
			"Use one of: commit, pr, ticket, ac, reply.")
	}
	usage := func(msg string) error {
		return refuse(CodeUsage, msg, "Run agentflow help ai to see which inputs each feature takes.")
	}
	ticket := r.Text != "" || r.Template != "" || r.TemplateRevision != "" || r.Project != "" || r.Type != "" || r.LinkTo != ""
	switch {
	case len(r.Paths) > 0 && r.Feature != FeatureCommit:
		return usage("Paths are only for commit drafts.")
	case ticket && r.Feature != FeatureTicket:
		return usage("Text, template, project, type and link_to are only for ticket drafts.")
	case r.Key != "" && r.Feature != FeatureAC:
		return usage("A task key is only for acceptance-criteria drafts.")
	case r.Thread != "" && r.Feature != FeatureReply:
		return usage("A thread is only for reply drafts.")
	case len(r.Text) > MaxText:
		return usage(fmt.Sprintf("The description is longer than %d bytes.", MaxText))
	case len(r.Hint) > MaxHint:
		return usage(fmt.Sprintf("The hint is longer than %d bytes.", MaxHint))
	case r.TemplateRevision != "" && r.Template == "":
		return usage("A template revision needs a template.")
	case len(r.Paths) > 256:
		return usage("Name at most 256 paths.")
	}
	for _, p := range r.Paths {
		if p == "" || strings.ContainsRune(p, 0) || filepath.IsAbs(p) {
			return usage("Paths are worktree-relative.")
		}
	}
	return nil
}

// Nested reports whether ctx's requester or this process is an AI session:
// a session cannot start a draft.
func Nested(ctx context.Context) bool {
	r, ok := aisession.RequesterFrom(ctx)
	return ok && r.Kind == aisession.RequesterSession || os.Getenv(app.SessionEnv) != ""
}

// Prepare runs every check of ai.draft in its order, gathers the context,
// filters it and builds the prompt. Nothing is started and nothing is
// written. A failed check is a *aisession.Refusal.
func Prepare(ctx context.Context, a *app.App, o aisession.Options, r Request) (Prepared, error) {
	if Nested(ctx) {
		return Prepared{}, refuse(aisession.RefuseNested, "An AI session cannot start a draft.", "Ask the person to run agentflow ai draft in their own terminal.")
	}
	if err := ValidateRequest(r); err != nil {
		return Prepared{}, err
	}
	f, ok := Lookup(r.Feature)
	if !ok {
		return Prepared{}, refuse(CodeUnknownFeature, Label(r.Feature)+" drafts are not in this build of agentflow.", "")
	}
	if a == nil || a.Repo == nil || a.Repo.Dir == "" {
		return Prepared{}, refuse(aisession.RefuseNotWorktree, "A draft needs a git worktree.", "Run the command inside the repository.")
	}
	harnessID, err := Route(a.User, r.Feature)
	if err != nil {
		return Prepared{}, err
	}
	limit := DefaultDraftDuration
	if a.User != nil {
		limit = a.User.AI.Drafts.EffectiveMaxDuration()
	}
	// The session checks, in their order (agent_start_off, repo_disabled,
	// not_worktree, not_ready, plugin_launch, the environment, the caps):
	// the Manager's own Prepare, with the draft profile.
	m := manager(o)
	probe := aisession.StartRequest{Harness: harnessID, Dir: a.Repo.Dir, Detach: true, Draft: &aisession.DraftStart{Feature: string(r.Feature), MaxDuration: limit}}
	plan, err := m.Prepare(ctx, probe)
	if err != nil {
		return Prepared{}, err
	}
	// 1. Gather.
	c, err := f.Gather(ctx, a, r)
	if err != nil {
		return Prepared{}, err
	}
	c.Feature = r.Feature
	if r.Hint != "" {
		c.Parts = append(c.Parts, Part{Kind: PartHint, Label: "the caller's guidance", Source: "caller", Text: r.Hint})
	}
	// 2. Filter and build.
	var caps Caps
	if fc, ok := f.(Caps); ok {
		caps = fc
	}
	home, _ := os.UserHomeDir()
	parts, err := Filter(c.Parts, FilterOptions{Root: a.Repo.Dir, Home: home, Caps: caps})
	if err != nil {
		return Prepared{}, err
	}
	targets := make([]Target, len(c.Targets))
	for i, t := range c.Targets {
		t.Label = visible(aisession.SanitizeLine(t.Label, 512))
		targets[i] = t
	}
	built := PromptBuilder{Feature: r.Feature, Task: f.Task(), Example: f.Example(), Targets: targets, Parts: parts}.Build()
	if len(built.Prompt) > MaxPrompt {
		return Prepared{}, refuse(CodeTooLarge, fmt.Sprintf("The draft's prompt would be %d bytes, more than the %d agentflow sends.", len(built.Prompt), MaxPrompt),
			"Draft with less context: fewer paths, a shorter hint or description.")
	}
	tf, err := json.Marshal(targetsFile{Feature: r.Feature, HarnessName: plan.HarnessName, Rules: c.Rules, Targets: c.Targets, Fixed: c.Fixed})
	if err != nil {
		return Prepared{}, err
	}
	man := Manifest{
		Feature: r.Feature, Harness: plan.Harness, HarnessName: plan.HarnessName, Version: plan.Version, Path: plan.Path,
		Worktree: plan.Worktree, Branch: plan.Branch, StartedBy: plan.StartedBy, Parts: parts, Targets: targets,
		Hint: visible(aisession.Redact(r.Hint)), Bytes: len(built.Prompt), PromptSHA256: built.SHA256, MaxDuration: plan.Limits.MaxDuration,
		NeverSent: append([]string{}, NeverSent...),
	}
	if r.Feature == FeatureTicket {
		man.Text = visible(aisession.Redact(aisession.Clamp(r.Text, 4<<10)))
	}
	if man.Parts == nil {
		man.Parts = []Part{}
	}
	return Prepared{Manifest: man, feature: r.Feature, harness: plan.Harness, prompt: built.Prompt, targets: tf, limit: plan.Limits.MaxDuration}, nil
}

// Start starts the draft session for p: hosted in this process by a person
// at a terminal (detach false), by a detached host otherwise.
func Start(ctx context.Context, a *app.App, p Prepared, o aisession.Options, detach bool) (aisession.Session, error) {
	if p.prompt == "" || a == nil || a.Repo == nil {
		return aisession.Session{}, errors.New("ai: start: the draft is not prepared")
	}
	return manager(o).Start(ctx, aisession.StartRequest{Harness: p.harness, Dir: a.Repo.Dir, Detach: detach, Draft: &aisession.DraftStart{
		Feature: string(p.feature), Prompt: p.prompt, Record: Record(p.Manifest), Targets: p.targets, MaxDuration: p.limit,
	}})
}

// Record is the manifest as text (compact JSON): the transcript's prompt
// item, which the console reads back. It names sources, sizes and hashes,
// never the data.
func Record(m Manifest) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// resolved is p with links resolved, or "" when it cannot be.
func resolved(p string) string {
	if p == "" {
		return ""
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return r
}
