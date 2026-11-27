package install_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/plugin/install"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

const ciNotify = `schema: 1
id: ci-notify
name: CI failure notifications
version: 0.1.0
description: Sends a notification when the checks of a branch you work on start failing.
tier: declarative
permissions:
  subscribe: [gh.ci.updated]
  invoke: [notify]
extensions:
  handler:
    - name: ci-failed
      on: [gh.ci.updated]
      when:
        - {field: payload.state, equals: failure}
      actions:
        - invoke:
            command: notify
            input:
              signal: "{{settings.signal}}"
settings:
  - key: signal
    type: string
    default: ci.failed
    label: Notification signal
    description: The notification signal to send.
`

const timelineHints = `schema: 1
id: timeline-hints
name: Timeline hints
version: 0.1.0
tier: process
exec:
  command: timeline-hints
permissions:
  subscribe: [op.failed]
  publish: [plugin.annotation.added]
extensions:
  handler:
    - name: failed-operations
      on: [op.failed]
`

var epoch = time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

// tree writes files under a new directory. A name that ends in "*" is
// executable.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, "*") {
			name, mode = strings.TrimSuffix(name, "*"), 0o755
		}
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func declarative(t *testing.T) string {
	t.Helper()
	return tree(t, map[string]string{manifest.FileName: ciNotify, "README.md": "ci-notify\n"})
}

func process(t *testing.T) string {
	t.Helper()
	return tree(t, map[string]string{manifest.FileName: timelineHints, "timeline-hints*": "#!/bin/sh\nexit 0\n"})
}

func mustPlan(t *testing.T, home, src string) *install.AddPlan {
	t.Helper()
	p, err := install.PlanAdd(home, src)
	if err != nil {
		t.Fatalf("PlanAdd(%s): %v", src, err)
	}
	return p
}

func mustAdd(t *testing.T, home, src string) install.Result {
	t.Helper()
	res, err := install.Add(home, mustPlan(t, home, src), epoch)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return res
}

func registry(t *testing.T, home string) manifest.Registry {
	t.Helper()
	r, err := manifest.ReadRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// cleanup makes a read-only installed tree deletable by t.TempDir.
func cleanup(t *testing.T, home string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
	})
}

func TestRefuseRemote(t *testing.T) {
	refused := []string{
		"http://example.com/p", "https://example.com/p.yml", "git@github.com:owner/repo.git", "ssh://git@host/x",
		"file:///tmp/plugin", "file:/tmp/plugin", "git://host/x", "ftp://host/x", "npm:some-plugin", "mailto:a@b.c",
		"./dir://x", "user@host:path",
	}
	for _, s := range refused {
		if err := install.RefuseRemote(s); !errors.Is(err, install.ErrRemoteSource) {
			t.Errorf("RefuseRemote(%q) = %v, want ErrRemoteSource", s, err)
		}
	}
	for _, s := range []string{"plugin", "./plugin", "../plugin", "/abs/plugin", "a/b/c", "ci-notify.yml", `C:\plugins\x`, "C:/plugins/x", `.\plugin`} {
		if err := install.RefuseRemote(s); err != nil {
			t.Errorf("RefuseRemote(%q) = %v, want nil", s, err)
		}
	}
	if err := install.RefuseRemote(""); err == nil || errors.Is(err, install.ErrRemoteSource) {
		t.Errorf("empty path: %v", err)
	}
}

func TestPlanAddDoesNotWriteAndAddressIsRefusedFirst(t *testing.T) {
	home := t.TempDir()
	if _, err := install.PlanAdd(home, "https://example.com/p"); !errors.Is(err, install.ErrRemoteSource) {
		t.Fatalf("got %v", err)
	}
	src := declarative(t)
	p := mustPlan(t, home, src)
	if p.Outcome != install.Installed || p.Previous != nil || p.Manifest.ID != "ci-notify" {
		t.Fatalf("plan = %+v", p)
	}
	if !manifest.ValidHash(p.Hash) || p.Hash != manifest.HashFiles(p.Files) {
		t.Fatalf("hash = %s", p.Hash)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("PlanAdd wrote %d entries to the home", len(entries))
	}
	abs, _ := filepath.Abs(src)
	if p.Source != abs {
		t.Fatalf("source = %s, want %s", p.Source, abs)
	}
}

