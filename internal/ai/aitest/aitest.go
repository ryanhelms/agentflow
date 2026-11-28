// Package aitest is a "Draft with AI" feature for tests (ADR "Phase 8 test
// guard"): Feature plays the role of a real feature with fixed parts and
// targets, so the framework, the command and the console are tested before
// (and apart from) lanes 8.2 and 8.3. Use registers it for one test.
package aitest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/app"
)

// Feature is a test feature. Its answer is Answer: a message that becomes
// one commit.prepare candidate (with the fixed paths), and items whose
// index maps to a target and becomes one ac.update candidate each.
type Feature struct {
	FeatureID ai.FeatureID
	Parts     []ai.Part
	Targets   []ai.Target
	Fixed     map[string]any
	// Refuse, when set, is what Gather returns.
	Refuse error
	// Gathered counts Gather calls.
	Gathered int
	// GatherFn, when set, builds the parts (to read the worktree).
	GatherFn func(ctx context.Context, a *app.App, r ai.Request) ([]ai.Part, error)
}

// Answer is Feature's answer struct.
type Answer struct {
	Message string `json:"message" draft:"max=8192"`
	Title   string `json:"title,omitempty" draft:"max=256,line"`
	Body    string `json:"body,omitempty" draft:"max=4096"`
	Items   []Item `json:"items,omitempty"`
}

// Item names a target by index.
type Item struct {
	Target int    `json:"target"`
	Note   string `json:"note" draft:"max=200,line"`
}

func (f *Feature) ID() ai.FeatureID {
	if f.FeatureID == "" {
		return ai.FeatureCommit
	}
	return f.FeatureID
}

func (f *Feature) Gather(ctx context.Context, a *app.App, r ai.Request) (ai.Context, error) {
	f.Gathered++
	if f.Refuse != nil {
		return ai.Context{}, f.Refuse
	}
	parts := append([]ai.Part{}, f.Parts...)
	if f.GatherFn != nil {
		p, err := f.GatherFn(ctx, a, r)
		if err != nil {
			return ai.Context{}, err
		}
		parts = append(parts, p...)
	}
	fixed := map[string]any{"paths": r.Paths}
	for k, v := range f.Fixed {
		fixed[k] = v
	}
	b, _ := json.Marshal(fixed)
	return ai.Context{Feature: f.ID(), Parts: parts, Targets: f.Targets, Fixed: b}, nil
}

func (f *Feature) Task() string { return "Write a commit message for the changes." }

func (f *Feature) Answer() any { return &Answer{} }

func (f *Feature) Example() string { return `{"message": "feat: add a greeting"}` }

func (f *Feature) Candidates(c ai.Context, answer any) ([]ai.Candidate, []ai.Issue) {
	a := answer.(*Answer)
	var fixed struct {
		Paths []string `json:"paths"`
	}
	_ = json.Unmarshal(c.Fixed, &fixed)
	var out []ai.Candidate
	var issues []ai.Issue
	if a.Message != "" || a.Title != "" {
		in, _ := json.Marshal(map[string]any{"message": a.Message, "paths": fixed.Paths})
		cand := ai.Candidate{Operation: "commit.prepare", Tool: "agentflow_commit_prepare", Input: in,
			CLI:    append([]string{"agentflow", "commit", "--prepare", "-m", a.Message}, pathArgs(fixed.Paths)...),
			Fields: []ai.Field{{Name: "message", Label: "Commit message", Value: a.Message}}}
		if a.Body != "" {
			cand.Fields = append(cand.Fields, ai.Field{Name: "body", Label: "Body", Value: a.Body})
		}
		if !strings.HasPrefix(a.Message, "feat: ") && !strings.HasPrefix(a.Message, "fix: ") {
			cand.Issues = append(cand.Issues, ai.Issue{Code: "convention", Field: "message", Message: "The subject does not follow the commit convention."})
		}
		out = append(out, cand)
	}
	seen := map[int]bool{}
	for _, it := range a.Items {
		if it.Target < 1 || it.Target > len(c.Targets) || seen[it.Target] {
			issues = append(issues, ai.Issue{Code: "target", Message: "The answer named a target agentflow did not give, or one twice."})
			continue
		}
		seen[it.Target] = true
		t := c.Targets[it.Target-1]
		in, _ := json.Marshal(map[string]any{"item": t.ID, "done": true, "evidence": it.Note})
		out = append(out, ai.Candidate{Operation: "ac.update", Tool: "agentflow_ac_update", Input: in, Target: &t,
			CLI:    []string{"agentflow", "ac", "done", t.ID, "--evidence", it.Note},
			Fields: []ai.Field{{Name: "evidence", Label: "Evidence", Value: it.Note}}})
	}
	return out, issues
}

func pathArgs(paths []string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, "--path", p)
	}
	return out
}

// Use registers f for the test and restores the registry afterwards.
func Use(tb testing.TB, f *Feature) *Feature {
	tb.Helper()
	restore := ai.Replace(f)
	tb.Cleanup(restore)
	return f
}
