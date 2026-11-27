package ops_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/ops"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// unsetAgentEnvironment removes every variable that tells agentflow an AI
// agent runs it. goldentest.Isolate gives each test a fixed agent session.
func unsetAgentEnvironment(t *testing.T) {
	t.Helper()
	names := []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "AGENTFLOW_CODEX_HOST_ACTIVE"}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "CODEX_") && name != "CODEX_HOME" {
			names = append(names, name)
		}
	}
	for _, name := range names {
		// Setenv registers the restore; the unset leaves the variable absent.
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	if name := app.AgentEnvironment(); name != "" {
		t.Fatalf("%s is still set", name)
	}
}

// setTerminal makes ops see (or not see) a terminal on standard input. A test
// run has none, so a test that runs plugin add or plugin remove sets it.
func setTerminal(t *testing.T, isTerminal bool) {
	t.Helper()
	previous := ops.StdinIsTerminal
	ops.StdinIsTerminal = func() bool { return isTerminal }
	t.Cleanup(func() { ops.StdinIsTerminal = previous })
}

// asPerson makes the rest of the test a person at a terminal: no agent in the
// environment and a terminal on standard input.
func asPerson(t *testing.T) {
	t.Helper()
	unsetAgentEnvironment(t)
	setTerminal(t, true)
}

// newPluginRepo is newOpsRepo for a person at a terminal.
func newPluginRepo(t *testing.T, repoConfig, userConfig string) *opsRepo {
	t.Helper()
	r := newOpsRepo(t, repoConfig, userConfig)
	asPerson(t)
	return r
}

// stdinText makes text the standard input for the rest of the test.
func stdinText(t *testing.T, text string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = previous; f.Close() })
}

// stderrText sends standard error to a file for the rest of the test and
// returns what was written so far.
func stderrText(t *testing.T) func() string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = f
	t.Cleanup(func() { os.Stderr = previous; f.Close() })
	return func() string {
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}

