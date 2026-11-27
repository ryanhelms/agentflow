package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserDir(t *testing.T) {
	f := newHomeFixture(t)
	own := filepath.Join(f.account, ".cache", "agentflow")
	ownFn := func() (string, error) { return own, nil }
	repo := newRepo(t)
	outside := realDir(t, t.TempDir())
	bare := realDir(t, t.TempDir())
	gitRun(t, bare, "init", "-q", "--bare")
	link := filepath.Join(outside, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(outside, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(outside, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	// A link the repository owns, pointing outside: the real path is used, never the link.
	if err := os.Symlink(real, filepath.Join(repo, "to-outside")); err != nil {
		t.Fatal(err)
	}
	// A dangling link is not a missing folder: its target could appear inside a repository.
	if err := os.Symlink(filepath.Join(repo, "gone"), filepath.Join(outside, "dangling")); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, dir, want, why string
	}{
		{"outside every repository", outside + "/cache/agentflow", outside + "/cache/agentflow", ""},
		{"trailing slash is cleaned", outside + "//agentflow/", outside + "/agentflow", ""},
		{"the account's own folder", own, own, ""},
		{"inside a working tree", repo + "/.cache/agentflow", own, "is inside the repository " + repo},
		{"inside the git directory", repo + "/.git/agentflow", own, "is inside the repository " + repo + "/.git"},
		{"inside a bare repository", bare + "/agentflow", own, "is inside the repository " + bare},
		{"a link outside that points into a repository", link + "/agentflow", own, "is inside the repository " + repo},
		{"a .. that would leave a repository", repo + "/../x/agentflow", own, "has a . or .. folder"},
		{"a . element", outside + "/./agentflow", own, "has a . or .. folder"},
		{"relative", "cache/agentflow", own, "is not absolute"},
		{"a file", file, own, "is a file"},
		{"a file used as a folder", file + "/agentflow", own, "cannot be checked"},
		{"a repository link to a folder outside", repo + "/to-outside/agentflow", real + "/agentflow", ""},
		{"a dangling link", outside + "/dangling/agentflow", own, "cannot be checked (its path does not resolve)"},
		{"a dangling link as the folder", outside + "/dangling", own, "cannot be checked (its path does not resolve)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.warnings.Reset()
			resetHomeWarnings()
			got, err := UserDir(tc.dir, "XDG_CACHE_HOME", "install cache", ownFn)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("UserDir(%s) = %s, want %s", tc.dir, got, tc.want)
			}
			w := f.warnings.String()
			if tc.why == "" {
				if w != "" {
					t.Fatalf("unexpected warning: %s", w)
				}
				return
			}
			if !strings.Contains(w, "ignoring the install cache "+tc.dir+" (from XDG_CACHE_HOME)") || !strings.Contains(w, tc.why) {
				t.Fatalf("warning %q does not name %s and %q", w, tc.dir, tc.why)
			}
		})
	}
}

func TestUserDirNotSetUsesOwnQuietly(t *testing.T) {
	f := newHomeFixture(t)
	got, err := UserDir("", "HOME", "install cache", func() (string, error) { return "/own/agentflow", nil })
	if err != nil || got != "/own/agentflow" {
		t.Fatalf("UserDir(\"\") = %q, %v", got, err)
	}
	if f.warnings.Len() != 0 {
		t.Fatalf("unexpected warning: %s", f.warnings)
	}
}

func TestUserDirDotfilesRepositoryAtAccountHome(t *testing.T) {
	f := newHomeFixture(t)
	gitRun(t, f.account, "init", "-q")
	dir := filepath.Join(f.account, "elsewhere", "agentflow")
	got, err := UserDir(dir, "XDG_CACHE_HOME", "install cache", func() (string, error) { return "/own", nil })
	if err != nil || got != dir {
		t.Fatalf("UserDir = %q, %v; want the dotfiles folder %s", got, err, dir)
	}
}

func TestUserDirWithoutOwnFolderIsRefused(t *testing.T) {
	newHomeFixture(t)
	repo := newRepo(t)
	_, err := UserDir(repo+"/agentflow", "LOCALAPPDATA", "install cache", func() (string, error) {
		return "", os.ErrNotExist
	})
	if err == nil || !strings.Contains(err.Error(), "set LOCALAPPDATA to a folder outside every repository") {
		t.Fatalf("err = %v", err)
	}
	// Outside every repository the missing own folder does not matter.
	dir := realDir(t, t.TempDir())
	if got, err := UserDir(dir, "LOCALAPPDATA", "install cache", func() (string, error) { return "", os.ErrNotExist }); err != nil || got != dir {
		t.Fatalf("UserDir = %q, %v", got, err)
	}
}

func TestUserDirWindowsRules(t *testing.T) {
	newHomeFixture(t)
	old := windowsRules
	windowsRules = true
	t.Cleanup(func() { windowsRules = old })
	own := func() (string, error) { return "/own", nil }
	outside := realDir(t, t.TempDir())
	if err := os.Symlink(outside, filepath.Join(outside, "link")); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{
		outside + "/agentflow":         outside + "/agentflow",
		outside + "/link/agentflow":    "/own", // a link or junction cannot be checked by agentflow.ps1
		`//?/C:/agentflow`:             "/own",
		outside + `\..\agentflow`:      "/own",
		outside + "/x/agentflow/../..": "/own",
	} {
		if got, err := UserDir(dir, "LOCALAPPDATA", "install cache", own); err != nil || got != want {
			t.Errorf("UserDir(%s) = %q, %v; want %s", dir, got, err, want)
		}
	}
}

// On Windows the account's own folder reached through a link is returned in
// own's spelling, so Go and agentflow.ps1 write and accept the same current.txt.
func TestUserDirWindowsOwnFolderThroughALink(t *testing.T) {
	newHomeFixture(t)
	old := windowsRules
	windowsRules = true
	t.Cleanup(func() { windowsRules = old })
	base := realDir(t, t.TempDir())
	own := filepath.Join(base, "Local", "agentflow")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "Local"), filepath.Join(base, "junction")); err != nil {
		t.Fatal(err)
	}
	got, err := UserDir(filepath.Join(base, "junction", "agentflow"), "LOCALAPPDATA", "install cache", func() (string, error) { return own, nil })
	if err != nil || got != own {
		t.Fatalf("UserDir = %q, %v; want the own spelling %s", got, err, own)
	}
}

func TestUserDirTestBinaryNeverFallsBackToRealAccount(t *testing.T) {
	old := accountOverridden
	accountOverridden = false
	t.Cleanup(func() { accountOverridden = old })
	_, err := UserDir("relative", "XDG_CACHE_HOME", "install cache", func() (string, error) { return "/real", nil })
	if err == nil || !strings.Contains(err.Error(), "does not fall back") {
		t.Fatalf("err = %v", err)
	}
}
