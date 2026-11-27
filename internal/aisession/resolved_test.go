package aisession

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
)

// The chain classifies the path the operation will reach, not only the
// path the agent spelled: a link inside the worktree can name a secret or
// a control file, and macOS and Windows fold characters Go does not.

func link(t *testing.T, target, at string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, at); err != nil {
		t.Fatal(err)
	}
}

func TestChainClassifiesTheResolvedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	c := newChainRig(t)
	// notes.txt -> .env: a read is a secret read.
	link(t, ".env", filepath.Join(c.wt, "notes.txt"))
	// docs -> .github/workflows: docs/ci.yml is a workflow.
	if err := os.MkdirAll(filepath.Join(c.wt, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(c.wt, ".github", "workflows", "ci.yml"), "on: push\n")
	link(t, filepath.Join(".github", "workflows"), filepath.Join(c.wt, "docs"))
	// hooks -> nested/.git: a link into a nested repository's git folder.
	writeTestFile(t, filepath.Join(c.wt, "nested", ".git", "config"), "[core]\n")
	link(t, filepath.Join("nested", ".git"), filepath.Join(c.wt, "hooks"))

	ctx := context.Background()
	if v := Run(ctx, c.chain(t, nil, nil, ""), c.read("notes.txt")); v.Decision != Ask || v.Code != ReasonSecretFile {
		t.Errorf("read notes.txt (-> .env) = %+v, want ask secret_file", v)
	}
	rules := map[string]config.AIRule{"docs": {Kind: config.AIRuleKindFSWrite, Match: "docs/**", Decision: config.AIDecisionAllow}}
	auto := config.Autonomy{Local: config.Auto}
	for name, user := range map[string]*config.User{"rule": userWith(rules, config.Autonomy{}), "local auto": userWith(nil, auto)} {
		if v := Run(ctx, c.chain(t, user, nil, ""), c.write("docs/ci.yml")); v.Decision != Ask || v.Code != ReasonControlFile {
			t.Errorf("%s: write docs/ci.yml (-> .github/workflows) = %+v, want ask control_file", name, v)
		}
		// A new file below the link is classified by its nearest parent.
		if v := Run(ctx, c.chain(t, user, nil, ""), c.write("docs/new.yml")); v.Decision != Ask || v.Code != ReasonControlFile {
			t.Errorf("%s: write docs/new.yml = %+v, want ask control_file", name, v)
		}
	}
	// A repository deny list applies to the resolved path too.
	repo := &config.Repo{}
	repo.AI.Permissions.Deny = []string{".github/**"}
	if v := Run(ctx, c.chain(t, userWith(rules, auto), repo, ""), c.write("docs/ci.yml")); v.Decision != Deny {
		t.Errorf("repo deny .github/**: write docs/ci.yml = %+v, want deny", v)
	}
	if v := Run(ctx, c.chain(t, nil, nil, ""), c.read("hooks/config")); v.Decision != Deny || v.Code != VerdictGitDir {
		t.Errorf("read hooks/config (-> nested/.git) = %+v, want deny git_dir", v)
	}
	// A permission request's locations are classified the same way.
	r := c.write("docs/ci.yml")
	r.Kind, r.ToolKind = KindPermission, "edit"
	if v := Run(ctx, c.chain(t, userWith(nil, auto), nil, ""), r); v.Decision != Ask || v.Code != ReasonControlFile {
		t.Errorf("edit docs/ci.yml = %+v, want ask control_file", v)
	}
}

func TestChainNonASCIINamesArePersonOnly(t *testing.T) {
	SetFoldForTest(t, true)
	c := newChainRig(t)
	ctx := context.Background()
	auto := config.Autonomy{Local: config.Auto}
	allowAll := map[string]config.AIRule{
		"r": {Kind: config.AIRuleKindFSRead, Match: "**", Decision: config.AIDecisionAllow},
		"w": {Kind: config.AIRuleKindFSWrite, Match: "**", Decision: config.AIDecisionAllow},
	}
	for _, rel := range []string{".github/workflowſ/ci.yml", "id_rſa", ".agentﬂow/config.yml", "src/Key.txt"} {
		for name, user := range map[string]*config.User{"default": nil, "local auto": userWith(nil, auto), "rules": userWith(allowAll, auto)} {
			if v := Run(ctx, c.chain(t, user, nil, ""), c.read(rel)); v.Decision != Ask || v.Code != ReasonUnicodeName {
				t.Errorf("%s: read %s = %+v, want ask unicode_name", name, rel, v)
			}
			if v := Run(ctx, c.chain(t, user, nil, ""), c.write(rel)); v.Decision != Ask {
				t.Errorf("%s: write %s = %+v, want ask", name, rel, v)
			}
		}
	}
	// ASCII names keep their old answers.
	if v := Run(ctx, c.chain(t, nil, nil, ""), c.read("src/a.go")); v.Decision != Allow {
		t.Errorf("read src/a.go = %+v, want allow", v)
	}
}

func TestReadRefusesAFileOtherThanTheCheckedOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	w, _ := fsRig(t)
	f, err := openFiles(w)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	link(t, "README.md", filepath.Join(w.real, "swap"))
	checked := filepath.Join(w.real, "sub", "x.txt")
	// The chain checked swap -> sub/x.txt; the link now names README.md.
	if _, _, err := f.readChecked("swap", checked, nil, nil); fsCode(err) != VerdictChanged {
		t.Errorf("read of a swapped link = %v, want changed", err)
	}
	if s, _, err := f.readChecked("swap", filepath.Join(w.real, "README.md"), nil, nil); err != nil || s != "hello\n" {
		t.Errorf("read of the checked file = %q, %v", s, err)
	}
	if _, _, err := f.readChecked("swap", "", nil, nil); !errors.As(err, new(*fsError)) || fsCode(err) != VerdictChanged {
		t.Errorf("read of a file that did not exist when checked = %v, want changed", err)
	}
}

func TestRealFolderFoldingIsPersonOnly(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("APFS folding")
	}
	c := newChainRig(t)
	// On APFS .claude/ſettings.json opens .claude/settings.json.
	p := filepath.Join(c.wt, ".claude", "ſettings.json")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("this file system does not fold ſ: %v", err)
	}
	if v := Run(context.Background(), c.chain(t, userWith(nil, config.Autonomy{Local: config.Auto}), nil, ""), c.write(".claude/ſettings.json")); v.Decision != Ask {
		t.Errorf("write .claude/ſettings.json = %+v, want ask", v)
	}
}

func writeTestFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