const pluginYAML = `schema: 1
id: ci-notify
name: CI failure notifications
version: 0.1.0
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

// autoEverything turns every autonomy level and the plugin action to auto:
// plugin add and remove must still ask.
const autoEverything = "# my settings\nautonomy: {local: auto, remote: auto, actions: {plugin.install: auto}}\n"

// pluginSource writes a declarative plugin directory and returns it.
func pluginSource(t *testing.T, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeOpsFile(t, filepath.Join(dir, manifest.FileName), pluginYAML)
	for name, text := range extra {
		writeOpsFile(t, filepath.Join(dir, name), text)
	}
	return dir
}

func cleanPluginHome(t *testing.T, home string) {
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

func installedPlugins(t *testing.T, home string) []manifest.Installed {
	t.Helper()
	reg, err := manifest.ReadRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	return reg.Plugins
}

func lastAudit(t *testing.T, r *opsRepo, n int) []map[string]any {
	t.Helper()
	lines := r.auditLines(t)
	if len(lines) < n {
		t.Fatalf("%d audit lines, want at least %d", len(lines), n)
	}
	return lines[len(lines)-n:]
}

func TestPluginAddAlwaysAsksInstallsTurnedOffAndAuditsOnce(t *testing.T) {
	r := newPluginRepo(t, "agentflow: 1\n", autoEverything)
	cleanPluginHome(t, r.home)
	src := pluginSource(t, map[string]string{"README.md": "notes\n"})
	personal := func() string { b, _ := os.ReadFile(filepath.Join(r.home, "config.yml")); return string(b) }
	before := personal()

	// Declined: auto autonomy did not skip the question, and nothing was written.
	em, _, err := r.cli(t, false, "plugin", "add", src, "--json")
	if err == nil || err.Error() != "cancelled" {
		t.Fatalf("a declined add returned %v", err)
	}
	wantOpTypes(t, em)
	if got := installedPlugins(t, r.home); len(got) != 0 {
		t.Fatalf("a declined add installed %+v", got)
	}
	if _, err := os.Stat(manifest.PluginsPath(r.home)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a declined add wrote under the plugins directory: %v", err)
	}
	if lines := lastAudit(t, r, 1); lines[0]["result"] != "awaiting_approval" || lines[0]["operation"] != "plugin.add" ||
		lines[0]["action"] != "plugin.install" || lines[0]["class"] != "local" {
		t.Fatalf("audit line of the question: %v", lines[0])
	}

	// Confirmed: installed, turned off, one question and one answer in the audit log.
	auditBefore := len(r.auditLines(t))
	em, env, err := r.cli(t, true, "plugin", "add", src, "--json")
	if err != nil {
		t.Fatal(err)
	}
	wantOpTypes(t, em)
	var added app.PluginAdded
	if err := json.Unmarshal(env.Out.Bytes(), &added); err != nil {
		t.Fatalf("output %q: %v", env.Out.String(), err)
	}
	if added.ID != "ci-notify" || added.Result != "installed" || added.Enabled || !manifest.ValidHash(added.Hash) ||
		!strings.Contains(added.Next, "plugins.ci-notify.enabled=true") {
		t.Fatalf("result = %+v", added)
	}
	got := installedPlugins(t, r.home)
	if len(got) != 1 || got[0].Hash != added.Hash {
		t.Fatalf("registry = %+v", got)
	}
	if dirHash, err := manifest.HashDir(got[0].Dir); err != nil || dirHash != added.Hash {
		t.Fatalf("installed copy hashes to %s (%v), want %s", dirHash, err, added.Hash)
	}
	if personal() != before {
		t.Errorf("plugin add changed the personal settings:\n%s", personal())
	}
	lines := r.auditLines(t)[auditBefore:]
	if len(lines) != 2 || lines[0]["result"] != "awaiting_approval" || lines[1]["result"] != "applied" ||
		lines[1]["operation"] != "plugin.add" || lines[1]["action"] != "plugin.install" {
		t.Fatalf("audit lines of the confirmed add: %v", lines)
	}

	// The same version again changes nothing and asks nothing.
	auditBefore = len(r.auditLines(t))
	_, env, err = r.cli(t, false, "plugin", "add", src, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Out.Bytes(), &added); err != nil || added.Result != "unchanged" {
		t.Fatalf("second add = %+v (%v)", added, err)
	}
	if lines := r.auditLines(t)[auditBefore:]; len(lines) != 1 || lines[0]["result"] != "applied" {
		t.Fatalf("audit lines of the unchanged add: %v", lines)
	}
}

func TestPluginAddTextOutputAsksOnStandardInput(t *testing.T) {
	r := newPluginRepo(t, "agentflow: 1\n", confirmEverything)
	cleanPluginHome(t, r.home)
	out := quietStdout(t)
	errOut := stderrText(t)
	stdinText(t, "y\n")
	if _, _, err := r.cli(t, false, "plugin", "add", pluginSource(t, nil)); err != nil {
		t.Fatal(err)
	}
	if shown := errOut(); !strings.Contains(shown, "Add plugin ci-notify 0.1.0") || !strings.Contains(shown, "Proceed? [y/N]: ") {
		t.Fatalf("question:\n%s", shown)
	}
	text := out()
	if !strings.Contains(text, "Added ci-notify 0.1.0 (sha256:") || !strings.Contains(text, "plugins.ci-notify.enabled=true") {
		t.Fatalf("output:\n%s", text)
	}
	if _, _, err := r.cli(t, false, "plugin", "add"); err == nil || !strings.Contains(err.Error(), "usage: agentflow plugin add PATH") {
		t.Errorf("no path: %v", err)
	}
}

// --yes is accepted and ignored: the question still appears, and its answer
// decides, in the text and the JSON flow.
func TestPluginYesDoesNotSkipTheQuestion(t *testing.T) {
	r := newPluginRepo(t, "agentflow: 1\n", autoEverything)
	cleanPluginHome(t, r.home)
	src := pluginSource(t, nil)
	quietStdout(t)
	errOut := stderrText(t)

	stdinText(t, "n\n")
	if _, _, err := r.cli(t, false, "plugin", "add", src, "--yes"); err == nil || err.Error() != "cancelled" {
		t.Fatalf("--yes with the answer no: %v", err)
	}
	if shown := errOut(); !strings.Contains(shown, "--yes is ignored: plugin add always asks you.") || !strings.Contains(shown, "Proceed? [y/N]: ") {
		t.Fatalf("question:\n%s", shown)
	}
	if _, _, err := r.cli(t, false, "plugin", "add", src, "--yes", "--json"); err == nil || err.Error() != "cancelled" {
		t.Fatalf("--yes --json without an answer: %v", err)
	}
	if got := installedPlugins(t, r.home); len(got) != 0 {
		t.Fatalf("--yes installed %+v", got)
	}

	if _, _, err := r.cli(t, true, "plugin", "add", src, "--json"); err != nil {
		t.Fatal(err)
	}
	r.reopen(t, "agentflow: 1\n")
	stdinText(t, "\n") // the default answer is no
	if _, _, err := r.cli(t, false, "plugin", "remove", "ci-notify", "--yes"); err == nil || err.Error() != "cancelled" {
		t.Fatalf("remove --yes with the default answer: %v", err)
	}
	if _, _, err := r.cli(t, false, "plugin", "remove", "ci-notify", "--yes", "--json"); err == nil || err.Error() != "cancelled" {
		t.Fatalf("remove --yes --json without an answer: %v", err)
	}
	if got := installedPlugins(t, r.home); len(got) != 1 {
		t.Fatalf("--yes removed the plugin: %+v", got)
	}
}

// wantRefused runs plugin add and plugin remove, with and without --yes and
// --json, and fails unless every one refuses with want and changes nothing.
func wantRefused(t *testing.T, r *opsRepo, src, want string) {
	t.Helper()
	before := installedPlugins(t, r.home)
	personal, _ := os.ReadFile(filepath.Join(r.home, "config.yml"))
	for _, args := range [][]string{
		{"plugin", "add", src},
		{"plugin", "add", src, "--yes"},
		{"plugin", "add", src, "--yes", "--json"},
		{"plugin", "remove", "ci-notify"},
		{"plugin", "remove", "ci-notify", "--yes"},
		{"plugin", "remove", "ci-notify", "--yes", "--json"},
	} {
		stdinText(t, "y\n")
		audit := len(r.auditLines(t))
		em, _, err := r.cli(t, true, args...)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "run this in your own terminal") {
			t.Errorf("%v: %v, want %q and to run it in your own terminal", args, err, want)
		}
		wantOpTypes(t, em)
		if lines := r.auditLines(t)[audit:]; len(lines) != 1 || lines[0]["result"] != "failed" || lines[0]["action"] != "plugin.install" {
			t.Errorf("%v: audit lines %v", args, lines)
		}
	}
	if got := installedPlugins(t, r.home); len(got) != len(before) || len(got) == 1 && got[0].Hash != before[0].Hash {
		t.Errorf("a refused command changed the registry from %+v to %+v", before, got)
	}
	if got, _ := os.ReadFile(filepath.Join(r.home, "config.yml")); string(got) != string(personal) {
		t.Errorf("a refused command changed the personal settings:\n%s", got)
	}
}

// installedForRefusal installs ci-notify as a person and returns a source of
// a newer version, so both a refused add and a refused remove would change
// something.
func installedForRefusal(t *testing.T) (*opsRepo, string) {
	t.Helper()
	r := newPluginRepo(t, "agentflow: 1\n", autoEverything+"plugins:\n  ci-notify: {enabled: true}\n")
	cleanPluginHome(t, r.home)
	src := pluginSource(t, nil)
	if _, _, err := r.cli(t, true, "plugin", "add", src, "--json"); err != nil {
		t.Fatal(err)
	}
	r.reopen(t, "agentflow: 1\n")
	writeOpsFile(t, filepath.Join(src, manifest.FileName), strings.Replace(pluginYAML, "version: 0.1.0", "version: 0.2.0", 1))
	quietStdout(t)
	quietStderr(t)
	return r, src
}

func TestPluginAddAndRemoveRefuseAnAIAgent(t *testing.T) {
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "AGENTFLOW_CODEX_HOST_ACTIVE"} {
		t.Run(name, func(t *testing.T) {
			r, src := installedForRefusal(t)
			// A terminal on standard input does not help an agent, and the
			// override that promote accepts does not apply here.
			t.Setenv(name, "1")
			t.Setenv("AGENTFLOW_ALLOW_PROMOTE", "1")
			wantRefused(t, r, src, "an AI agent may not run it ("+name+" is set)")
		})
	}
}

func TestPluginAddAndRemoveNeedATerminalAndYesDoesNotReplaceIt(t *testing.T) {
	r, src := installedForRefusal(t)
	// With no agent variable set, a script, pipe or CI job still has no
	// terminal.
	setTerminal(t, false)
	wantRefused(t, r, src, "asks you at a terminal, and --yes does not skip that")
}

func TestPluginAddTokenIsBoundToTheFilesThatWereReviewed(t *testing.T) {
	r := newPluginRepo(t, "agentflow: 1\n", confirmEverything)
	cleanPluginHome(t, r.home)
	src := pluginSource(t, map[string]string{"README.md": "reviewed\n"})
	out, err := r.app.PluginAdd(t.Context(), src, "")
	if err != nil {
		t.Fatal(err)
	}
	pend, ok := out.(*app.Pending)
	if !ok || pend.Status != "needs_confirmation" || pend.ConfirmToken == "" {
		t.Fatalf("first call = %#v", out)
	}
	// A file changes between the preview and the apply.
	writeOpsFile(t, filepath.Join(src, "README.md"), "changed after review\n")
	if _, err := r.app.PluginAdd(t.Context(), src, pend.ConfirmToken); policy.ErrorCode(err) != "operation_changed" {
		t.Fatalf("a changed file: %v", err)
	}
	if got := installedPlugins(t, r.home); len(got) != 0 {
		t.Fatalf("installed after a failed token: %+v", got)
	}
	// A new preview of the changed files confirms.
	out, err = r.app.PluginAdd(t.Context(), src, "")
	pend, _ = out.(*app.Pending)
	if err != nil || pend == nil {
		t.Fatalf("second preview = %#v, %v", out, err)
	}
	if _, err := r.app.PluginAdd(t.Context(), src, pend.ConfirmToken); err != nil {
		t.Fatal(err)
	}
	if got := installedPlugins(t, r.home); len(got) != 1 {
		t.Fatalf("registry = %+v", got)
	}
}

func TestPluginAddSuggestOnlyInstallsNothing(t *testing.T) {
	r := newPluginRepo(t, "agentflow: 1\n", "autonomy: {local: suggest, remote: suggest}\n")
	src := pluginSource(t, nil)
	out, err := r.app.PluginAdd(t.Context(), src, "")
	pend, ok := out.(*app.Pending)
	if err != nil || !ok || pend.Status != "suggest" || len(pend.Commands) != 1 || !strings.HasPrefix(pend.Commands[0], "agentflow plugin add ") {
		t.Fatalf("suggest = %#v, %v", out, err)
	}
	if _, err := os.Stat(manifest.PluginsPath(r.home)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("suggest wrote under the plugins directory: %v", err)
	}
}

func TestPluginAddRefusesWhatItMustAndWritesNothing(t *testing.T) {
	r := newPluginRepo(t, "agentflow: 1\n", confirmEverything)
	outside := t.TempDir()
	writeOpsFile(t, filepath.Join(outside, "secret.txt"), "private\n")
	linked := pluginSource(t, nil)
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(linked, "link.txt")); err != nil {
		t.Skip("symlinks are not available:", err)
	}
	dirLinked := pluginSource(t, nil)
	if err := os.Symlink(outside, filepath.Join(dirLinked, "sub")); err != nil {
		t.Skip("symlinks are not available:", err)
	}
	noExec := t.TempDir()
	writeOpsFile(t, filepath.Join(noExec, manifest.FileName), "schema: 1\nid: needs-exec\nname: Needs exec\nversion: 0.1.0\ntier: process\nexec: {command: run}\npermissions: {subscribe: [op.failed]}\nextensions: {handler: [{name: h, on: [op.failed]}]}\n")
	cases := []struct {
		name string
		path string
		want string
	}{
		{"https address", "https://example.com/plugin.yml", "never an address"},
		{"git address", "git@github.com:owner/plugin.git", "never an address"},
		{"file address", "file:///tmp/plugin", "never an address"},
		{"missing path", filepath.Join(outside, "nope"), "no such file"},
		{"symlink to a file outside", linked, "is a symbolic link"},
		{"symlink to a directory outside", dirLinked, "is a symbolic link"},
		{"process plugin without its program", noExec, "build the plugin before you add it"},
		{"directory without a manifest", outside, manifest.FileName},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := len(r.auditLines(t))
			em, _, err := r.cli(t, true, "plugin", "add", c.path)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want it to contain %q", err, c.want)
			}
			wantOpTypes(t, em)
			if _, err := os.Stat(manifest.PluginsPath(r.home)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a refused add wrote under the plugins directory: %v", err)
			}
			if lines := r.auditLines(t)[before:]; len(lines) != 1 || lines[0]["result"] != "failed" || lines[0]["operation"] != "plugin.add" {
				t.Errorf("audit lines %v", lines)
			}
		})
	}
	if runtime.GOOS != "windows" {
		if b, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(b) != "private\n" {
			t.Error("the file outside the plugin changed")
		}
	}
}

func TestPluginUpgradeThenRemoveClearsEverythingAndKeepsTheRestOfTheSettings(t *testing.T) {
	personalText := "# my settings\nautonomy: {local: confirm, remote: confirm}\nplugins:\n  ci-notify:\n    enabled: true\n    settings:\n      signal: ci.failed\nupdate:\n  auto: false\n"
	r := newPluginRepo(t, "agentflow: 1\n", personalText)
	cleanPluginHome(t, r.home)
	src := pluginSource(t, nil)
	if _, _, err := r.cli(t, true, "plugin", "add", src, "--json"); err != nil {
		t.Fatal(err)
	}
	first := installedPlugins(t, r.home)[0]

	// An upgrade asks again, keeps the data directory and replaces the copy.
	writeOpsFile(t, filepath.Join(src, manifest.FileName), strings.Replace(pluginYAML, "version: 0.1.0", "version: 0.2.0", 1))
	if err := os.WriteFile(filepath.Join(manifest.DataPath(r.home, "ci-notify"), "state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.cli(t, false, "plugin", "add", src, "--json"); err == nil || err.Error() != "cancelled" {
		t.Fatalf("an upgrade without an answer: %v", err)
	}
	_, env, err := r.cli(t, true, "plugin", "add", src, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var added app.PluginAdded
	if err := json.Unmarshal(env.Out.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	if added.Result != "upgraded" || added.Replaced == nil || added.Replaced.Version != "0.1.0" || added.Replaced.Hash != first.Hash || !added.Enabled {
		t.Fatalf("upgrade = %+v", added)
	}
	if _, err := os.Stat(first.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the replaced copy is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(manifest.DataPath(r.home, "ci-notify"), "state.json")); err != nil {
		t.Errorf("the upgrade lost plugin data: %v", err)
	}

	// Remove: declined, then confirmed.
	r.reopen(t, "agentflow: 1\n")
	if _, _, err := r.cli(t, false, "plugin", "remove", "ci-notify", "--json"); err == nil || err.Error() != "cancelled" {
		t.Fatalf("a declined remove: %v", err)
	}
	if got := installedPlugins(t, r.home); len(got) != 1 {
		t.Fatalf("a declined remove changed the registry: %+v", got)
	}
	auditBefore := len(r.auditLines(t))
	em, env, err := r.cli(t, true, "plugin", "remove", "ci-notify", "--json")
	if err != nil {
		t.Fatal(err)
	}
	wantOpTypes(t, em)
	var removed app.PluginRemoved
	if err := json.Unmarshal(env.Out.Bytes(), &removed); err != nil {
		t.Fatal(err)
	}
	if !removed.Removed || removed.ID != "ci-notify" || removed.Version != "0.2.0" || !removed.SettingsCleared {
		t.Fatalf("removed = %+v", removed)
	}
	if got := installedPlugins(t, r.home); len(got) != 0 {
		t.Fatalf("registry after remove = %+v", got)
	}
	if _, err := os.Stat(manifest.RootPath(r.home, "ci-notify")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the plugin's directory is still there: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(r.home, "config.yml"))
	if want := "# my settings\nautonomy: {local: confirm, remote: confirm}\nupdate:\n  auto: false\n"; string(b) != want {
		t.Fatalf("personal settings after remove:\n%s\nwant:\n%s", b, want)
	}
	if lines := r.auditLines(t)[auditBefore:]; len(lines) != 2 || lines[1]["result"] != "applied" || lines[1]["operation"] != "plugin.remove" {
		t.Fatalf("audit lines of the confirmed remove: %v", lines)
	}

	// Removing it again, or something that was never there, is an error.
	for _, id := range []string{"ci-notify", "never-added", "../etc"} {
		if _, _, err := r.cli(t, true, "plugin", "remove", id); err == nil {
			t.Errorf("plugin remove %s succeeded", id)
		}
	}
}

func TestPluginRemoveNeverTouchesAPathFromTheRegistry(t *testing.T) {
	r := newPluginRepo(t, "agentflow: 1\n", confirmEverything)
	cleanPluginHome(t, r.home)
	if _, _, err := r.cli(t, true, "plugin", "add", pluginSource(t, nil), "--json"); err != nil {
		t.Fatal(err)
	}
	victim := t.TempDir()
	writeOpsFile(t, filepath.Join(victim, "precious"), "keep\n")
	if err := manifest.UpdateRegistry(r.home, func(reg *manifest.Registry) error {
		reg.Plugins[0].Dir = victim
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.cli(t, true, "plugin", "remove", "ci-notify", "--json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(victim, "precious")); err != nil {
		t.Fatalf("remove deleted a path it read from the registry: %v", err)
	}
}

func TestPluginListTextJSONAndToolAgree(t *testing.T) {
	r := newPluginRepo(t, "agentflow: 1\nplugins: {disabled: [ci-notify]}\n", "autonomy: {local: confirm, remote: confirm}\nplugins:\n  ci-notify: {enabled: true}\n")
	cleanPluginHome(t, r.home)
	out := quietStdout(t)
	if _, _, err := r.cli(t, false, "plugin", "list"); err != nil {
		t.Fatal(err)
	}
	if text := out(); !strings.Contains(text, "No plugins are installed") {
		t.Fatalf("empty list:\n%s", text)
	}
	if _, _, err := r.cli(t, true, "plugin", "add", pluginSource(t, nil), "--json"); err != nil {
		t.Fatal(err)
	}
	r.reopen(t, "agentflow: 1\nplugins: {disabled: [ci-notify]}\n")

	before := len(out())
	if _, _, err := r.cli(t, false, "plugin", "list"); err != nil {
		t.Fatal(err)
	}
	if text := out()[before:]; text != "ci-notify 0.1.0  declarative  turned on, turned off in this repository\n" {
		t.Fatalf("list text = %q", text)
	}

	_, env, err := r.cli(t, false, "plugin", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var viaCLI, viaTool app.PluginList
	if err := json.Unmarshal(env.Out.Bytes(), &viaCLI); err != nil {
		t.Fatal(err)
	}
	em, text, isErr := r.tool(t, "plugin.list", map[string]any{})
	if isErr {
		t.Fatalf("tool: %s", text)
	}
	wantOpTypes(t, em)
	if err := json.Unmarshal([]byte(text), &viaTool); err != nil {
		t.Fatal(err)
	}
	if len(viaCLI.Plugins) != 1 || len(viaTool.Plugins) != 1 || viaCLI.Plugins[0].Hash != viaTool.Plugins[0].Hash ||
		!viaCLI.Plugins[0].Enabled || !viaCLI.Plugins[0].DisabledInRepo || !viaCLI.Plugins[0].HashOK {
		t.Fatalf("cli %+v, tool %+v", viaCLI, viaTool)
	}
	if strings.Contains(text, r.home) || strings.Contains(text, "source") {
		t.Errorf("the tool output has a path: %s", text)
	}
}

func TestPluginHasOnlyAListTool(t *testing.T) {
	for _, c := range command.Default.Commands() {
		m := c.Meta()
		if strings.HasPrefix(m.Name, "plugin.") && m.Tool != "" && m.Tool != "agentflow_plugin_list" {
			t.Errorf("%s has the tool %s: installing and removing plugins is for the person at the terminal", m.Name, m.Tool)
		}
	}
}

func TestPluginListTextCountsUndeliveredEvents(t *testing.T) {
	r := newPluginRepo(t, "agentflow: 1\n", confirmEverything)
	cleanPluginHome(t, r.home)
	if _, _, err := r.cli(t, true, "plugin", "add", pluginSource(t, nil), "--json"); err != nil {
		t.Fatal(err)
	}
	letter := `{"event":{"id":"evt-1","type":"gh.ci.updated","repo":"repo-1","payload":{}},"consumer":"plugin:ci-notify","reason":"timeout","time":"2026-11-26T09:00:00Z"}` + "\n"
	writeOpsFile(t, filepath.Join(manifest.PluginsPath(r.home), manifest.DeadLetterFile), letter+letter)
	out := quietStdout(t)
	if _, _, err := r.cli(t, false, "plugin", "list"); err != nil {
		t.Fatal(err)
	}
	if text := out(); text != "ci-notify 0.1.0  declarative  turned off, 2 events not delivered\n" {
		t.Fatalf("list text = %q", text)
	}
}

const denyPushYAML = `schema: 1
id: no-push
name: No pushes
version: 0.1.0
tier: declarative
extensions:
  permission_rule:
    - name: no-push
      match: {operation: [push]}
      deny: Pushes are paused.
