package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseRemoteURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want RemoteURL
	}{
		{"https", "https://github.com/acme/widgets.git",
			RemoteURL{Transport: "https", Host: "github.com", Path: "acme/widgets", Owner: "acme", Name: "widgets"}},
		{"https no .git trailing slash", "https://github.com/acme/widgets/",
			RemoteURL{Transport: "https", Host: "github.com", Path: "acme/widgets", Owner: "acme", Name: "widgets"}},
		{"https upper-case host", "HTTPS://GitHub.COM/Acme/Widgets",
			RemoteURL{Transport: "https", Host: "github.com", Path: "Acme/Widgets", Owner: "Acme", Name: "Widgets"}},
		{"https credentials dropped", "https://x-access-token:s3cret@github.com/acme/widgets.git",
			RemoteURL{Transport: "https", Host: "github.com", Path: "acme/widgets", Owner: "acme", Name: "widgets"}},
		{"https port", "https://git.example.com:8443/team/app.git",
			RemoteURL{Transport: "https", Host: "git.example.com", Port: "8443", Path: "team/app", Owner: "team", Name: "app"}},
		{"gitlab nested groups", "https://gitlab.com/group/sub/deep/repo.git",
			RemoteURL{Transport: "https", Host: "gitlab.com", Path: "group/sub/deep/repo", Owner: "group/sub/deep", Name: "repo"}},
		{"scp", "git@github.com:acme/widgets.git",
			RemoteURL{Transport: "ssh", Host: "github.com", User: "git", Path: "acme/widgets", Owner: "acme", Name: "widgets"}},
		{"scp without user", "github.com:acme/widgets",
			RemoteURL{Transport: "ssh", Host: "github.com", Path: "acme/widgets", Owner: "acme", Name: "widgets"}},
		{"scp absolute path", "git@git.example.com:/srv/team/app.git",
			RemoteURL{Transport: "ssh", Host: "git.example.com", User: "git", Path: "srv/team/app", Owner: "srv/team", Name: "app"}},
		{"scp ipv6", "git@[::1]:team/app.git",
			RemoteURL{Transport: "ssh", Host: "::1", User: "git", Path: "team/app", Owner: "team", Name: "app"}},
		{"ssh url", "ssh://git@bitbucket.org/acme/widgets.git",
			RemoteURL{Transport: "ssh", Host: "bitbucket.org", User: "git", Path: "acme/widgets", Owner: "acme", Name: "widgets"}},
		{"ssh url port", "ssh://git@git.example.com:7999/proj/repo.git",
			RemoteURL{Transport: "ssh", Host: "git.example.com", Port: "7999", User: "git", Path: "proj/repo", Owner: "proj", Name: "repo"}},
		{"git+ssh scheme", "git+ssh://git@gitlab.com/g/r.git",
			RemoteURL{Transport: "ssh", Host: "gitlab.com", User: "git", Path: "g/r", Owner: "g", Name: "r"}},
		{"git protocol", "git://github.com/acme/widgets.git",
			RemoteURL{Transport: "git", Host: "github.com", Path: "acme/widgets", Owner: "acme", Name: "widgets"}},
		{"azure https", "https://dev.azure.com/org/proj/_git/repo",
			RemoteURL{Transport: "https", Host: "dev.azure.com", Path: "org/proj/repo", Owner: "org", Project: "proj", Name: "repo"}},
		{"azure https with user and encoded project", "https://org@dev.azure.com/org/My%20Proj/_git/repo",
			RemoteURL{Transport: "https", Host: "dev.azure.com", Path: "org/My Proj/repo", Owner: "org", Project: "My Proj", Name: "repo"}},
		{"azure repo-only", "https://dev.azure.com/org/_git/repo",
			RemoteURL{Transport: "https", Host: "dev.azure.com", Path: "org/repo/repo", Owner: "org", Project: "repo", Name: "repo"}},
		{"azure ssh", "git@ssh.dev.azure.com:v3/org/proj/repo",
			RemoteURL{Transport: "ssh", Host: "ssh.dev.azure.com", User: "git", Path: "org/proj/repo", Owner: "org", Project: "proj", Name: "repo"}},
		{"azure legacy visualstudio", "https://org.visualstudio.com/DefaultCollection/proj/_git/repo",
			RemoteURL{Transport: "https", Host: "org.visualstudio.com", Path: "org/proj/repo", Owner: "org", Project: "proj", Name: "repo"}},
		{"azure legacy ssh", "org@vs-ssh.visualstudio.com:v3/org/proj/repo",
			RemoteURL{Transport: "ssh", Host: "vs-ssh.visualstudio.com", User: "org", Path: "org/proj/repo", Owner: "org", Project: "proj", Name: "repo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRemoteURL(tt.in)
			if err != nil {
				t.Fatalf("ParseRemoteURL(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseRemoteURL(%q)\n got  %+v\n want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseRemoteURLLocal(t *testing.T) {
	for _, in := range []string{
		"/srv/git/app.git", "./app", "../app.git", "~/src/app", "app.git",
		"file:///srv/git/app.git", `C:\src\app`, "C:/src/app",
	} {
		_, err := ParseRemoteURL(in)
		if !errors.Is(err, ErrLocalRemote) {
			t.Errorf("ParseRemoteURL(%q) = %v, want ErrLocalRemote", in, err)
		}
	}
}

func TestParseRemoteURLMalformed(t *testing.T) {
	for _, in := range []string{
		"", "   ", "https://", "https:///acme/widgets", "https://github.com",
		"https://github.com/", "https://github.com/onlyowner", "https://github.com/a//b",
		"https://github.com/a/../b", "ftp://github.com/a/b", "git@github.com:",
		"git@github.com:onlyowner", "git@[::1:a/b", "git@:a/b", "https://exa mple.com/a/b",
		"https://dev.azure.com/org/proj/_git/", "https://github.com/a/b\nhttps://evil/x",
	} {
		_, err := ParseRemoteURL(in)
		if !errors.Is(err, ErrInvalidRemote) {
			t.Errorf("ParseRemoteURL(%q) = %v, want ErrInvalidRemote", in, err)
		}
	}
}

func TestParseRemoteURLErrorsDoNotLeakCredentials(t *testing.T) {
	_, err := ParseRemoteURL("https://user:topsecret@github.com/onlyowner?token=secretquery")
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "topsecret") || strings.Contains(err.Error(), "user:") || strings.Contains(err.Error(), "secretquery") {
		t.Errorf("error leaks credentials: %v", err)
	}
}

func TestInferForge(t *testing.T) {
	tests := map[string]Forge{
		"github.com": ForgeGitHub, "GitHub.com": ForgeGitHub, "ssh.github.com": ForgeGitHub,
		"acme.ghe.com": ForgeGitHub, "gitlab.com": ForgeGitLab, "bitbucket.org": ForgeBitbucket,
		"dev.azure.com": ForgeAzureDevOps, "ssh.dev.azure.com": ForgeAzureDevOps,
		"org.visualstudio.com": ForgeAzureDevOps, "vs-ssh.visualstudio.com": ForgeAzureDevOps,
		// Look-alikes and self-hosted installs are never guessed.
		"github.example.com": ForgeUnknown, "gitlab.example.com": ForgeUnknown,
		"notgithub.com": ForgeUnknown, "github.com.evil.test": ForgeUnknown, "": ForgeUnknown,
	}
	for host, want := range tests {
		if got := InferForge(host); got != want {
			t.Errorf("InferForge(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestParseForge(t *testing.T) {
	ok := map[string]Forge{
		"github": ForgeGitHub, "GitLab": ForgeGitLab, "bitbucket": ForgeBitbucket,
		"azuredevops": ForgeAzureDevOps, "azure-devops": ForgeAzureDevOps, "ADO": ForgeAzureDevOps,
	}
	for in, want := range ok {
		if got, err := ParseForge(in); err != nil || got != want {
			t.Errorf("ParseForge(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "gitea", "git hub"} {
		if _, err := ParseForge(in); err == nil {
			t.Errorf("ParseForge(%q): want error", in)
		}
	}
}

func rm(m map[string]string) map[string]string { return m }

func TestDetectForge(t *testing.T) {
	const (
		gh     = "https://github.com/acme/widgets.git"
		ghSSH  = "git@github.com:acme/widgets.git"
		ghFork = "git@github.com:me/widgets.git"
	)
	tests := []struct {
		name       string
		remotes    map[string]string
		opts       DetectOptions
		status     Status
		forge      Forge
		src, tgt   string // remote names
		fork       bool
		msgContain string
	}{
		{name: "origin only", remotes: rm(map[string]string{"origin": gh}),
			status: StatusOK, forge: ForgeGitHub, src: "origin", tgt: "origin"},
		{name: "fork with upstream", remotes: rm(map[string]string{"origin": ghFork, "upstream": gh}),
			status: StatusOK, forge: ForgeGitHub, src: "origin", tgt: "upstream", fork: true},
		{name: "upstream same repo over other transport is not a fork", remotes: rm(map[string]string{"origin": ghSSH, "upstream": gh}),
			status: StatusOK, forge: ForgeGitHub, src: "origin", tgt: "origin"},
		{name: "upstream same repo different case is not a fork", remotes: rm(map[string]string{"origin": "https://github.com/ACME/Widgets", "upstream": gh}),
			status: StatusOK, forge: ForgeGitHub, src: "origin", tgt: "origin"},
		{name: "ssh.github.com folds onto github.com", remotes: rm(map[string]string{"origin": "ssh://git@ssh.github.com:443/acme/widgets.git", "upstream": gh}),
			status: StatusOK, forge: ForgeGitHub, src: "origin", tgt: "origin"},
		{name: "explicit roles win", remotes: rm(map[string]string{"origin": gh, "mine": ghFork, "upstream": "https://github.com/other/widgets"}),
			opts:   DetectOptions{Source: "mine", Target: "origin"},
			status: StatusOK, forge: ForgeGitHub, src: "mine", tgt: "origin", fork: true},
		{name: "explicit target only", remotes: rm(map[string]string{"origin": ghFork, "canon": gh}),
			opts:   DetectOptions{Target: "canon"},
			status: StatusOK, forge: ForgeGitHub, src: "origin", tgt: "canon", fork: true},
		{name: "explicit source only, upstream still target", remotes: rm(map[string]string{"fork": ghFork, "upstream": gh}),
			opts:   DetectOptions{Source: "fork"},
			status: StatusOK, forge: ForgeGitHub, src: "fork", tgt: "upstream", fork: true},
		{name: "single non-origin remote is used", remotes: rm(map[string]string{"gh": gh}),
			status: StatusOK, forge: ForgeGitHub, src: "gh", tgt: "gh"},
		{name: "gitlab", remotes: rm(map[string]string{"origin": "git@gitlab.com:g/s/r.git"}),
			status: StatusOK, forge: ForgeGitLab, src: "origin", tgt: "origin"},
		{name: "bitbucket", remotes: rm(map[string]string{"origin": "https://bitbucket.org/acme/widgets"}),
			status: StatusOK, forge: ForgeBitbucket, src: "origin", tgt: "origin"},
		{name: "azure", remotes: rm(map[string]string{"origin": "git@ssh.dev.azure.com:v3/org/proj/repo", "upstream": "https://org@dev.azure.com/org/proj/_git/repo"}),
			status: StatusOK, forge: ForgeAzureDevOps, src: "origin", tgt: "origin"},
		{name: "self-hosted mapped", remotes: rm(map[string]string{"origin": "git@git.corp.example:team/app.git"}),
			opts:   DetectOptions{Hosts: map[string]string{"Git.Corp.Example": "gitlab"}},
			status: StatusOK, forge: ForgeGitLab, src: "origin", tgt: "origin"},
		{name: "self-hosted mapped with port in url", remotes: rm(map[string]string{"origin": "https://git.corp.example:8443/team/app.git"}),
			opts:   DetectOptions{Hosts: map[string]string{"git.corp.example": "github"}},
			status: StatusOK, forge: ForgeGitHub, src: "origin", tgt: "origin"},
		{name: "mapping overrides inference", remotes: rm(map[string]string{"origin": gh}),
			opts:   DetectOptions{Hosts: map[string]string{"github.com": "gitlab"}},
			status: StatusOK, forge: ForgeGitLab, src: "origin", tgt: "origin"},
		{name: "self-hosted unmapped", remotes: rm(map[string]string{"origin": "https://git.corp.example/team/app.git"}),
			status: StatusUnknown, src: "origin", tgt: "origin", msgContain: `"git.corp.example"`},
		{name: "no remotes", remotes: rm(map[string]string{}),
			status: StatusLocalOnly, msgContain: "git remote add"},
		{name: "only local remote", remotes: rm(map[string]string{"origin": "/srv/git/app.git"}),
			status: StatusLocalOnly, msgContain: "local path"},
		{name: "only malformed remote", remotes: rm(map[string]string{"origin": "https://github.com/onlyowner"}),
			status: StatusUnknown, msgContain: "fix its URL"},
		{name: "no origin, several remotes", remotes: rm(map[string]string{"a": gh, "b": ghFork}),
			status: StatusAmbiguous, msgContain: "a, b"},
		{name: "origin malformed but another remote parses", remotes: rm(map[string]string{"origin": "nonsense", "upstream": gh}),
			status: StatusUnknown, msgContain: "origin cannot be used"},
		{name: "upstream malformed", remotes: rm(map[string]string{"origin": ghFork, "upstream": "https://github.com/x"}),
			status: StatusAmbiguous, src: "origin", msgContain: "upstream cannot be used"},
		{name: "upstream malformed but target explicit", remotes: rm(map[string]string{"origin": ghFork, "upstream": "https://github.com/x"}),
			opts:   DetectOptions{Target: "origin"},
			status: StatusOK, forge: ForgeGitHub, src: "origin", tgt: "origin"},
		{name: "missing explicit source", remotes: rm(map[string]string{"origin": gh}),
			opts:   DetectOptions{Source: "fork"},
			status: StatusUnknown, msgContain: `"fork" does not exist`},
		{name: "explicit target is local", remotes: rm(map[string]string{"origin": gh, "backup": "/mnt/backup.git"}),
			opts:   DetectOptions{Target: "backup"},
			status: StatusUnknown, msgContain: `target remote "backup" cannot be used`},
		{name: "cross-forge fork", remotes: rm(map[string]string{"origin": "git@gitlab.com:me/w.git", "upstream": gh}),
			status: StatusAmbiguous, src: "origin", tgt: "upstream", fork: true, msgContain: "cannot cross code hosts"},
		{name: "same forge across hosts", remotes: rm(map[string]string{"origin": "git@acme.ghe.com:me/w.git", "upstream": gh}),
			status: StatusAmbiguous, src: "origin", tgt: "upstream", fork: true, msgContain: "cannot cross code hosts"},
		{name: "fork onto unmapped host", remotes: rm(map[string]string{"origin": ghFork, "upstream": "https://git.corp.example/a/b"}),
			status: StatusUnknown, src: "origin", tgt: "upstream", fork: true, msgContain: "git.corp.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DetectForge(tt.remotes, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tt.status {
				t.Fatalf("status = %q (%s), want %q", got.Status, got.Message, tt.status)
			}
			if got.Forge != tt.forge {
				t.Errorf("forge = %q, want %q", got.Forge, tt.forge)
			}
			if name := remoteName(got.Source); name != tt.src {
				t.Errorf("source = %q, want %q", name, tt.src)
			}
			if name := remoteName(got.Target); name != tt.tgt {
				t.Errorf("target = %q, want %q", name, tt.tgt)
			}
			if tt.src != "" && tt.tgt != "" && got.Fork != tt.fork {
				t.Errorf("fork = %v, want %v", got.Fork, tt.fork)
			}
			if got.OK() != (tt.status == StatusOK) {
				t.Errorf("OK() = %v for status %q", got.OK(), got.Status)
			}
			if tt.status == StatusOK && got.Message != "" {
				t.Errorf("OK detection has message %q", got.Message)
			}
			if tt.status != StatusOK && got.Message == "" {
				t.Error("non-OK detection must explain itself in Message")
			}
			if !strings.Contains(got.Message, tt.msgContain) {
				t.Errorf("message %q does not contain %q", got.Message, tt.msgContain)
			}
		})
	}
}

func remoteName(r *Remote) string {
	if r == nil {
		return ""
	}
	return r.Name
}

func TestDetectForgeIsDeterministic(t *testing.T) {
	remotes := map[string]string{
		"z": "https://github.com/a/z", "m": "https://github.com/a/m", "b": "bad",
		"c": "/local", "a": "https://github.com/a/a", "y": "also bad",
	}
	first, err := DetectForge(remotes, DetectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusAmbiguous {
		t.Fatalf("status = %q", first.Status)
	}
	for i := 0; i < 50; i++ {
		got, _ := DetectForge(remotes, DetectOptions{})
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, got, first)
		}
	}
	var names []string
	for _, r := range first.Remotes {
		names = append(names, r.Name)
	}
	if !reflect.DeepEqual(names, []string{"a", "m", "z"}) {
		t.Errorf("Remotes not sorted by name: %v", names)
	}
	if len(first.Skipped) != 3 || first.Skipped[0].Name != "b" || !first.Skipped[1].Local {
		t.Errorf("Skipped = %+v", first.Skipped)
	}
}

func TestDetectForgeInvalidHostMapping(t *testing.T) {
	for name, hosts := range map[string]map[string]string{
		"unknown driver":  {"git.example.com": "gitea"},
		"scheme in key":   {"https://git.example.com": "gitlab"},
		"port in key":     {"git.example.com:8443": "gitlab"},
		"path in key":     {"git.example.com/x": "gitlab"},
		"empty key":       {"": "gitlab"},
		"case conflict":   {"Git.Example.com": "gitlab", "git.example.com": "github"},
		"whitespace only": {"  ": "gitlab"},
	} {
		if _, err := DetectForge(map[string]string{"origin": "https://github.com/a/b"}, DetectOptions{Hosts: hosts}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

// --- Real git, local temp repos ---------------------------------------------

// isolateGit keeps the developer's global and system git config out of tests.
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func tempRepo(t *testing.T) *Repo {
	t.Helper()
	isolateGit(t)
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	repo, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func gitIn(t *testing.T, r *Repo, args ...string) {
	t.Helper()
	if _, err := r.Git(context.Background(), args...); err != nil {
		t.Fatal(err)
	}
}

func TestRepoDetectForge(t *testing.T) {
	ctx := context.Background()

	t.Run("no remotes is local-only", func(t *testing.T) {
		r := tempRepo(t)
		d, err := r.DetectForge(ctx, DetectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != StatusLocalOnly || !strings.Contains(d.Message, "git remote add") {
			t.Errorf("got %q %q", d.Status, d.Message)
		}
		if got := r.RemoteRepo(ctx); got != "" {
			t.Errorf("RemoteRepo() = %q without origin", got)
		}
	})

	t.Run("missing origin with upstream only", func(t *testing.T) {
		r := tempRepo(t)
		gitIn(t, r, "remote", "add", "upstream", "https://github.com/acme/widgets.git")
		d, err := r.DetectForge(ctx, DetectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !d.OK() || d.Source.Name != "upstream" || d.Fork {
			t.Errorf("got %+v", d)
		}
	})

	t.Run("fork", func(t *testing.T) {
		r := tempRepo(t)
		gitIn(t, r, "remote", "add", "origin", "git@github.com:me/widgets.git")
		gitIn(t, r, "remote", "add", "upstream", "https://github.com/acme/widgets.git")
		d, err := r.DetectForge(ctx, DetectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !d.OK() || !d.Fork || d.Source.URL.Path != "me/widgets" || d.Target.URL.Path != "acme/widgets" {
			t.Errorf("got %+v", d)
		}
		// Existing behaviour is untouched: RemoteRepo still reports origin.
		if got := r.RemoteRepo(ctx); got != "me/widgets" {
			t.Errorf("RemoteRepo() = %q", got)
		}
	})

	t.Run("insteadOf rewrites are honoured, local config only", func(t *testing.T) {
		r := tempRepo(t)
		gitIn(t, r, "config", "url.https://git.corp.example/.insteadOf", "corp:")
		gitIn(t, r, "remote", "add", "origin", "corp:team/app.git")
		d, err := r.DetectForge(ctx, DetectOptions{Hosts: map[string]string{"git.corp.example": "gitlab"}})
		if err != nil {
			t.Fatal(err)
		}
		if !d.OK() || d.Forge != ForgeGitLab || d.Target.URL.Path != "team/app" {
			t.Errorf("got %+v", d)
		}
	})

	t.Run("local path remote", func(t *testing.T) {
		r := tempRepo(t)
		bare := filepath.Join(t.TempDir(), "bare.git")
		if out, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		gitIn(t, r, "remote", "add", "origin", bare)
		d, err := r.DetectForge(ctx, DetectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != StatusLocalOnly {
			t.Errorf("got %q %q", d.Status, d.Message)
		}
	})

	t.Run("malformed origin", func(t *testing.T) {
		r := tempRepo(t)
		gitIn(t, r, "remote", "add", "origin", "https://github.com/onlyowner")
		d, err := r.DetectForge(ctx, DetectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != StatusUnknown {
			t.Errorf("got %q %q", d.Status, d.Message)
		}
	})
}

func TestRemotesOutsideRepoFails(t *testing.T) {
	r := &Repo{Dir: t.TempDir()}
	isolateGit(t)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(r.Dir))
	if _, err := r.Remotes(context.Background()); err == nil {
		t.Error("want error outside a repository")
	}
}

// --- Existing API is preserved ----------------------------------------------

func TestParseGitHubRemoteUnchanged(t *testing.T) {
	tests := map[string]string{
		"https://github.com/acme/widgets.git": "acme/widgets",
		"https://github.com/acme/widgets":     "acme/widgets",
		"git@github.com:acme/widgets.git":     "acme/widgets",
		"ssh://git@github.com/acme/widgets/":  "acme/widgets",
		"https://gitlab.com/acme/widgets.git": "",
		"":                                    "",
		"/srv/git/widgets.git":                "",
	}
	for in, want := range tests {
		if got := ParseGitHubRemote(in); got != want {
			t.Errorf("ParseGitHubRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestKeyFromBranchLegacyFixture pins what branch-name key extraction does
// today, because issue #16 assumed it rejects a pull-request-number suffix.
// It does not: KeyFromBranch upper-cases the branch and takes the first
// LETTERS-DIGITS token, so "pr-16" yields the key "PR-16". This test records
// that behaviour (callers depend on it) so a future change to it is deliberate;
// it is not an endorsement. Tightening it belongs with the commit package.
func TestKeyFromBranchLegacyFixture(t *testing.T) {
	tests := []struct {
		branch string
		want   string
	}{
		{"feature/PLAT-123-add-forge", "PLAT-123"},
		{"plat-123-lowercase", "PLAT-123"},
		{"codex/providers-forge", ""},
		{"pull/16/head", ""},                   // numeric-only segments never match
		{"feature/PLAT-123-pr-16", "PLAT-123"}, // first match wins
		// The surprising cases: a PR/issue-number suffix is accepted as a key.
		{"pr-16", "PR-16"},
		{"fix/forge-pr-16", "PR-16"},
		{"gh-16", "GH-16"},
		{"feat/issue-16-forge", "ISSUE-16"},
	}
	for _, tt := range tests {
		if got := KeyFromBranch(tt.branch); got != tt.want {
			t.Errorf("KeyFromBranch(%q) = %q, want %q", tt.branch, got, tt.want)
		}
	}
}
