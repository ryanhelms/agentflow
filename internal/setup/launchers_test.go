package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/version"
	"github.com/ryanhelms/agentflow/templates"
)

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func launcherTemplate(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := templates.Repo.ReadFile(path.Join("repo", rel))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// stockTestdata maps each launcher path to the hashes of its released copies in
// testdata/stock/<release>/<path>.
func stockTestdata(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	releases, err := os.ReadDir(filepath.Join("testdata", "stock"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range releases {
		for _, rel := range LauncherFiles {
			data, err := os.ReadFile(filepath.Join("testdata", "stock", r.Name(), filepath.FromSlash(rel)))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			out[rel] = append(out[rel], sha(data))
		}
	}
	return out
}

// Whoever changes a launcher template must add its hash, so that pin can replace it later.
func TestStockLaunchersIncludeEveryTemplate(t *testing.T) {
	for _, rel := range LauncherFiles {
		if h := sha(launcherTemplate(t, rel)); !slices.Contains(stockLaunchers[rel], h) {
			t.Errorf("%s: add the template's hash %s to stockLaunchers (keep the old ones)", rel, h)
		}
	}
	if len(stockLaunchers) != len(LauncherFiles) {
		t.Errorf("stockLaunchers has %d paths, LauncherFiles %d", len(stockLaunchers), len(LauncherFiles))
	}
}

// Every listed hash is a released file kept in testdata, or the current template. A
// replaced template's old hash fails here until its released copy is added to testdata.
func TestStockLaunchersMatchReleasedBytes(t *testing.T) {
	released := stockTestdata(t)
	for _, rel := range LauncherFiles {
		cur := sha(launcherTemplate(t, rel))
		for _, h := range stockLaunchers[rel] {
			if h != cur && !slices.Contains(released[rel], h) {
				t.Errorf("%s: %s is neither the template nor a released copy in testdata/stock", rel, h)
			}
		}
		for _, h := range released[rel] {
			if !slices.Contains(stockLaunchers[rel], h) {
				t.Errorf("%s: released copy %s is missing from stockLaunchers", rel, h)
			}
		}
	}
}

// Devflow-era commit-msg hooks call devflow; replacing one would switch an unmigrated
// repository to agentflow (docs/agentflow-migration.md). Keep them out of the list.
func TestStockLaunchersExcludeDevflowHooks(t *testing.T) {
	for _, h := range []string{
		"fcdc10ff077e300d5fe56be4a5c8ca572d2005907932b284b3433f4a3668b3cd",
		"65ee47e1e3301db51fc923d85862f147db2bb538a82686dfa8fb0a8b2b0250e6",
	} {
		if slices.Contains(stockLaunchers[".githooks/commit-msg"], h) {
			t.Errorf("devflow-era commit-msg %s must not be replaced by pin", h)
		}
	}
}

// With release tags present (not in a shallow CI checkout), every released launcher file
// from v0.53.0 on must be listed.
func TestStockLaunchersMatchGitTags(t *testing.T) {
	out, err := exec.Command("git", "tag", "--list", "v0.*").Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		t.Skip("no release tags in this checkout")
	}
	checked := 0
	for _, tag := range strings.Fields(string(out)) {
		if VersionLess(tag, "v0.53.0") {
			continue
		}
		for _, rel := range LauncherFiles {
			data, err := exec.Command("git", "cat-file", "blob", tag+":templates/repo/"+rel).Output()
			if err != nil {
				continue // not in that release
			}
			checked++
			if !slices.Contains(stockLaunchers[rel], sha(data)) {
				t.Errorf("%s at %s (%s) is missing from stockLaunchers", rel, tag, sha(data))
			}
		}
	}
	if checked == 0 {
		t.Skip("no released launcher files found")
	}
}

func stockCopy(t *testing.T, release, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "stock", release, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeRepoFile(t *testing.T, dir, rel string, data []byte, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, mode); err != nil {
		t.Fatal(err)
	}
}

func refresh(t *testing.T, dir string, check bool) map[string]LauncherFile {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	files, err := RefreshLaunchers(root, check)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]LauncherFile{}
	for _, f := range files {
		out[f.Path] = f
	}
	return out
}

func TestRefreshLaunchers(t *testing.T) {
	dir := t.TempDir()
	old := stockCopy(t, "v0.54.0", ".agentflow/bin/agentflow")
	writeRepoFile(t, dir, ".agentflow/bin/agentflow", old, 0o644)
	ps1 := launcherTemplate(t, ".agentflow/bin/agentflow.ps1")
	writeRepoFile(t, dir, ".agentflow/bin/agentflow.ps1", ps1, 0o644)
	// A converted line ending is a customisation, not a stock file.
	crlf := bytes.ReplaceAll(stockCopy(t, "v0.53.0", ".githooks/commit-msg"), []byte("\n"), []byte("\r\n"))
	writeRepoFile(t, dir, ".githooks/commit-msg", crlf, 0o755)
	target := filepath.Join(dir, "elsewhere")
	prePush := stockCopy(t, "v0.56.0", ".githooks/pre-push")
	writeRepoFile(t, dir, "elsewhere", prePush, 0o755)
	if err := os.Symlink(target, filepath.Join(dir, ".githooks", "pre-push")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}

	want := map[string]string{
		".agentflow/bin/agentflow":     LauncherOutdated,
		".agentflow/bin/agentflow.ps1": LauncherCurrent,
		".githooks/commit-msg":         LauncherCustom,
		".githooks/pre-push":           LauncherNotFile,
	}
	got := refresh(t, dir, true)
	for rel, state := range want {
		if got[rel].State != state {
			t.Errorf("check: %s = %+v, want %s", rel, got[rel], state)
		}
	}
	if !strings.Contains(got[".githooks/commit-msg"].Detail, "/templates/repo/.githooks/commit-msg") {
		t.Errorf("custom detail does not say how to replace it: %q", got[".githooks/commit-msg"].Detail)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, ".agentflow", "bin", "agentflow")); !bytes.Equal(data, old) {
		t.Fatal("check wrote the launcher")
	}

	want[".agentflow/bin/agentflow"] = LauncherUpdated
	got = refresh(t, dir, false)
	for rel, state := range want {
		if got[rel].State != state {
			t.Errorf("refresh: %s = %+v, want %s", rel, got[rel], state)
		}
	}
	launcher := filepath.Join(dir, ".agentflow", "bin", "agentflow")
	if data, _ := os.ReadFile(launcher); !bytes.Equal(data, launcherTemplate(t, ".agentflow/bin/agentflow")) {
		t.Error("the outdated launcher was not replaced with the template")
	}
	if info, _ := os.Stat(launcher); info.Mode().Perm() != 0o755 {
		t.Errorf("launcher mode = %v, want 0755", info.Mode().Perm())
	}
	if data, _ := os.ReadFile(filepath.Join(dir, ".githooks", "commit-msg")); !bytes.Equal(data, crlf) {
		t.Error("the customised hook was changed")
	}
	if data, _ := os.ReadFile(target); !bytes.Equal(data, prePush) {
		t.Error("pin wrote through the pre-push link")
	}
	if info, _ := os.Lstat(filepath.Join(dir, ".githooks", "pre-push")); info.Mode()&os.ModeSymlink == 0 {
		t.Error("the pre-push link was replaced")
	}

	// Up to date now: a second run changes nothing.
	got = refresh(t, dir, false)
	if got[".agentflow/bin/agentflow"].State != LauncherCurrent {
		t.Errorf("second run: %+v", got[".agentflow/bin/agentflow"])
	}
}