`

// config.set does not let an agent turn a deny rule off while
// console.autonomy says suggest: it gets the suggestion and no token.
func TestAgentCannotTurnOffADenyRuleThroughConfigSet(t *testing.T) {
	personal := "autonomy:\n  actions:\n    console.autonomy: suggest\nplugins:\n  no-push: {enabled: true}\n"
	r := newOpsRepo(t, "agentflow: 1\n", personal) // an agent session, as goldentest.Isolate sets it
	m, err := manifest.Load([]byte(denyPushYAML))
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.UpdateRegistry(r.home, func(reg *manifest.Registry) error {
		reg.Plugins = append(reg.Plugins, manifest.Installed{ID: m.ID, Version: m.Version, Tier: m.Tier, Manifest: m})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	const id = "personal:plugins.no-push.enabled"
	for _, edit := range []map[string]any{{"id": id, "op": "set", "value": false}, {"id": id, "op": "unset"}} {
		_, text, isErr := r.tool(t, "config.set", map[string]any{"edits": []any{edit}})
		var p app.Pending
		if err := json.Unmarshal([]byte(text), &p); isErr || err != nil || p.Status != "suggest" || p.ConfirmToken != "" {
			t.Fatalf("config.set %v = %s", edit, text)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(r.home, "config.yml")); string(b) != personal {
		t.Errorf("the personal settings changed:\n%s", b)
	}
}