func TestPlanAddSingleYAMLFile(t *testing.T) {
	home := t.TempDir()
	dir := tree(t, map[string]string{"anything.yaml": ciNotify})
	p := mustPlan(t, home, filepath.Join(dir, "anything.yaml"))
	if len(p.Files) != 1 || p.Files[0].Path != manifest.FileName {
		t.Fatalf("files = %+v", p.Files)
	}
	// The same bytes in a directory hash the same.
	q := mustPlan(t, home, tree(t, map[string]string{manifest.FileName: ciNotify}))
	if p.Hash != q.Hash {
		t.Fatalf("a single file and a directory with that file hash differently: %s %s", p.Hash, q.Hash)
	}
	proc := tree(t, map[string]string{"p.yml": timelineHints})
	_, err := install.PlanAdd(home, filepath.Join(proc, "p.yml"))
	var ve *manifest.ValidationError
	if !errors.As(err, &ve) || ve.Problems[0].Code != manifest.CodeTier {
		t.Fatalf("a process plugin as one file: %v", err)
	}
}

func TestPlanAddRefusals(t *testing.T) {
	home := t.TempDir()
	outside := tree(t, map[string]string{"secret.txt": "do not read"})
	cases := map[string]func(t *testing.T) string{
		"no manifest": func(t *testing.T) string { return tree(t, map[string]string{"a.txt": "x"}) },
		"missing path": func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "nope")
		},
		"invalid manifest": func(t *testing.T) string {
			return tree(t, map[string]string{manifest.FileName: strings.Replace(ciNotify, "tier: declarative", "tier: builtin", 1)})
		},
		"symlink inside": func(t *testing.T) string {
			d := declarative(t)
			if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(d, "link.txt")); err != nil {
				t.Skip("symlinks are not available:", err)
			}
			return d
		},
		"directory symlink inside": func(t *testing.T) string {
			d := declarative(t)
			if err := os.Symlink(outside, filepath.Join(d, "sub")); err != nil {
				t.Skip("symlinks are not available:", err)
			}
			return d
		},
		"symlink root": func(t *testing.T) string {
			link := filepath.Join(t.TempDir(), "plugin")
			if err := os.Symlink(declarative(t), link); err != nil {
				t.Skip("symlinks are not available:", err)
			}
			return link
		},
		"symlink to a file": func(t *testing.T) string {
			link := filepath.Join(t.TempDir(), "p.yml")
			real := filepath.Join(tree(t, map[string]string{"x.yml": ciNotify}), "x.yml")
			if err := os.Symlink(real, link); err != nil {
				t.Skip("symlinks are not available:", err)
			}
			return link
		},
		"git directory": func(t *testing.T) string {
			return tree(t, map[string]string{manifest.FileName: ciNotify, ".git/config": "[core]"})
		},
		"process without its executable": func(t *testing.T) string {
			return tree(t, map[string]string{manifest.FileName: timelineHints})
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := install.PlanAdd(home, mk(t)); err == nil {
				t.Fatal("PlanAdd succeeded")
			}
		})
	}
	if runtime.GOOS != "windows" {
		d := tree(t, map[string]string{manifest.FileName: timelineHints, "timeline-hints": "#!/bin/sh\n"})
		_, err := install.PlanAdd(home, d)
		var ve *manifest.ValidationError
		if !errors.As(err, &ve) || ve.Problems[0].Code != manifest.CodeExec || !strings.Contains(ve.Problems[0].Message, "executable") {
			t.Fatalf("a process plugin whose file is not executable: %v", err)
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("a refused plan wrote %d entries", len(entries))
	}
}

