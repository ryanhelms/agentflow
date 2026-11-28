package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/provider"
)

// Whatever form git writes a file header in, the draft filter gets
// "diff --git a/OLD b/NEW", and a file it could not identify from that line
// keeps no content.
func TestDeliveryDraftDiffHeaders(t *testing.T) {
	const hunk = "@@ -0,0 +1 @@\n+VALUE=planted\n"
	for name, c := range map[string]struct {
		header, extra string
		changed       []string
		want          string
		withheld      []string
		content       bool
	}{
		"standard":           {"a/x.go b/x.go", "", []string{"x.go"}, "diff --git a/x.go b/x.go\n", nil, true},
		"no prefix":          {".env .env", "", []string{".env"}, "diff --git a/.env b/.env\n", nil, true},
		"mnemonic":           {"c/.env i/.env", "", []string{".env"}, "diff --git a/.env b/.env\n", nil, true},
		"quoted":             {`"a/caf\303\251/.env" "b/caf\303\251/.env"`, "", []string{"café/.env"}, "diff --git a/café/.env b/café/.env\n", nil, true},
		"spaces":             {"a/my notes.txt b/my notes.txt", "", []string{"my notes.txt"}, "diff --git a/my notes.txt b/my notes.txt\n", nil, true},
		"renamed secret":     {"a/.env b/settings.txt", "rename from .env\nrename to settings.txt\n", []string{".env", "settings.txt"}, "diff --git a/.env b/settings.txt\n[content left out", []string{".env"}, false},
		"unknown prefixes":   {"x/.env y/.env", "", []string{".env"}, "its name could not be read", []string{"(a file whose name could not be read)"}, false},
		"name looks like b/": {"a/x b/.env b/x b/.env", "", []string{"x b/.env"}, "cannot be written in a header", []string{"x b/.env"}, false},
		"control character":  {`"a/x\ny" "b/x\ny"`, "", []string{"x\ny"}, "cannot be written in a header", []string{`"x\ny"`}, false},
	} {
		t.Run(name, func(t *testing.T) {
			d := draftDiff("diff --git "+c.header+"\n"+c.extra+hunk, c.changed)
			if !strings.Contains(d.Diff, c.want) || !slices.Equal(d.Withheld, c.withheld) || strings.Contains(d.Diff, "planted") != c.content {
				t.Fatalf("diff %q, withheld %q", d.Diff, d.Withheld)
			}
			if !strings.HasSuffix(d.Stat, "1 files changed, 1 insertions(+), 0 deletions(-)\n") {
				t.Errorf("stat %q", d.Stat)
			}
		})
	}
}

// The "---" and "+++" lines name the file too, so they get the same two
// prefixes, whatever git's settings wrote. Lines of the change itself that
// look like them stay as they are.
func TestDeliveryDraftDiffFileLines(t *testing.T) {
	for name, c := range map[string]struct {
		diff, want string
		changed    []string
	}{
		"no prefix": {"diff --git x.go x.go\nindex 1..2 100644\n--- x.go\n+++ x.go\n@@ -1 +1 @@\n--- old\n+++ new\n",
			"diff --git a/x.go b/x.go\nindex 1..2 100644\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n--- old\n+++ new\n", []string{"x.go"}},
		"added": {"diff --git c/x.go i/x.go\nnew file mode 100644\n--- /dev/null\n+++ i/x.go\n@@ -0,0 +1 @@\n+x\n",
			"diff --git a/x.go b/x.go\nnew file mode 100644\n--- /dev/null\n+++ b/x.go\n@@ -0,0 +1 @@\n+x\n", []string{"x.go"}},
		"deleted": {"diff --git x.go x.go\ndeleted file mode 100644\n--- x.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n",
			"diff --git a/x.go b/x.go\ndeleted file mode 100644\n--- a/x.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n", []string{"x.go"}},
	} {
		if d := draftDiff(c.diff, c.changed); d.Diff != c.want {
			t.Errorf("%s: %q", name, d.Diff)
		}
	}
}

// A change that deletes a secret file may have moved it somewhere git did
// not report, so the files it adds keep no content. Others are sent.
func TestDeliveryDraftDiffAddedBesideADeletedSecret(t *testing.T) {
	d := draftDiff("diff --git a/.env b/.env\ndeleted file mode 100644\n--- a/.env\n+++ /dev/null\n@@ -1 +0,0 @@\n-V=planted\n"+
		"diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+V=moved\n"+
		"diff --git a/old.txt b/old.txt\n--- a/old.txt\n+++ b/old.txt\n@@ -1 +1 @@\n-before\n+after\n",
		[]string{".env", "new.txt", "old.txt"})
	if strings.Contains(d.Diff, "V=moved") || !strings.Contains(d.Diff, "+after") || !slices.Equal(d.Withheld, []string{"new.txt"}) {
		t.Fatalf("diff %q, withheld %q", d.Diff, d.Withheld)
	}
	if d.Files != "D .env\nA new.txt\nM old.txt\n" {
		t.Errorf("files %q", d.Files)
	}
}

