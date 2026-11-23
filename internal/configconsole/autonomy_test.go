package configconsole

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/consolecfg"
)

func TestConcurrentPersonalAutonomySave(t *testing.T) {
	dir := t.TempDir()
	done := make(chan error, 2)
	for _, lvl := range []config.Level{config.Auto, config.Suggest} {
		go func() {
			err := replacePersonal(dir, nil, []byte("autonomy: {local: "+string(lvl)+"}\n"))
			done <- err
		}()
	}
	a, b := <-done, <-done
	if a == nil {
		a, b = b, a
	}
	if !errors.Is(a, ErrUserRevision) || b != nil {
		t.Fatalf("concurrent results: %v, %v", a, b)
	}
}

func autonomyFixture(t *testing.T, repo, user string) (*app.App, string) {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("DEVFLOW_HOME", home)
	t.Setenv("DEVFLOW_NO_UPDATE", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if out, err := exec.Command("git", "init", "-qb", "main", root).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if err := os.Mkdir(filepath.Join(root, ".devflow"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, consolecfg.MainFile), []byte(repo), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, "config.yml")
	if err := os.WriteFile(file, []byte(user), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(t.Context(), root, "test-owner")
	if err != nil {
		t.Fatal(err)
	}
	return a, file
}
func TestPersonalPatchApprovalAndExistingSettings(t *testing.T) {
	const private = "private-setting-value"
	const repo = "devflow: 1\ncommit: {require_key: false}\n"
	const initial = "custom_field: " + private + "\napproval_window: 10m\nautonomy:\n  actions: {push: auto, commit: suggest}\n"
	a, file := autonomyFixture(t, repo, initial)
	ctx := t.Context()
	view, err := InspectAutonomy(ctx, a.Repo.Dir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	rev := view.UserRevision
	patch := ApprovalPatch{Actions: map[string]config.Level{"commit": config.Auto}}
	result, err := PersonalAutonomy(ctx, a, rev, patch, "")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := result.(*app.Pending)
	if !ok || p.ConfirmToken == "" || p.Status != "needs_confirmation" {
		t.Fatalf("missing approval: %T", result)
	}
	summary, err := json.Marshal(p.Summary)
	if err != nil || bytes.Contains(summary, []byte(private)) {
		t.Fatal("leaked unrelated preference", err)
	}
	raw, _ := os.ReadFile(file)
	if string(raw) != initial {
		t.Fatal("preview wrote preferences")
	}
	if _, err = PersonalAutonomy(ctx, a, rev, ApprovalPatch{Actions: map[string]config.Level{"commit": config.Confirm}}, p.ConfirmToken); err == nil {
		t.Fatal("changed action accepted")
	}
	result, err = PersonalAutonomy(ctx, a, rev, patch, p.ConfirmToken)
	if err != nil {
		t.Fatal(err)
	}
	out := result.(personalSave)
	if !strings.Contains(out.Note, "Restart running Devflow MCP sessions") {
		t.Fatal("missing restart note")
	}
	if out.Personal.Actions["push"] != config.Auto || out.Personal.Actions["commit"] != config.Auto || out.Personal.Window != "10m0s" {
		t.Fatalf("wrong settings %+v", out.Personal)
	}
	raw, _ = os.ReadFile(file)
	if !bytes.Contains(raw, []byte(private)) {
		t.Fatal("discarded unrelated setting")
	}
	if _, err = PersonalAutonomy(ctx, a, rev, patch, p.ConfirmToken); !errors.Is(err, ErrUserRevision) {
		t.Fatalf("stale revision: %v", err)
	}
	// Inherit removes just the chosen override.
	clear := ApprovalPatch{Actions: map[string]config.Level{"commit": ""}}
	result, err = PersonalAutonomy(ctx, a, out.UserRevision, clear, "")
	if err != nil {
		t.Fatal(err)
	}
	p = result.(*app.Pending)
	result, err = PersonalAutonomy(ctx, a, out.UserRevision, clear, p.ConfirmToken)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(personalSave).Personal; len(got.Actions) != 1 || got.Actions["push"] != config.Auto {
		t.Fatal(got)
	}
}
func TestPersonalApprovalUsesCurrentPolicyAndPrivateErrors(t *testing.T) {
	a, file := autonomyFixture(t, "devflow: 1\n", "autonomy: {local: auto}\n")
	ctx := t.Context()
	view, err := InspectAutonomy(ctx, a.Repo.Dir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	patch := ApprovalPatch{Actions: map[string]config.Level{"console.autonomy": config.Auto}}
	result, err := PersonalAutonomy(ctx, a, view.UserRevision, patch, "")
	if err != nil {
		t.Fatal(err)
	}
	p := result.(*app.Pending)
	if p.Status != "needs_confirmation" {
		t.Fatal("prospective settings authorized themselves")
	}
	if err := os.WriteFile(filepath.Join(a.Repo.Dir, consolecfg.MainFile), []byte("policy: {max_autonomy: {local: suggest}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = PersonalAutonomy(ctx, a, view.UserRevision, patch, p.ConfirmToken)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(*app.Pending).Status; got != "suggest" {
		t.Fatal("stale policy", got)
	}
	before, _ := os.ReadFile(file)
	if bytes.Contains(before, []byte("console.autonomy")) {
		t.Fatal("wrote despite current ceiling")
	}
	if err := os.WriteFile(file, []byte("approval_window: private-secret-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, op := range []func() error{
		func() error { _, err := InspectAutonomy(ctx, a.Repo.Dir, "", nil); return err },
		func() error {
			_, err := PersonalAutonomy(ctx, a, hash([]byte("approval_window: private-secret-value\n")), patch, "")
			return err
		},
	} {
		err := op()
		if err == nil || strings.Contains(err.Error(), "private-secret-value") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
}
func TestStageRepositoryPatchPreservesStagedDraft(t *testing.T) {
	const original = "devflow: 1\npolicy: {allow_auto_pr_publication: true, max_autonomy: {actions: {push: confirm, commit: suggest}}}\n"
	a, _ := autonomyFixture(t, original, "")
	draft := original + "# staged edit\n"
	base := hash([]byte(original))
	patch := ApprovalPatch{Actions: map[string]config.Level{"commit": "", "pr_open": config.Confirm}}
	staged, err := StageAutonomy(t.Context(), a.Repo.Dir, base, &draft, patch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged.Content, "staged edit") || staged.Autonomy.Repository.Actions["push"] != config.Confirm || len(staged.Autonomy.Repository.Actions) != 2 {
		t.Fatalf("lost staged values: %s", staged.Content)
	}
	if !strings.Contains(staged.Content, "allow_auto_pr_publication: true") {
		t.Fatal("an unrelated action edit removed the publication policy")
	}
	data, _ := os.ReadFile(filepath.Join(a.Repo.Dir, consolecfg.MainFile))
	if string(data) != original {
		t.Fatal("composition wrote repository")
	}
	if _, err = StageAutonomy(t.Context(), a.Repo.Dir, "stale", &draft, patch); err == nil {
		t.Fatal("accepted stale source")
	}
}