func TestAddWritesAReadOnlyVerifiedCopyAndTheRegistry(t *testing.T) {
	home := t.TempDir()
	cleanup(t, home)
	src := declarative(t)
	plan := mustPlan(t, home, src)
	res, err := install.Add(home, plan, epoch)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != install.Installed || res.Previous != nil {
		t.Fatalf("result = %+v", res)
	}
	dir := manifest.InstallPath(home, "ci-notify", plan.Hash)
	if got, err := manifest.HashDir(dir); err != nil || got != plan.Hash {
		t.Fatalf("installed copy hashes to %s (%v), want %s", got, err, plan.Hash)
	}
	reg := registry(t, home)
	if len(reg.Plugins) != 1 {
		t.Fatalf("registry = %+v", reg)
	}
	e := reg.Plugins[0]
	if e.ID != "ci-notify" || e.Version != "0.1.0" || e.Tier != manifest.TierDeclarative || e.Hash != plan.Hash || e.Dir != dir ||
		!e.InstalledAt.Equal(epoch) || e.Source != plan.Source || e.Manifest.Name != "CI failure notifications" {
		t.Fatalf("entry = %+v", e)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{
			dir:                                   0o500,
			filepath.Join(dir, manifest.FileName): 0o400,
			filepath.Join(dir, "README.md"):       0o400,
			manifest.DataPath(home, "ci-notify"):  0o700,
			manifest.PluginsPath(home):            0o700,
			manifest.RegistryPath(home):           0o600,
			filepath.Dir(dir):                     0o700,
		} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != want {
				t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed"), 0o600); err == nil {
			t.Error("the installed copy is writable")
		}
	}
	// No personal settings are written: enabling is the person's change.
	if _, err := os.Stat(filepath.Join(home, "config.yml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Add touched config.yml: %v", err)
	}
	// The source stays as it was.
	if b, _ := os.ReadFile(filepath.Join(src, manifest.FileName)); string(b) != ciNotify {
		t.Error("the source changed")
	}
}

func TestAddKeepsTheExecutableBitAndHashesIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no executable bit")
	}
	home := t.TempDir()
	cleanup(t, home)
	res := mustAdd(t, home, process(t))
	exe := filepath.Join(res.Entry.Dir, "timeline-hints")
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o500 {
		t.Fatalf("executable mode = %o", info.Mode().Perm())
	}
	if got, _ := manifest.HashDir(res.Entry.Dir); got != res.Entry.Hash {
		t.Fatalf("hash %s != %s", got, res.Entry.Hash)
	}
}

func TestAddSameVersionAgainChangesNothing(t *testing.T) {
	home := t.TempDir()
	cleanup(t, home)
	src := declarative(t)
	first := mustAdd(t, home, src)
	before, _ := os.Stat(manifest.RegistryPath(home))
	plan := mustPlan(t, home, src)
	if plan.Outcome != install.Unchanged || plan.Outcome.Changes() {
		t.Fatalf("outcome = %s", plan.Outcome)
	}
	again, err := install.Add(home, plan, epoch.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again.Outcome != install.Unchanged || !again.Entry.InstalledAt.Equal(first.Entry.InstalledAt) {
		t.Fatalf("again = %+v", again)
	}
	after, _ := os.Stat(manifest.RegistryPath(home))
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("an unchanged add rewrote the registry")
	}
}

