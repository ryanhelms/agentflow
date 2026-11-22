package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nscaledev/devflow/internal/commit"
	"github.com/nscaledev/devflow/internal/flowmap"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
)

// CommitPlan groups the working tree into proposed commits.
func (a *App) CommitPlan(ctx context.Context) ([]commit.Group, error) {
	staged, err := a.Repo.StagedFiles(ctx)
	if err != nil {
		return nil, err
	}
	files := staged
	if len(files) == 0 {
		changes, err := a.Repo.Changes(ctx)
		if err != nil {
			return nil, err
		}
		for _, c := range changes {
			files = append(files, c.Path)
		}
	}
	if len(files) == 0 {
		return nil, errors.New("nothing to commit")
	}
	return commit.Plan(files), nil
}

// CommitContext is what the agent needs to write a message.
type CommitContext struct {
	Key           string   `json:"key"`
	TaskRef       string   `json:"task_ref,omitempty"`
	Ticket        string   `json:"ticket,omitempty"`
	Files         []string `json:"files"`
	SuggestedType string   `json:"suggested_type"`
	Breaking      []string `json:"breaking_paths,omitempty"`
	DiffStat      string   `json:"diffstat"`
	Diff          string   `json:"diff"`
	DiffTruncated bool     `json:"diff_truncated,omitempty"`
	SuggestVerb   string   `json:"suggested_verb,omitempty"`
	Rules         string   `json:"rules"`
}

// CommitContext stages paths (if given) and returns rules, ticket and diff.
func (a *App) CommitContext(ctx context.Context, paths []string) (*CommitContext, error) {
	if len(paths) > 0 {
		if err := a.Repo.Add(ctx, paths...); err != nil {
			return nil, err
		}
	}
	files, err := a.Repo.StagedFiles(ctx)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("nothing staged; pass paths or stage changes first")
	}
	diff, trunc, err := a.Repo.StagedDiff(ctx, 24000)
	if err != nil {
		return nil, err
	}
	cc := &CommitContext{
		Files:         files,
		SuggestedType: commit.TypeFromFiles(files),
		Breaking:      commit.Breaking(files, a.Cfg.Commit.BreakingPaths),
		DiffStat:      a.Repo.DiffStat(ctx),
		Diff:          diff,
		DiffTruncated: trunc,
		Rules:         a.rulesText(),
	}
	if a.UsesTaskRefs() {
		ref, err := a.branchTask(ctx)
		if err != nil {
			return nil, err
		}
		if ref.ID != "" {
			cc.Key = taskKey(ref)
			cc.TaskRef = ref.String()
			cc.Rules += "\nInclude this trailer after a blank line:\n" + provider.TaskTrailer + ref.String()
			if task, err := a.getTask(ctx, ref); err == nil {
				cc.Ticket = task.Title + " (status: " + task.State.Name + ")"
			}
		}
		return cc, nil
	}
	cc.Key = a.CurrentKey(ctx)
	if cc.Key != "" {
		if jc, err := a.Jira(); err == nil {
			if iss, err := jc.GetIssue(ctx, cc.Key); err == nil {
				cc.Ticket = iss.Fields.Summary + " (status: " + iss.Fields.Status.Name + ")"
				if iss.Fields.Status.Category.Key == "new" {
					cc.SuggestVerb = "start"
				}
			}
		}
	}
	return cc, nil
}

func (a *App) rulesText() string {
	r := a.Cfg.Commit
	s := fmt.Sprintf(`Subject: type(KEY[,KEY])[!]: summary   (≤%d chars)
- type: one of %s
- KEY: the Jira key (use the branch key unless the change belongs elsewhere)
- summary: imperative mood, lowercase start, no trailing period
- "!" plus a "BREAKING CHANGE: <what and how to migrate>" footer when an API, schema or migration breaks callers
Optional verbs after the summary: #start #review #qa #done #block (moved via the flow map; #done only matters on the default branch)
Optional Smart Commits: #comment <text>, #time 1h 30m
Body: blank line, then why the change was needed (not a list of files). Wrap at 72.
Trailers: "Refs: KEY-2" for related tickets.`, r.SubjectMax, strings.Join(r.Types, ", "))
	if a.UsesTaskRefs() {
		subject := "type[(scope)][!]: summary"
		if r.KeyRequired() {
			subject = "type(KEY[,KEY])[!]: summary"
		}
		s = fmt.Sprintf("Subject: %s (≤%d chars). Use one of %s. Use an imperative, lowercase summary without a trailing period. Document breaking changes in a BREAKING CHANGE footer. Use a qualified Task-Ref trailer for the issue.", subject, r.SubjectMax, strings.Join(r.Types, ", "))
	}
	if !r.AITrailer {
		s += "\nDo not add AI attribution trailers (Co-authored-by, Generated with...)."
	}
	return s
}

// Validate validates a message with the repo rules and branch key hint.
func (a *App) Validate(ctx context.Context, msg string) []commit.Issue {
	if a.UsesTaskRefs() {
		return a.validateTaskMessage(msg)
	}
	return commit.Validate(msg, a.Cfg.Commit, a.CurrentKey(ctx))
}

func (a *App) validateTaskMessage(msg string) []commit.Issue {
	issues := commit.Validate(msg, a.Cfg.Commit, "")
	_, body, _ := strings.Cut(msg, "\n")
	if _, err := a.taskKeys(body); err != nil {
		issues = append(issues, commit.Issue{Code: "task_ref", Message: err.Error()})
	}
	return issues
}

