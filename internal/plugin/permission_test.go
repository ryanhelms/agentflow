package plugin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
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
	st := &plugin.PermissionStage{Source: func() manifest.Source { return src }, Layers: func() plugin.Layers { return layers }}
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
	if err := run("status", "cli"); !errors.As(err, &denied) {
		t.Fatalf("unreadable rules: %v", err)
	}
	if err := run("config.set", "cli"); err != nil {
		t.Fatalf("config.set with unreadable rules: %v", err)
	}
}
