package plugin_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
	"github.com/ryanhelms/agentflow/internal/plugin/plugintest"
	"github.com/ryanhelms/agentflow/internal/policy"
)

func TestPermissionRulesOnlyDeny(t *testing.T) {
	src, layers := &plugintest.Source{}, &plugintest.Layers{}
	rule := func(id string, match manifest.RuleMatch) manifest.Installed {
		return manifest.Installed{ID: id, Tier: manifest.TierDeclarative, Manifest: manifest.Manifest{ID: id, Tier: manifest.TierDeclarative,
			Extensions: manifest.Extensions{PermissionRule: []manifest.PermissionRule{{Name: "r", Match: match, Deny: "No pushes from agents.\nsecond line"}}}}}
	}
	src.Set(rule("guard", manifest.RuleMatch{Operation: []string{"push", "config.*", "approval.*", "policy.*", "plugin.*"}, Origin: []string{"mcp:*"}}),
		rule("off", manifest.RuleMatch{Operation: []string{"*"}}))
	layers.SetPersonal("guard", plugin.PersonalPlugin{Enabled: true})
	st := &plugin.PermissionStage{Source: func() manifest.Source { return src }, Layers: func() plugin.Layers { return layers },
		RegistryPath: func() string { return "/home/me/.agentflow/plugins/installed.json" },
		ConfigPath:   func() string { return "/home/me/.agentflow/config.yml" }}
	var ran []string
	spec := func(name string) command.Spec[fakeIn, any] {
		return command.Spec[fakeIn, any]{Name: name, Policy: command.Policy{Class: policy.Local, Action: name},
			Run: func(context.Context, *app.App, fakeIn) (any, error) { ran = append(ran, name); return "ok", nil }}
	}
	inv := command.NewInvoker(command.Options{Stages: []command.Stage{st}})
	run := func(name, origin string) error {
		_, err := command.Invoke(context.Background(), inv, nil, spec(name), fakeIn{}, command.Origin{Source: origin})
		return err
	}
	var denied *plugin.DeniedError
	if err := run("push", "mcp:claude"); !errors.As(err, &denied) || denied.Plugin != "guard" || denied.Message != "No pushes from agents." || denied.Code() != plugin.DeniedCode {
		t.Fatalf("push from MCP: %v", err)
	}
	for _, name := range []string{"config.set", "approval.approve", "policy.explain", "plugin.remove"} {
		if err := run(name, "mcp:claude"); err != nil {
			t.Errorf("%s must never be denied: %v", name, err)
		}
	}
	if err := run("push", "cli"); err != nil {
		t.Fatalf("a rule that does not match must not deny: %v", err)
	}
	if err := run("commit", "mcp:claude"); err != nil {
		t.Fatalf("the disabled plugin's rule applied: %v", err)
	}
	if len(ran) != 6 {
		t.Fatalf("ran %v", ran)
	}
	// Enabling the second plugin applies its rule on the next call.
	layers.SetPersonal("off", plugin.PersonalPlugin{Enabled: true})
	if err := run("commit", "cli"); !errors.As(err, &denied) || denied.Plugin != "off" {
		t.Fatalf("new rule not applied: %v", err)
	}
	// An unreadable registry refuses rather than drops a deny, except for
	// the operations that turn a plugin off.
	ps, _ := src.Installed(context.Background())
	src.Err = errors.New("unreadable")
	src.Set(ps...)
	if err := run("status", "cli"); !errors.As(err, &denied) ||
		!strings.Contains(denied.Message, "/home/me/.agentflow/plugins/installed.json") || !strings.Contains(denied.Message, "agentflow plugin list") {
		t.Fatalf("unreadable registry: %v", err)
	}
	if err := run("config.set", "cli"); err != nil {
		t.Fatalf("config.set with unreadable rules: %v", err)
	}
	// An unreadable personal layer names config.yml.
	src.Err = nil
	src.Set(ps...)
	layers.Err = errors.New("yaml: bad")
	if err := run("status", "cli"); !errors.As(err, &denied) ||
		!strings.Contains(denied.Message, "/home/me/.agentflow/config.yml") || strings.Contains(denied.Message, "yaml: bad") {
		t.Fatalf("unreadable config: %v", err)
	}
}

// permissionFixture is one enabled declarative plugin "guard" with rule
// match m, run through a PermissionStage.
func permissionFixture(t *testing.T, m manifest.RuleMatch) (*plugintest.Source, *plugintest.Layers, func(a *app.App, name, action string) error) {
	t.Helper()
	src, layers := &plugintest.Source{}, &plugintest.Layers{}
	src.Set(manifest.Installed{ID: "guard", Tier: manifest.TierDeclarative, Manifest: manifest.Manifest{ID: "guard", Tier: manifest.TierDeclarative,
		Extensions: manifest.Extensions{PermissionRule: []manifest.PermissionRule{{Name: "r", Match: m, Deny: "no"}}}}})
	layers.SetPersonal("guard", plugin.PersonalPlugin{Enabled: true})
	st := &plugin.PermissionStage{Source: func() manifest.Source { return src }, Layers: func() plugin.Layers { return layers }}
	inv := command.NewInvoker(command.Options{Stages: []command.Stage{st}})
	return src, layers, func(a *app.App, name, action string) error {
		s := command.Spec[fakeIn, any]{Name: name, Policy: command.Policy{Class: policy.Local, Action: action},
			Run: func(context.Context, *app.App, fakeIn) (any, error) { return "ok", nil }}
		_, err := command.Invoke(context.Background(), inv, a, s, fakeIn{}, command.Origin{Source: "cli"})
		return err
	}
}