func TestDeliveryWithoutAddresses(t *testing.T) {
	got := withoutAddresses("fix: a thing\n\nAsked for by <ann@example.com>.\n\nSigned-off-by: Ann <ann@example.com>\nReviewed-by: Bo\nRefs: PLAT-1")
	if got != "fix: a thing\n\nAsked for by <address>.\n\nRefs: PLAT-1" {
		t.Fatalf("%q", got)
	}
}

// deliveryGit stands for ai.GitDiff, which this package cannot import.
func deliveryGit(a *App) DraftGit {
	return func(ctx context.Context, args ...string) (string, error) {
		return a.Repo.Git(ctx, append([]string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/"}, args...)...)
	}
}

func deliveryRepo(t *testing.T) *App {
	t.Helper()
	a, err := Open(t.Context(), newRepo(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func deliveryWrite(t *testing.T, a *App, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(a.Repo.Dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func deliveryCode(t *testing.T, err error, code string) {
	t.Helper()
	var r *aisession.Refusal
	if !errors.As(err, &r) || r.Code != code {
		t.Fatalf("got %v, want refusal %s", err, code)
	}
}

// The base is the local branch when there is no remote-tracking ref, and a
// base that cannot be resolved is a refusal rather than a fetch.
func TestDeliveryDraftBase(t *testing.T) {
	a := deliveryRepo(t)
	mustGit(t, a.Repo, "checkout", "-qb", "feature", "main")
	_, err := a.PRDraftContext(t.Context(), deliveryGit(a))
	deliveryCode(t, err, "nothing_to_draft")

	deliveryWrite(t, a, "greeting.txt", "hello\n")
	mustGit(t, a.Repo, "add", "greeting.txt")
	mustGit(t, a.Repo, "commit", "-qm", "feat: greet", "-m", "Signed-off-by: Dev <dev@example.com>")
	d, err := a.PRDraftContext(t.Context(), deliveryGit(a))
	if err != nil {
		t.Fatal(err)
	}
	if d.Commits != "feat: greet" || !strings.Contains(d.Diff, "+hello") || d.Files != "A greeting.txt\n" {
		t.Fatalf("commits %q, files %q, diff %q", d.Commits, d.Files, d.Diff)
	}

	mustGit(t, a.Repo, "branch", "-m", "main", "trunk")
	_, err = a.PRDraftContext(t.Context(), deliveryGit(a))
	deliveryCode(t, err, "no_base")
}

// A commit draft reads the staged change, or the named paths, and leaves the
// index as it was.
func TestDeliveryCommitDraftKeepsTheIndex(t *testing.T) {
	a := deliveryRepo(t)
	deliveryWrite(t, a, "staged.txt", "staged\n")
	deliveryWrite(t, a, "other.txt", "not staged\n")
	_, err := a.CommitDraftContext(t.Context(), nil)
	deliveryCode(t, err, "nothing_to_draft")

	mustGit(t, a.Repo, "add", "staged.txt")
	status := func() string {
		out, err := a.Repo.Git(t.Context(), "--no-optional-locks", "status", "--porcelain")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := status()
	for _, c := range []struct {
		paths      []string
		with, left string
	}{{nil, "+staged", "+not staged"}, {[]string{"other.txt"}, "+not staged", "+staged"}} {
		d, err := a.CommitDraftContext(t.Context(), c.paths)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(d.Diff, c.with+"\n") || strings.Contains(d.Diff, c.left+"\n") {
			t.Errorf("paths %q: %q", c.paths, d.Diff)
		}
		if after := status(); after != before {
			t.Errorf("paths %q changed the index: %q, was %q", c.paths, after, before)
		}
	}
}

// Reading the criteria for a draft saves no checklist.
func TestDeliveryACDraftWritesNoChecklist(t *testing.T) {
	ctx := context.Background()
	a := fileTaskApp(t, "  provider: filesystem\n  settings: {scope: 'eng/platform'}\n")
	a.Gate.Autonomy.Local, a.Gate.Autonomy.Remote = config.Confirm, config.Auto
	create := func(token string) (any, error) {
		return a.CreateTicket(ctx, "", "", "Greet", "Acceptance criteria\n\n- Says hello", "", token)
	}
	v, err := create("")
	if err != nil {
		t.Fatal(err)
	}
	v, err = create(confirmation(t, v))
	if err != nil {
		t.Fatal(err)
	}
	key := v.(map[string]string)["key"]
	mustGit(t, a.Repo, "checkout", "-qb", "feature")
	deliveryWrite(t, a, "greeting.txt", "hello\n")
	mustGit(t, a.Repo, "add", "greeting.txt")

	d, err := a.ACDraftContext(ctx, deliveryGit(a), key)
	if err != nil {
		t.Fatal(err)
	}
	if d.Key != key || len(d.Criteria) != 1 || d.Criteria[0].Text != "Says hello" || d.Source != "filesystem" || !strings.Contains(d.Staged.Diff, "+hello") {
		t.Fatalf("%+v", d)
	}
	ref, err := provider.ParseRef(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.Checklists(ctx).Dir, ref.StorageKey()+".json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the draft saved a checklist: %v", err)
	}
}
