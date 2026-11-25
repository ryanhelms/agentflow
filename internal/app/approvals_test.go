package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nscaledev/agentflow/internal/config"
)

func TestPolicyExplainWorkingCopyPublicationOptIn(t *testing.T) {
	root := newRepo(t) // This feature branch has no target remote.
	if err := os.MkdirAll(filepath.Join(root, ".agentflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	workingPolicy := "policy: {max_autonomy: {remote: confirm, actions: {notify: suggest}}}"
	if err := os.WriteFile(filepath.Join(root, ".agentflow", "config.yml"), []byte(workingPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "config.yml"), []byte("autonomy: {remote: auto}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	a, err := Open(ctx, root, "test")
	if err != nil {
		t.Fatal(err)
	}
	// The same connection picks up the new repository opt-in and the new
	// personal preference. Its PR target is still unreviewed.
	workingPolicy = "policy: {allow_auto_pr_publication: true, max_autonomy: {remote: confirm, actions: {notify: suggest}}}"
	if err := os.WriteFile(filepath.Join(root, ".agentflow", "config.yml"), []byte(workingPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "config.yml"), []byte("autonomy: {remote: auto, actions: {pr_open_draft: suggest}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	listed, err := a.PolicyExplain(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	listedRules := make(map[string][]config.AutonomyDecision)
	for _, row := range listed {
		listedRules[row.Action] = row.Rules
	}
	for action, want := range map[string]config.Level{
		"pr_open": config.Confirm, "pr_open_draft": config.Confirm,
		"push": config.Confirm, "notify": config.Suggest,
	} {
		single, err := a.PolicyExplain(ctx, action)
		if err != nil || len(single) != 1 || len(single[0].Rules) == 0 {
			t.Fatalf("%s: %v, %v", action, single, err)
		}
		if !slices.Equal(single[0].Rules, listedRules[action]) {
			t.Fatalf("%s: individual and full explanations differ", action)
		}
		for _, rule := range single[0].Rules {
			wantRule := want
			if rule.Action == "pr_open_draft" {
				wantRule = config.Suggest
			}
			if rule.Level != wantRule {
				t.Errorf("%s includes %s level %s, want %s", action, rule.Action, rule.Level, wantRule)
			}
		}
	}
	// Losing the config in an opted-in session retains its last class ceiling.
	enabledSession, err := Open(ctx, root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".agentflow", "config.yml")); err != nil {
		t.Fatal(err)
	}
	missing, err := enabledSession.PolicyExplain(ctx, "pr_open")
	if err != nil || len(missing) != 1 || len(missing[0].Rules) != 2 {
		t.Fatal(missing, err)
	}
	for _, rule := range missing[0].Rules {
		if rule.Level != config.Confirm {
			t.Fatalf("missing config permitted %s at %s", rule.Action, rule.Level)
		}
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "config.yml"), []byte("autonomy: private-invalid-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := enabledSession.PolicyExplain(ctx, "pr_open"); err == nil || err.Error() != "cannot read personal approval settings" {
		t.Fatalf("invalid personal settings: %v", err)
	}
}