func TestBrokenRegistryBlocksNothingWhenNoPluginIsEnabled(t *testing.T) {
	src, layers, run := permissionFixture(t, manifest.RuleMatch{Operation: []string{"*"}})
	layers.SetPersonal("guard", plugin.PersonalPlugin{Enabled: false})
	ps, _ := src.Installed(context.Background())
	src.Err = errors.New("unreadable")
	src.Set(ps...)
	if err := run(nil, "status", ""); err != nil {
		t.Fatalf("a broken registry with nothing enabled denied a read: %v", err)
	}
	// Enabled, it fails closed.
	layers.SetPersonal("guard", plugin.PersonalPlugin{Enabled: true})
	var denied *plugin.DeniedError
	if err := run(nil, "status", ""); !errors.As(err, &denied) {
		t.Fatalf("a broken registry with a plugin enabled allowed: %v", err)
	}
	// A home that cannot be found fails closed too when a plugin is enabled,
	// and blocks nothing when none is.
	st := &plugin.PermissionStage{Source: func() manifest.Source { return nil }, Layers: func() plugin.Layers { return layers }}
	inv := command.NewInvoker(command.Options{Stages: []command.Stage{st}})
	s := command.Spec[fakeIn, any]{Name: "status", Policy: command.Policy{Class: policy.Read},
		Run: func(context.Context, *app.App, fakeIn) (any, error) { return "ok", nil }}
	if _, err := command.Invoke(context.Background(), inv, nil, s, fakeIn{}, command.Origin{Source: "cli"}); !errors.As(err, &denied) ||
		!strings.Contains(denied.Message, "agentflow home cannot be found") {
		t.Fatalf("no home with a plugin enabled: %v", err)
	}
	layers.SetPersonal("guard", plugin.PersonalPlugin{Enabled: false})
	if _, err := command.Invoke(context.Background(), inv, nil, s, fakeIn{}, command.Origin{Source: "cli"}); err != nil {
		t.Fatalf("no home with nothing enabled: %v", err)
	}
}

func TestBranchRuleMatchesTheBranchBeingWritten(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	repo, err := gitx.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{Repo: repo}
	_, _, run := permissionFixture(t, manifest.RuleMatch{Branch: []string{"main"}})
	var denied *plugin.DeniedError
	for _, op := range [][2]string{{"commit", "commit"}, {"push", "push"}, {"pr.open", "pr_open"}} {
		if err := run(a, op[0], op[1]); !errors.As(err, &denied) {
			t.Errorf("%s on main was not denied: %v", op[0], err)
		}
		// The branch cannot be determined: the condition matches.
		if err := run(nil, op[0], op[1]); !errors.As(err, &denied) {
			t.Errorf("%s with an unknown branch was not denied: %v", op[0], err)
		}
	}
	// An operation that writes no branch is never matched, even by "*".
	_, _, runAny := permissionFixture(t, manifest.RuleMatch{Branch: []string{"*"}})
	if err := runAny(a, "notify", "notify"); err != nil {
		t.Fatalf("a branch rule matched an operation with no branch: %v", err)
	}
	// Another branch is not matched.
	cmd := exec.Command("git", "checkout", "-q", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := run(a, "push", "push"); err != nil {
		t.Fatalf("push on feature was denied by a main rule: %v", err)
	}
}

// TestPluginsDocRuleDeniesAReleasePush runs the permission-rule example of
// docs/plugins.md: it denies a push from a release/* branch and nothing
// else.
func TestPluginsDocRuleDeniesAReleasePush(t *testing.T) {
	b, err := os.ReadFile("../../docs/plugins.md")
	if err != nil {
		t.Fatal(err)
	}
	var block string
	for _, m := range regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(string(b), -1) {
		if strings.HasPrefix(m[1], "extensions:") && strings.Contains(m[1], "permission_rule") {
			block = m[1]
		}
	}
	if block == "" {
		t.Fatal("no permission rule example in docs/plugins.md")
	}
	m, err := manifest.Load([]byte("schema: 1\nid: pause-pushes\nname: Pause pushes\nversion: 0.1.0\ntier: declarative\n" + block))
	if err != nil {
		t.Fatal(err)
	}
	src, layers := &plugintest.Source{}, &plugintest.Layers{}
	src.Set(manifest.Installed{ID: m.ID, Tier: m.Tier, Manifest: m})
	layers.SetPersonal(m.ID, plugin.PersonalPlugin{Enabled: true})
	st := &plugin.PermissionStage{Source: func() manifest.Source { return src }, Layers: func() plugin.Layers { return layers }}
	inv := command.NewInvoker(command.Options{Stages: []command.Stage{st}})

	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "init")
	repo, err := gitx.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{Repo: repo}
	push := func() error {
		s := command.Spec[fakeIn, any]{Name: "push", Policy: command.Policy{Class: policy.Remote, Action: "push"},
			Run: func(context.Context, *app.App, fakeIn) (any, error) { return "ok", nil }}
		_, err := command.Invoke(context.Background(), inv, a, s, fakeIn{}, command.Origin{Source: "cli"})
		return err
	}
	if err := push(); err != nil {
		t.Fatalf("push from main denied: %v", err)
	}
	git("checkout", "-q", "-b", "release/1.4")
	var denied *plugin.DeniedError
	if err := push(); !errors.As(err, &denied) || denied.Plugin != "pause-pushes" || !strings.HasPrefix(denied.Message, "Pushes to release branches are paused.") {
		t.Fatalf("push from release/1.4: %v", err)
	}
}
