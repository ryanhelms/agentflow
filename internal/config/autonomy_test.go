package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScopedAutonomy(t *testing.T) {
	user := &User{Autonomy: Autonomy{Local: Confirm, Remote: Confirm, Actions: map[string]Level{"commit": Auto, "pr_open_draft": Auto, "ticket_create": Auto}}}
	repo := &Repo{}
	repo.Policy.MaxAutonomy = Autonomy{Local: Auto, Remote: Confirm, Actions: map[string]Level{"pr_open_draft": Auto}}
	for _, tc := range []struct {
		class, action string
		force         bool
		want          Level
		source        string
	}{
		{"local", "commit", false, Auto, "autonomy.actions.commit"},
		{"remote", "commit", false, Confirm, "autonomy.remote"},
		{"local", "adr_draft", false, Confirm, "autonomy.local"},
		{"remote", "pr_open_draft", false, Confirm, "policy.max_autonomy.remote"},
		{"remote", "pr_open", false, Confirm, "autonomy.remote"},
		{"local", "ticket_create", true, Confirm, "mandatory confirmation"},
	} {
		got := ResolveAutonomy(user, repo, tc.class, tc.action, tc.force)
		if got.Level != tc.want || got.Source != tc.source || got.ForceConfirm != tc.force {
			t.Errorf("%s %s: got %+v", tc.class, tc.action, got)
		}
	}
	repo.Policy.MaxAutonomy.Actions["commit"] = Suggest
	got := ResolveAutonomy(user, repo, "local", "commit", true)
	if got.Level != Suggest || got.Source != "policy.max_autonomy.actions.commit" || got.UserClass != Confirm || got.UserAction != Auto || got.RepoClass != Auto || got.RepoAction != Suggest {
		t.Fatalf("action ceiling or explanation: %+v", got)
	}
}

func TestApprovalWindowAndDefaultSource(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVFLOW_HOME", dir)
	u, err := LoadUser()
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := LoadRepoCore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := ResolveAutonomy(u, r, "local", "commit", false)
	if d.Level != Confirm || !d.UserClassDefault || !d.RepoClassDefault || d.Window != 5*time.Minute || d.WindowSource != "default" {
		t.Fatalf("defaults: %+v", d)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("autonomy: {local: confirm, actions: {commit: auto}}\napproval_window: 20m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	u, err = LoadUser()
	if err != nil {
		t.Fatal(err)
	}
	d = ResolveAutonomy(u, r, "local", "commit", false)
	if d.UserClassDefault || !d.RepoClassDefault || d.Window != 20*time.Minute || d.WindowSource != "approval_window" || d.Level != Auto {
		t.Fatalf("user config: %+v", d)
	}
	r, _, _, err = parseRepoFile([]byte("policy: {max_autonomy: {local: confirm}, max_approval_window: 12m}"))
	if err != nil {
		t.Fatal(err)
	}
	d = ResolveAutonomy(u, r, "local", "commit", false)
	if d.RepoClassDefault || d.Level != Confirm || d.Window != 12*time.Minute || d.WindowSource != "policy.max_approval_window" {
		t.Fatalf("repository config: %+v", d)
	}
}

func TestInvalidScopedAutonomy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVFLOW_HOME", dir)
	for _, tc := range []struct{ user, repo, want string }{
		{"autonomy: {actions: {'*': auto}}", "policy: {max_autonomy: {actions: {'*': auto}}}", "unknown action"},
		{"autonomy: {actions: {commit: yes}}", "policy: {max_autonomy: {actions: {commit: 'yes'}}}", "suggest, confirm or auto"},
		{"approval_window: 0s", "policy: {max_approval_window: 61m}", "between 1m and 1h"},
		{"approval_window: forever", "policy: {max_approval_window: forever}", "duration"},
	} {
		if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(tc.user), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadUser(); err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
			t.Errorf("%s: got %v", tc.user, err)
		}
		if _, _, _, err := parseRepoFile([]byte(tc.repo)); err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
			t.Errorf("%s: got %v", tc.repo, err)
		}
	}
}