func TestAddAnUpgradeReplacesTheCopyAndKeepsTheData(t *testing.T) {
	home := t.TempDir()
	cleanup(t, home)
	first := mustAdd(t, home, declarative(t))
	data := manifest.DataPath(home, "ci-notify")
	if err := os.WriteFile(filepath.Join(data, "state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	next := tree(t, map[string]string{manifest.FileName: strings.Replace(ciNotify, "version: 0.1.0", "version: 0.2.0", 1)})
	plan := mustPlan(t, home, next)
	if plan.Outcome != install.Upgraded || plan.Previous == nil || plan.Previous.Version != "0.1.0" {
		t.Fatalf("plan = %+v", plan)
	}
	sum := plan.Summary(false)
	if sum.Operation != "upgrade" || sum.Replaces == nil || sum.Replaces.Hash != first.Entry.Hash {
		t.Fatalf("summary = %+v", sum)
	}
	res, err := install.Add(home, plan, epoch.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != install.Upgraded || res.Previous == nil || res.Previous.Hash != first.Entry.Hash {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(first.Entry.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the previous copy is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "state.json")); err != nil {
		t.Errorf("the data directory lost a file: %v", err)
	}
	reg := registry(t, home)
	if len(reg.Plugins) != 1 || reg.Plugins[0].Version != "0.2.0" || reg.Plugins[0].Hash == first.Entry.Hash {
		t.Fatalf("registry = %+v", reg)
	}
}

func TestAddRepairsACopyThatNoLongerMatchesItsHash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only modes differ")
	}
	home := t.TempDir()
	cleanup(t, home)
	src := declarative(t)
	first := mustAdd(t, home, src)
	if err := os.Chmod(first.Entry.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(first.Entry.Dir, "README.md")
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := mustPlan(t, home, src)
	if plan.Outcome != install.Repaired || !plan.Outcome.Changes() {
		t.Fatalf("outcome = %s", plan.Outcome)
	}
	if s := plan.Summary(false); s.Operation != "repair" || len(s.Warnings) < 2 {
		t.Fatalf("summary = %+v", s)
	}
	if _, err := install.Add(home, plan, epoch.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := manifest.HashDir(first.Entry.Dir); err != nil || got != first.Entry.Hash {
		t.Fatalf("after the repair the copy hashes to %s (%v)", got, err)
	}
}

func TestAddACorruptRegistryIsLeftAlone(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(manifest.PluginsPath(home), 0o700); err != nil {
		t.Fatal(err)
	}
	bad := []byte(`{"version": 1, "plugins": [{"id": "x"`)
	if err := os.WriteFile(manifest.RegistryPath(home), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := install.PlanAdd(home, declarative(t)); err == nil {
		t.Fatal("PlanAdd read a corrupt registry")
	}
	if got, _ := os.ReadFile(manifest.RegistryPath(home)); string(got) != string(bad) {
		t.Error("the registry was changed")
	}
}

func TestAddTwoPluginsAtOnceKeepsBoth(t *testing.T) {
	home := t.TempDir()
	cleanup(t, home)
	a, b := declarative(t), process(t)
	pa, pb := mustPlan(t, home, a), mustPlan(t, home, b)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, p := range []*install.AddPlan{pa, pb} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = install.Add(home, p, epoch)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if reg := registry(t, home); len(reg.Plugins) != 2 {
		t.Fatalf("registry = %+v", reg)
	}
}

func TestSummaryListsEverythingThePersonConfirms(t *testing.T) {
	home := t.TempDir()
	s := mustPlan(t, home, declarative(t)).Summary(false)
	if s.Operation != "install" || s.ID != "ci-notify" || s.Name == "" || s.Version != "0.1.0" || s.Tier != "declarative" ||
		!manifest.ValidHash(s.Hash) || s.Exec != nil || s.Replaces != nil {
		t.Fatalf("summary = %+v", s)
	}
	if len(s.Permissions.Subscribe) != 1 || s.Permissions.Invoke[0] != "notify" || s.Permissions.Publish == nil {
		t.Fatalf("permissions = %+v", s.Permissions)
	}
	if len(s.Extensions) != 1 || !strings.Contains(s.Extensions[0], "gh.ci.updated") || !strings.Contains(s.Extensions[0], "invoke notify") {
		t.Fatalf("extensions = %v", s.Extensions)
	}
	if len(s.Settings) != 1 || s.Settings[0] != "signal" {
		t.Fatalf("settings = %v", s.Settings)
	}
	joined := strings.Join(s.Warnings, "\n")
	if !strings.Contains(joined, "wait for your approval") || strings.Contains(joined, "not a sandbox") {
		t.Fatalf("warnings = %v", s.Warnings)
	}
	if !strings.Contains(s.Next, "plugins.ci-notify.enabled=true") || !strings.Contains(s.Next, "turned off") {
		t.Fatalf("next = %q", s.Next)
	}
	// A process plugin shows its executable and says plainly that it is not sandboxed.
	ps := mustPlan(t, home, process(t)).Summary(true)
	if ps.Exec == nil || ps.Exec.Command != "timeline-hints" || !strings.Contains(strings.Join(ps.Warnings, "\n"), "not a sandbox") {
		t.Fatalf("process summary = %+v", ps)
	}
	if !strings.Contains(strings.Join(ps.Warnings, "\n"), "already true") || !strings.Contains(ps.Next, "enabled=false") {
		t.Fatalf("an already enabled plugin: %+v", ps)
	}
}

func TestSummaryIsStableAcrossReads(t *testing.T) {
	home := t.TempDir()
	src := declarative(t)
	a, b := marshal(mustPlan(t, home, src).Summary(false)), marshal(mustPlan(t, home, src).Summary(false))
	if a != b {
		t.Fatalf("two plans of one tree summarise differently:\n%s\n%s", a, b)
	}
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("changed after review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := marshal(mustPlan(t, home, src).Summary(false)); c == a {
		t.Fatal("a changed file did not change the summary, so a token would not notice")
	}
}

func TestRemoveDeletesEverythingAndOnlyThat(t *testing.T) {
	home := t.TempDir()
	cleanup(t, home)
	keep := mustAdd(t, home, process(t))
	res := mustAdd(t, home, declarative(t))
	data := manifest.DataPath(home, "ci-notify")
	if err := os.WriteFile(filepath.Join(data, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := install.PlanRemove(home, "ci-notify")
	if err != nil {
		t.Fatal(err)
	}
	if s := plan.Summary(true); s.Operation != "remove" || len(s.Deletes) != 3 || s.Hash != res.Entry.Hash {
		t.Fatalf("summary = %+v", s)
	}
	if s := plan.Summary(false); len(s.Deletes) != 2 {
		t.Fatalf("summary without settings = %+v", s)
	}
	if err := install.Remove(home, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manifest.RootPath(home, "ci-notify")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the plugin's directory is still there: %v", err)
	}
	if reg := registry(t, home); len(reg.Plugins) != 1 || reg.Plugins[0].ID != "timeline-hints" {
		t.Fatalf("registry = %+v", reg)
	}
	if _, err := os.Stat(keep.Entry.Dir); err != nil {
		t.Errorf("another plugin was touched: %v", err)
	}
	if _, err := install.PlanRemove(home, "ci-notify"); !errors.Is(err, manifest.ErrNotFound) {
		t.Fatalf("second remove: %v", err)
	}
	if err := install.Remove(home, plan); !errors.Is(err, manifest.ErrNotFound) {
		t.Fatalf("Remove of a plugin that is gone: %v", err)
	}
}

func TestRemoveNeverTrustsTheRegistryPath(t *testing.T) {
	home := t.TempDir()
	cleanup(t, home)
	mustAdd(t, home, declarative(t))
	victim := t.TempDir()
	if err := os.WriteFile(filepath.Join(victim, "precious"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manifest.UpdateRegistry(home, func(r *manifest.Registry) error {
		r.Plugins[0].Dir = victim
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := install.PlanRemove(home, "ci-notify")
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Remove(home, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(victim, "precious")); err != nil {
		t.Fatalf("remove followed the path in the registry: %v", err)
	}
}

func TestPlanRemoveRefusesAnIDThatIsNotAPluginID(t *testing.T) {
	home := t.TempDir()
	for _, id := range []string{"", "..", "../x", "a/b", ".", "Ci-Notify", "x"} {
		if _, err := install.PlanRemove(home, id); err == nil || errors.Is(err, manifest.ErrNotFound) {
			t.Errorf("PlanRemove(%q) = %v", id, err)
		}
	}
	if _, err := install.PlanRemove(home, "ci-notify"); !errors.Is(err, manifest.ErrNotFound) {
		t.Errorf("an unknown plugin: %v", err)
	}
}

func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