// Effect is one predicted task change.
type Effect struct {
	Key  string `json:"key"`
	When string `json:"when"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Note string `json:"note,omitempty"`
}

// Preview predicts what a message (and the next PR events) will do in Jira.
func (a *App) Preview(ctx context.Context, msg string) ([]Effect, []string, error) {
	m := commit.Parse(msg)
	var effects []Effect
	var warnings []string
	if m.Skip {
		return nil, []string{"message has [devflow skip]; automation ignores it"}, nil
	}
	if a.UsesTaskRefs() {
		var err error
		m.Keys, err = a.taskKeys(m.Body)
		if err != nil {
			return nil, nil, err
		}
		if m.Comment != "" || m.Time != "" {
			name := "GitHub Issues"
			if a.usesFileTasks() {
				name = a.Cfg.Providers.TaskManagement.Provider
			}
			warnings = append(warnings, name+" does not apply Jira Smart Commit commands")
			m.Comment, m.Time = "", ""
		}
	}
	branch := a.Repo.Branch(ctx)
	onDefault := branch == a.Repo.DefaultBranch(ctx)
	for _, key := range m.Keys {
		for _, v := range m.Verbs {
			if v == "done" && !onDefault {
				warnings = append(warnings, key+": #done only applies when the commit lands on the default branch")
				if a.UsesTaskRefs() {
					continue
				}
			}
			plan, err := a.PlanTransition(ctx, key, "#"+v)
			if err != nil {
				return nil, nil, err
			}
			effects = append(effects, Effect{Key: key, When: "on push (#" + v + ")", From: plan.From, To: plan.To, Note: plan.Note})
			if plan.To == "" && plan.Note != "" {
				warnings = append(warnings, fmt.Sprintf("%s: #%s does nothing (%s)", key, v, plan.Note))
			}
		}
		for _, ev := range []string{flowmap.PROpened, flowmap.PRMerged} {
			plan, err := a.PlanTransition(ctx, key, ev)
			if err != nil && a.UsesTaskRefs() {
				return nil, nil, err
			}
			if err == nil && plan.To != "" {
				effects = append(effects, Effect{Key: key, When: "on " + ev, From: plan.From, To: plan.To})
			}
		}
	}
	if m.Comment != "" {
		effects = append(effects, Effect{Key: strings.Join(m.Keys, ","), When: "on push", Note: "Smart Commit comment: " + m.Comment})
	}
	if m.Time != "" {
		effects = append(effects, Effect{Key: strings.Join(m.Keys, ","), When: "on push", Note: "Smart Commit log work: " + m.Time})
	}
	return effects, warnings, nil
}

// CommitResult is returned after a commit.
type CommitResult struct {
	Status  string   `json:"status"`
	SHA     string   `json:"sha"`
	Message string   `json:"message"`
	Files   []string `json:"files"`
}

// Commit validates and commits. paths limits the commit to those files (they are staged first).
func (a *App) Commit(ctx context.Context, msg string, paths []string, token string) (any, error) {
	if !a.Cfg.Commit.AITrailer {
		msg = commit.StripAITrailers(msg)
	}
	if issues := a.Validate(ctx, msg); len(issues) > 0 {
		return map[string]any{"status": "invalid", "issues": issues}, nil
	}
	if len(paths) > 0 {
		if err := a.Repo.Add(ctx, paths...); err != nil {
			return nil, err
		}
	}
	files := paths
	if len(files) == 0 {
		var err error
		if files, err = a.Repo.StagedFiles(ctx); err != nil {
			return nil, err
		}
	}
	if len(files) == 0 {
		return nil, errors.New("nothing staged to commit")
	}
	if hits := ScanSecrets(ctx, a); len(hits) > 0 {
		return map[string]any{"status": "blocked", "reason": "possible secrets in staged changes", "findings": hits}, nil
	}
	effects, _, _ := a.Preview(ctx, msg)
	summary := map[string]any{"message": msg, "files": files, "branch": a.Repo.Branch(ctx), "jira_effects": effects}
	cmd := "git commit -F - <<'EOF'\n" + msg + "EOF"
	if p, err := a.gate(policy.Local, "commit", summary, token, false, []string{cmd}); p != nil || err != nil {
		return p, err
	}
	sha, err := a.Repo.Commit(ctx, msg, paths)
	a.audit(policy.Local, "commit", summary, err)
	if err != nil {
		return nil, err
	}
	return CommitResult{Status: "committed", SHA: sha, Message: strings.SplitN(msg, "\n", 2)[0], Files: files}, nil
}

// LintBranch validates every commit on the branch since it left the default branch.
func (a *App) LintBranch(ctx context.Context) (map[string][]commit.Issue, error) {
	base := a.Repo.BaseRef(ctx)
	out, err := a.Repo.Git(ctx, "log", "--format=%h%x00%B%x01", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	bad := map[string][]commit.Issue{}
	for _, entry := range strings.Split(out, "\x01") {
		sha, msg, ok := strings.Cut(strings.TrimSpace(entry), "\x00")
		if !ok {
			continue
		}
		var issues []commit.Issue
		if a.UsesTaskRefs() {
			issues = a.validateTaskMessage(msg)
		} else {
			issues = commit.Validate(msg, a.Cfg.Commit, "")
		}
		if len(issues) > 0 {
			bad[sha] = issues
		}
	}
	return bad, nil
}