func TestRefreshLaunchersSkipsMissingAndLinkedFolders(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	old := stockCopy(t, "v0.56.0", ".githooks/pre-push")
	writeRepoFile(t, outside, "pre-push", old, 0o755)
	if err := os.Symlink(outside, filepath.Join(dir, ".githooks")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	// A folder link that stays inside the repository is still a link.
	inside := filepath.Join(dir, "real-bin")
	writeRepoFile(t, dir, "real-bin/agentflow", stockCopy(t, "v0.54.0", ".agentflow/bin/agentflow"), 0o755)
	if err := os.MkdirAll(filepath.Join(dir, ".agentflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(dir, ".agentflow", "bin")); err != nil {
		t.Fatal(err)
	}
	got := refresh(t, dir, false)
	for _, rel := range LauncherFiles {
		if got[rel].State != LauncherUnsafe {
			t.Errorf("%s = %+v, want %s", rel, got[rel], LauncherUnsafe)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(outside, "pre-push")); !bytes.Equal(data, old) {
		t.Error("pin wrote outside the repository")
	}
	if data, _ := os.ReadFile(filepath.Join(inside, "agentflow")); !bytes.Equal(data, stockCopy(t, "v0.54.0", ".agentflow/bin/agentflow")) {
		t.Error("pin wrote through a folder link")
	}

	empty := t.TempDir()
	if got := refresh(t, empty, false); len(got) != 0 {
		t.Errorf("missing files reported: %+v", got)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Errorf("pin created files: %v", entries)
	}
}

func setRelease(t *testing.T, ver string) {
	t.Helper()
	oldC, oldV := version.Commit, version.Version
	version.Commit, version.Version = strings.Repeat("abcdef1234", 4), ver
	t.Cleanup(func() { version.Commit, version.Version = oldC, oldV })
}

func TestPinRefreshesLaunchers(t *testing.T) {
	old := stockCopy(t, "v0.54.0", ".agentflow/bin/agentflow")

	// A non-release build refreshes nothing.
	dir := t.TempDir()
	writeRepoFile(t, dir, ".agentflow/bin/agentflow", old, 0o755)
	if _, err := Pin(dir, false); !errors.Is(err, ErrNotRelease) {
		t.Fatalf("non-release Pin = %v, want ErrNotRelease", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, ".agentflow", "bin", "agentflow")); !bytes.Equal(data, old) {
		t.Fatal("a non-release build replaced the launcher")
	}

	setRelease(t, "0.61.0")
	// No workflow, but a stock launcher: pin still handles it.
	res, err := Pin(dir, true)
	if err != nil || res.File != "" || !res.Behind() || len(res.Launchers) != 1 || res.Launchers[0].State != LauncherOutdated {
		t.Fatalf("Pin --check without workflow = %+v, %v", res, err)
	}
	res, err = Pin(dir, false)
	if err != nil || res.Launchers[0].State != LauncherUpdated {
		t.Fatalf("Pin without workflow = %+v, %v", res, err)
	}
	if res, err = Pin(dir, true); err != nil || res.Behind() {
		t.Fatalf("Pin --check after pin = %+v, %v", res, err)
	}

	// No workflow and no stock file: today's error.
	custom := t.TempDir()
	writeRepoFile(t, custom, ".agentflow/bin/agentflow", []byte("#!/bin/sh\n"), 0o755)
	if _, err := Pin(custom, false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Pin with no workflow and a custom launcher = %v, want not-exist", err)
	}
	if _, err := Pin(t.TempDir(), true); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Pin in an empty repository = %v, want not-exist", err)
	}

	// A customised launcher only warns: --check is not behind for it.
	wf := t.TempDir()
	writeRepoFile(t, wf, CallerWorkflow, []byte("jobs: {}\n"), 0o644)
	writeRepoFile(t, wf, ".githooks/commit-msg", []byte("#!/bin/sh\n"), 0o755)
	res, err = Pin(wf, true)
	if err != nil || res.File != CallerWorkflow || res.Behind() || res.Launchers[0].State != LauncherCustom {
		t.Fatalf("Pin --check with a custom hook = %+v, %v", res, err)
	}
}
