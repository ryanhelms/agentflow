package manifest_test

import (
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

func TestSamplesValidate(t *testing.T) {
	for name, text := range map[string]string{"ci-notify": ciNotify, "timeline-hints": timelineHints} {
		if _, err := manifest.Load([]byte(text)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestParseIsStrict(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"unknown key":      {ciNotify + "colour: red\n", "unknown key colour"},
		"unknown nested":   {strings.Replace(ciNotify, "  invoke: [notify]\n", "  invoke: [notify]\n  admin: [all]\n", 1), "unknown key admin"},
		"wrong type":       {strings.Replace(ciNotify, "schema: 1", "schema: one", 1), "cannot unmarshal"},
		"two documents":    {ciNotify + "---\n" + ciNotify, "more than one YAML document"},
		"empty":            {"", "empty"},
		"comments only":    {"# nothing\n", "empty"},
		"not yaml":         {"id: [unclosed", ""},
		"too large":        {ciNotify + "# " + strings.Repeat("x", manifest.MaxManifestBytes), "larger than"},
		"sensitive widget": {strings.Replace(ciNotify, "    label:", "    sensitive: true\n    label:", 1), "unknown key sensitive"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := manifest.Parse([]byte(tc.text))
			ps := problems(t, err)
			if len(ps) == 0 {
				t.Fatal("Parse accepted it")
			}
			if !strings.Contains(ps[0].Message, tc.want) {
				t.Errorf("message %q does not contain %q", ps[0].Message, tc.want)
			}
			if strings.Contains(ps[0].Message, "manifest.Manifest") || strings.Contains(ps[0].Message, "type ") {
				t.Errorf("message names a Go type: %q", ps[0].Message)
			}
		})
	}
}

func TestValidateRefusesWithCodes(t *testing.T) {
	grant := func(m *manifest.Manifest, subscribe ...string) { m.Permissions.Subscribe = subscribe }
	handler := func(m *manifest.Manifest) *manifest.Handler { return &m.Extensions.Handler[0] }
	cases := []struct {
		name string
		base string
		edit func(m *manifest.Manifest)
		code string
		path string
	}{
		{"missing schema", ciNotify, func(m *manifest.Manifest) { m.Schema = 0 }, manifest.CodeRequired, "schema"},
		{"future schema", ciNotify, func(m *manifest.Manifest) { m.Schema = 2 }, manifest.CodeSyntax, "schema"},
		{"id upper case", ciNotify, func(m *manifest.Manifest) { m.ID = "CI-notify" }, manifest.CodeID, "id"},
		{"id one character", ciNotify, func(m *manifest.Manifest) { m.ID = "a" }, manifest.CodeID, "id"},
		{"id ends with hyphen", ciNotify, func(m *manifest.Manifest) { m.ID = "notify-" }, manifest.CodeID, "id"},
		{"id starts with digit", ciNotify, func(m *manifest.Manifest) { m.ID = "1notify" }, manifest.CodeID, "id"},
		{"id too long", ciNotify, func(m *manifest.Manifest) { m.ID = strings.Repeat("a", 41) }, manifest.CodeID, "id"},
		{"id with dot", ciNotify, func(m *manifest.Manifest) { m.ID = "ci.notify" }, manifest.CodeID, "id"},
		{"id agentflow", ciNotify, func(m *manifest.Manifest) { m.ID = "agentflow" }, manifest.CodeID, "id"},
		{"id builtin", ciNotify, func(m *manifest.Manifest) { m.ID = "builtin" }, manifest.CodeID, "id"},
		{"id daemon", ciNotify, func(m *manifest.Manifest) { m.ID = "daemon" }, manifest.CodeID, "id"},
		{"id cli", ciNotify, func(m *manifest.Manifest) { m.ID = "cli" }, manifest.CodeID, "id"},
		{"id mcp", ciNotify, func(m *manifest.Manifest) { m.ID = "mcp" }, manifest.CodeID, "id"},
		{"id agentflow prefix", ciNotify, func(m *manifest.Manifest) { m.ID = "agentflow-ci" }, manifest.CodeID, "id"},
		{"no name", ciNotify, func(m *manifest.Manifest) { m.Name = " " }, manifest.CodeRequired, "name"},
		{"long name", ciNotify, func(m *manifest.Manifest) { m.Name = strings.Repeat("n", 65) }, manifest.CodeLimit, "name"},
		{"name with newline", ciNotify, func(m *manifest.Manifest) { m.Name = "a\nb" }, manifest.CodeSyntax, "name"},
		{"long description", ciNotify, func(m *manifest.Manifest) { m.Description = strings.Repeat("d", 513) }, manifest.CodeLimit, "description"},
		{"version with v", ciNotify, func(m *manifest.Manifest) { m.Version = "v1.0.0" }, manifest.CodeVersion, "version"},
		{"version two parts", ciNotify, func(m *manifest.Manifest) { m.Version = "1.0" }, manifest.CodeVersion, "version"},
		{"version leading zero", ciNotify, func(m *manifest.Manifest) { m.Version = "01.0.0" }, manifest.CodeVersion, "version"},
		{"no tier", ciNotify, func(m *manifest.Manifest) { m.Tier = "" }, manifest.CodeRequired, "tier"},
		{"builtin in a file", ciNotify, func(m *manifest.Manifest) { m.Tier = manifest.TierBuiltin }, manifest.CodeTier, "tier"},
		{"unknown tier", ciNotify, func(m *manifest.Manifest) { m.Tier = "wasm" }, manifest.CodeTier, "tier"},
		{"declarative with exec", ciNotify, func(m *manifest.Manifest) { m.Exec = &manifest.Exec{Command: "run"} }, manifest.CodeExec, "exec"},
		{"process without exec", timelineHints, func(m *manifest.Manifest) { m.Exec = nil }, manifest.CodeExec, "exec.command"},
		{"exec parent directory", timelineHints, func(m *manifest.Manifest) { m.Exec.Command = "../bin/run" }, manifest.CodeExec, "exec.command"},
		{"exec nested parent", timelineHints, func(m *manifest.Manifest) { m.Exec.Command = "bin/../../run" }, manifest.CodeExec, "exec.command"},
		{"exec absolute", timelineHints, func(m *manifest.Manifest) { m.Exec.Command = "/usr/bin/env" }, manifest.CodeExec, "exec.command"},
		{"exec backslash", timelineHints, func(m *manifest.Manifest) { m.Exec.Command = `bin\run` }, manifest.CodeExec, "exec.command"},
		{"exec drive", timelineHints, func(m *manifest.Manifest) { m.Exec.Command = `C:run` }, manifest.CodeExec, "exec.command"},
		{"exec directory", timelineHints, func(m *manifest.Manifest) { m.Exec.Command = "bin/" }, manifest.CodeExec, "exec.command"},
		{"exec dot", timelineHints, func(m *manifest.Manifest) { m.Exec.Command = "." }, manifest.CodeExec, "exec.command"},
		{"exec too many args", timelineHints, func(m *manifest.Manifest) { m.Exec.Args = make([]string, 17) }, manifest.CodeLimit, "exec.args"},
		{"reserved harness", ciNotify, func(m *manifest.Manifest) { m.Extensions.Harness = []map[string]any{{"name": "x"}} }, manifest.CodeReserved, "extensions.harness"},
		{"reserved provider", ciNotify, func(m *manifest.Manifest) { m.Extensions.Provider = []map[string]any{{}} }, manifest.CodeReserved, "extensions.provider"},
		{"reserved source", ciNotify, func(m *manifest.Manifest) { m.Extensions.Source = []map[string]any{{}} }, manifest.CodeReserved, "extensions.source"},
		{"reserved sink", ciNotify, func(m *manifest.Manifest) { m.Extensions.Sink = []map[string]any{{}} }, manifest.CodeReserved, "extensions.sink"},
		{"reserved widget", ciNotify, func(m *manifest.Manifest) { m.Extensions.Widget = []map[string]any{{}} }, manifest.CodeReserved, "extensions.widget"},
		{"no extension", ciNotify, func(m *manifest.Manifest) { m.Extensions.Handler = nil }, manifest.CodeRequired, "extensions"},
		{"handler name", ciNotify, func(m *manifest.Manifest) { handler(m).Name = "Ci Failed" }, manifest.CodeID, "extensions.handler[0].name"},
		{"duplicate handler", ciNotify, func(m *manifest.Manifest) {
			m.Extensions.Handler = append(m.Extensions.Handler, m.Extensions.Handler[0])
		}, manifest.CodeID, "extensions.handler[1].name"},
		{"too many handlers", ciNotify, func(m *manifest.Manifest) {
			m.Extensions.Handler = make([]manifest.Handler, manifest.MaxHandlers+1)
		}, manifest.CodeLimit, "extensions.handler"},
		{"handler without on", ciNotify, func(m *manifest.Manifest) { handler(m).On = nil }, manifest.CodeRequired, "extensions.handler[0].on"},
		{"handler on not granted", ciNotify, func(m *manifest.Manifest) { handler(m).On = []string{"git.head.changed"} }, manifest.CodeUngranted, "extensions.handler[0].on[0]"},
		{"handler on bad pattern", ciNotify, func(m *manifest.Manifest) { handler(m).On = []string{"GH.ci"} }, manifest.CodeSyntax, "extensions.handler[0].on[0]"},
		{"handler on approvals", ciNotify, func(m *manifest.Manifest) {
			handler(m).On = []string{"approval.requested"}
			grant(m, "approval.requested")
		}, manifest.CodePermission, "extensions.handler[0].on[0]"},
		{"handler on too many", ciNotify, func(m *manifest.Manifest) { handler(m).On = make([]string, 17) }, manifest.CodeLimit, "extensions.handler[0].on"},
		{"subscribe approvals", ciNotify, func(m *manifest.Manifest) { grant(m, "approval.*") }, manifest.CodePermission, "permissions.subscribe[0]"},
		{"subscribe everything", ciNotify, func(m *manifest.Manifest) { grant(m, "**") }, manifest.CodePermission, "permissions.subscribe[0]"},
		{"subscribe wildcard family", ciNotify, func(m *manifest.Manifest) { grant(m, "*.ci.updated") }, manifest.CodePermission, "permissions.subscribe[0]"},
		{"subscribe client", ciNotify, func(m *manifest.Manifest) { grant(m, "client.settings.changed") }, manifest.CodePermission, "permissions.subscribe[0]"},
		{"subscribe ui", ciNotify, func(m *manifest.Manifest) { grant(m, "ui.**") }, manifest.CodePermission, "permissions.subscribe[0]"},
		{"subscribe lifecycle", ciNotify, func(m *manifest.Manifest) { grant(m, "plugin.lifecycle.changed") }, manifest.CodePermission, "permissions.subscribe[0]"},
		{"subscribe delivery", ciNotify, func(m *manifest.Manifest) { grant(m, "plugin.delivery.failed") }, manifest.CodePermission, "permissions.subscribe[0]"},
		{"subscribe stream", ciNotify, func(m *manifest.Manifest) { grant(m, "stream.overflow") }, manifest.CodePermission, "permissions.subscribe[0]"},
		{"subscribe unknown type", ciNotify, func(m *manifest.Manifest) { grant(m, "gh.ci.updatd") }, manifest.CodePermission, "permissions.subscribe[0]"},
		{"subscribe invalid", ciNotify, func(m *manifest.Manifest) { grant(m, "gh..x") }, manifest.CodeSyntax, "permissions.subscribe[0]"},
		{"subscribe too many", ciNotify, func(m *manifest.Manifest) { m.Permissions.Subscribe = make([]string, 17) }, manifest.CodeLimit, "permissions.subscribe"},
		{"publish op", timelineHints, func(m *manifest.Manifest) { m.Permissions.Publish = []string{"op.applied"} }, manifest.CodePermission, "permissions.publish[0]"},
		{"publish approval", timelineHints, func(m *manifest.Manifest) { m.Permissions.Publish = []string{"approval.resolved"} }, manifest.CodePermission, "permissions.publish[0]"},
		{"invoke approve", ciNotify, func(m *manifest.Manifest) { m.Permissions.Invoke = []string{"approval.approve"} }, manifest.CodePermission, "permissions.invoke[0]"},
		{"invoke commit", ciNotify, func(m *manifest.Manifest) { m.Permissions.Invoke = []string{"commit"} }, manifest.CodePermission, "permissions.invoke[0]"},
		{"invoke config", ciNotify, func(m *manifest.Manifest) { m.Permissions.Invoke = []string{"config.set"} }, manifest.CodePermission, "permissions.invoke[0]"},
		{"invoke plugin add", ciNotify, func(m *manifest.Manifest) { m.Permissions.Invoke = []string{"plugin.add"} }, manifest.CodePermission, "permissions.invoke[0]"},
		{"invoke not granted", ciNotify, func(m *manifest.Manifest) { m.Permissions.Invoke = nil }, manifest.CodeUngranted, "extensions.handler[0].actions[0].invoke.command"},
		{"invoke command empty", ciNotify, func(m *manifest.Manifest) { handler(m).Actions[0].Invoke.Command = "" }, manifest.CodeRequired, "extensions.handler[0].actions[0].invoke.command"},
		{"invoke outside allow-list", ciNotify, func(m *manifest.Manifest) {
			handler(m).Actions[0].Invoke.Command = "push"
			m.Permissions.Invoke = []string{"notify"}
		}, manifest.CodePermission, "extensions.handler[0].actions[0].invoke.command"},
		{"invoke confirm token", ciNotify, func(m *manifest.Manifest) {
			handler(m).Actions[0].Invoke.Input["confirm_token"] = "cf_x"
		}, manifest.CodeAction, "extensions.handler[0].actions[0].invoke.input.confirm_token"},
		{"declarative handler without actions", ciNotify, func(m *manifest.Manifest) { handler(m).Actions = nil }, manifest.CodeAction, "extensions.handler[0].actions"},
		{"too many actions", ciNotify, func(m *manifest.Manifest) {
			a := handler(m).Actions[0]
			handler(m).Actions = []manifest.Action{a, a, a, a, a}
		}, manifest.CodeLimit, "extensions.handler[0].actions"},
		{"empty action", ciNotify, func(m *manifest.Manifest) { handler(m).Actions = []manifest.Action{{}} }, manifest.CodeAction, "extensions.handler[0].actions[0]"},
		{"two kinds of action", ciNotify, func(m *manifest.Manifest) {
			handler(m).Actions[0].Annotate = &manifest.AnnotateAction{Text: "x"}
		}, manifest.CodeAction, "extensions.handler[0].actions[0]"},
		{"annotate on gh event", ciNotify, func(m *manifest.Manifest) {
			m.Permissions.Publish = []string{manifest.AnnotateType}
			handler(m).Actions = []manifest.Action{{Annotate: &manifest.AnnotateAction{Text: "x"}}}
		}, manifest.CodeAction, "extensions.handler[0].actions[0].annotate"},
		{"annotate not granted", ciNotify, func(m *manifest.Manifest) {
			grant(m, "op.failed")
			handler(m).On = []string{"op.failed"}
			handler(m).Actions = []manifest.Action{{Annotate: &manifest.AnnotateAction{Text: "x"}}}
		}, manifest.CodeUngranted, "extensions.handler[0].actions[0].annotate"},
		{"annotate http url", ciNotify, func(m *manifest.Manifest) {
			grant(m, "op.failed")
			m.Permissions.Publish = []string{manifest.AnnotateType}
			handler(m).On = []string{"op.failed"}
			handler(m).Actions = []manifest.Action{{Annotate: &manifest.AnnotateAction{Text: "x", URL: "http://example.org"}}}
		}, manifest.CodeAction, "extensions.handler[0].actions[0].annotate.url"},
		{"annotate bad level", ciNotify, func(m *manifest.Manifest) {
			grant(m, "op.failed")
			m.Permissions.Publish = []string{manifest.AnnotateType}
			handler(m).On = []string{"op.failed"}
			handler(m).Actions = []manifest.Action{{Annotate: &manifest.AnnotateAction{Text: "x", Level: "error"}}}
		}, manifest.CodeAction, "extensions.handler[0].actions[0].annotate.level"},
		{"annotate without text", ciNotify, func(m *manifest.Manifest) {
			grant(m, "op.failed")
			m.Permissions.Publish = []string{manifest.AnnotateType}
			handler(m).On = []string{"op.failed"}
			handler(m).Actions = []manifest.Action{{Annotate: &manifest.AnnotateAction{}}}
		}, manifest.CodeRequired, "extensions.handler[0].actions[0].annotate.text"},
		{"condition without operator", ciNotify, func(m *manifest.Manifest) { handler(m).When = []manifest.Condition{{Field: "type"}} }, manifest.CodeCondition, "extensions.handler[0].when[0]"},
		{"condition with two operators", ciNotify, func(m *manifest.Manifest) {
			yes := true
			handler(m).When = []manifest.Condition{{Field: "type", Equals: "x", Exists: &yes}}
		}, manifest.CodeCondition, "extensions.handler[0].when[0]"},
		{"condition field", ciNotify, func(m *manifest.Manifest) {
			handler(m).When = []manifest.Condition{{Field: "id", Equals: "x"}}
		}, manifest.CodeCondition, "extensions.handler[0].when[0].field"},
		{"condition payload key", ciNotify, func(m *manifest.Manifest) {
			handler(m).When = []manifest.Condition{{Field: "payload..x", Equals: "x"}}
		}, manifest.CodeCondition, "extensions.handler[0].when[0].field"},
		{"condition operand", ciNotify, func(m *manifest.Manifest) {
			handler(m).When = []manifest.Condition{{Field: "type", Equals: []any{"x"}}}
		}, manifest.CodeCondition, "extensions.handler[0].when[0].equals"},
		{"condition empty in", ciNotify, func(m *manifest.Manifest) {
			handler(m).When = []manifest.Condition{{Field: "type", In: []any{}}}
		}, manifest.CodeCondition, "extensions.handler[0].when[0].in"},
		{"too many conditions", ciNotify, func(m *manifest.Manifest) {
			c := manifest.Condition{Field: "type", Equals: "x"}
			handler(m).When = make([]manifest.Condition, 17)
			for i := range handler(m).When {
				handler(m).When[i] = c
			}
		}, manifest.CodeLimit, "extensions.handler[0].when"},
		{"template unknown", ciNotify, func(m *manifest.Manifest) {
			handler(m).Actions[0].Invoke.Input["title"] = "{{env.HOME}}"
		}, manifest.CodeTemplate, "extensions.handler[0].actions[0].invoke.input.title"},
		{"template unknown event field", ciNotify, func(m *manifest.Manifest) {
			handler(m).Actions[0].Invoke.Input["title"] = "{{event.id}}"
		}, manifest.CodeTemplate, "extensions.handler[0].actions[0].invoke.input.title"},
		{"template with spaces", ciNotify, func(m *manifest.Manifest) {
			handler(m).Actions[0].Invoke.Input["title"] = "{{ event.type }}"
		}, manifest.CodeTemplate, "extensions.handler[0].actions[0].invoke.input.title"},
		{"template unclosed", ciNotify, func(m *manifest.Manifest) {
			handler(m).Actions[0].Invoke.Input["title"] = "{{event.type"
		}, manifest.CodeTemplate, "extensions.handler[0].actions[0].invoke.input.title"},
		{"template setting undeclared", ciNotify, func(m *manifest.Manifest) {
			handler(m).Actions[0].Invoke.Input["title"] = "{{settings.other}}"
		}, manifest.CodeTemplate, "extensions.handler[0].actions[0].invoke.input.title"},
		{"template in a list", ciNotify, func(m *manifest.Manifest) {
			handler(m).Actions[0].Invoke.Input["links"] = []any{map[string]any{"url": "{{nope.x}}"}}
		}, manifest.CodeTemplate, "extensions.handler[0].actions[0].invoke.input.links[0].url"},
		{"process handler with actions", timelineHints, func(m *manifest.Manifest) {
			m.Extensions.Handler[0].Actions = []manifest.Action{{Annotate: &manifest.AnnotateAction{Text: "x"}}}
		}, manifest.CodeRule, "extensions.handler[0]"},
		{"process handler with when", timelineHints, func(m *manifest.Manifest) {
			m.Extensions.Handler[0].When = []manifest.Condition{{Field: "type", Equals: "op.failed"}}
		}, manifest.CodeRule, "extensions.handler[0]"},
		{"process with rule", timelineHints, func(m *manifest.Manifest) {
			m.Extensions.PermissionRule = []manifest.PermissionRule{{Name: "no-push", Match: manifest.RuleMatch{Operation: []string{"push"}}, Deny: "no"}}
		}, manifest.CodeRule, "extensions.permission_rule[0]"},
		{"rule without match", ciNotify, func(m *manifest.Manifest) {
			m.Extensions.PermissionRule = []manifest.PermissionRule{{Name: "none", Deny: "no"}}
		}, manifest.CodeRule, "extensions.permission_rule[0].match"},
		{"rule bad glob", ciNotify, func(m *manifest.Manifest) {
			m.Extensions.PermissionRule = []manifest.PermissionRule{{Name: "bad", Match: manifest.RuleMatch{Operation: []string{"[x"}}, Deny: "no"}}
		}, manifest.CodeRule, "extensions.permission_rule[0].match.operation[0]"},
		{"rule without message", ciNotify, func(m *manifest.Manifest) {
			m.Extensions.PermissionRule = []manifest.PermissionRule{{Name: "quiet", Match: manifest.RuleMatch{Operation: []string{"push"}}}}
		}, manifest.CodeRequired, "extensions.permission_rule[0].deny"},
		{"rule message too long", ciNotify, func(m *manifest.Manifest) {
			m.Extensions.PermissionRule = []manifest.PermissionRule{{Name: "long", Match: manifest.RuleMatch{Operation: []string{"push"}}, Deny: strings.Repeat("d", 257)}}
		}, manifest.CodeLimit, "extensions.permission_rule[0].deny"},
		{"rule duplicate", ciNotify, func(m *manifest.Manifest) {
			r := manifest.PermissionRule{Name: "same", Match: manifest.RuleMatch{Operation: []string{"push"}}, Deny: "no"}
			m.Extensions.PermissionRule = []manifest.PermissionRule{r, r}
		}, manifest.CodeID, "extensions.permission_rule[1].name"},
		{"too many rules", ciNotify, func(m *manifest.Manifest) {
			m.Extensions.PermissionRule = make([]manifest.PermissionRule, manifest.MaxRules+1)
		}, manifest.CodeLimit, "extensions.permission_rule"},
		{"too many settings", ciNotify, func(m *manifest.Manifest) { m.Settings = make([]manifest.Setting, manifest.MaxSettings+1) }, manifest.CodeLimit, "settings"},
		{"duplicate setting", ciNotify, func(m *manifest.Manifest) {
			s := m.Settings[0]
			s.Label = "Another"
			m.Settings = append(m.Settings, s)
		}, manifest.CodeSetting, "settings[1].key"},
		{"setting label taken", ciNotify, func(m *manifest.Manifest) {
			s := m.Settings[0]
			s.Key = "other"
			m.Settings = append(m.Settings, s)
		}, manifest.CodeSetting, "settings[1].label"},
		{"setting label enabled", ciNotify, func(m *manifest.Manifest) { m.Settings[0].Label = "Enabled" }, manifest.CodeSetting, "settings[0].label"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mustLoad(t, tc.base)
			tc.edit(&m)
			ps := problems(t, manifest.Validate(m))
			if !has(ps, tc.code, tc.path) {
				t.Errorf("want %s at %s, got:%s", tc.code, tc.path, dump(ps))
			}
		})
	}
}

func TestValidateSettings(t *testing.T) {
	one := 1
	ten := 10
	cases := []struct {
		name string
		s    manifest.Setting
		code string
		path string // relative to settings[0]; "" means valid
	}{
		{"boolean", manifest.Setting{Key: "loud", Type: manifest.SettingBoolean, Default: true, Label: "Loud", Description: "d"}, "", ""},
		{"string", manifest.Setting{Key: "name", Type: manifest.SettingString, Default: "x", Label: "Name", Description: "d"}, "", ""},
		{"string with enum", manifest.Setting{Key: "mode", Type: manifest.SettingString, Enum: []string{"a", "b"}, Default: "a", Label: "Mode", Description: "d"}, "", ""},
		{"duration", manifest.Setting{Key: "wait", Type: manifest.SettingString, Widget: config.WidgetDuration, Default: "5m", MinDuration: "1m", MaxDuration: "1h", Label: "Wait", Description: "d"}, "", ""},
		{"integer", manifest.Setting{Key: "count", Type: manifest.SettingInteger, Min: &one, Max: &ten, Default: 3, Label: "Count", Description: "d"}, "", ""},
		{"list", manifest.Setting{Key: "tags", Type: manifest.SettingStringList, Default: []any{"a"}, Label: "Tags", Description: "d"}, "", ""},
		{"restart", manifest.Setting{Key: "port", Type: manifest.SettingString, Applies: config.AppliesRestart, Label: "Port", Description: "d"}, "", ""},
		{"bad key", manifest.Setting{Key: "Bad-Key", Type: manifest.SettingBoolean, Label: "L", Description: "d"}, manifest.CodeSetting, "key"},
		{"no label", manifest.Setting{Key: "x", Type: manifest.SettingBoolean, Description: "d"}, manifest.CodeRequired, "label"},
		{"no description", manifest.Setting{Key: "x", Type: manifest.SettingBoolean, Label: "L"}, manifest.CodeRequired, "description"},
		{"unknown type", manifest.Setting{Key: "x", Type: "float", Label: "L", Description: "d"}, manifest.CodeSetting, "type"},
		{"credential widget", manifest.Setting{Key: "token", Type: manifest.SettingString, Widget: config.WidgetCredentialRef, Label: "L", Description: "d"}, manifest.CodeSetting, "widget"},
		{"file widget", manifest.Setting{Key: "path", Type: manifest.SettingString, Widget: config.WidgetFilePicker, Label: "L", Description: "d"}, manifest.CodeSetting, "widget"},
		{"branch widget", manifest.Setting{Key: "branch", Type: manifest.SettingString, Widget: config.WidgetBranchPicker, Label: "L", Description: "d"}, manifest.CodeSetting, "widget"},
		{"provider widget", manifest.Setting{Key: "p", Type: manifest.SettingString, Widget: config.WidgetProviderPicker, Label: "L", Description: "d"}, manifest.CodeSetting, "widget"},
		{"kv widget", manifest.Setting{Key: "kv", Type: manifest.SettingString, Widget: config.WidgetKVTable, Label: "L", Description: "d"}, manifest.CodeSetting, "widget"},
		{"list widget", manifest.Setting{Key: "rows", Type: manifest.SettingStringList, Widget: config.WidgetList, Label: "L", Description: "d"}, manifest.CodeSetting, "widget"},
		{"widget for another type", manifest.Setting{Key: "x", Type: manifest.SettingBoolean, Widget: config.WidgetText, Label: "L", Description: "d"}, manifest.CodeSetting, "widget"},
		{"select without enum", manifest.Setting{Key: "x", Type: manifest.SettingString, Widget: config.WidgetSelect, Label: "L", Description: "d"}, manifest.CodeSetting, "widget"},
		{"enum on integer", manifest.Setting{Key: "x", Type: manifest.SettingInteger, Min: &one, Enum: []string{"a"}, Label: "L", Description: "d"}, manifest.CodeSetting, "enum"},
		{"enum twice", manifest.Setting{Key: "x", Type: manifest.SettingString, Enum: []string{"a", "a"}, Label: "L", Description: "d"}, manifest.CodeSetting, "enum"},
		{"integer without min", manifest.Setting{Key: "x", Type: manifest.SettingInteger, Label: "L", Description: "d"}, manifest.CodeSetting, "min"},
		{"max below min", manifest.Setting{Key: "x", Type: manifest.SettingInteger, Min: &ten, Max: &one, Label: "L", Description: "d"}, manifest.CodeSetting, "max"},
		{"min on string", manifest.Setting{Key: "x", Type: manifest.SettingString, Min: &one, Label: "L", Description: "d"}, manifest.CodeSetting, "min"},
		{"duration limits on text", manifest.Setting{Key: "x", Type: manifest.SettingString, MinDuration: "1m", Label: "L", Description: "d"}, manifest.CodeSetting, "min_duration"},
		{"bad duration", manifest.Setting{Key: "x", Type: manifest.SettingString, Widget: config.WidgetDuration, MinDuration: "soon", Label: "L", Description: "d"}, manifest.CodeSetting, "min_duration"},
		{"duration order", manifest.Setting{Key: "x", Type: manifest.SettingString, Widget: config.WidgetDuration, MinDuration: "1h", MaxDuration: "1m", Label: "L", Description: "d"}, manifest.CodeSetting, "max_duration"},
		{"next-run applies", manifest.Setting{Key: "x", Type: manifest.SettingBoolean, Applies: config.AppliesNextRun, Label: "L", Description: "d"}, manifest.CodeSetting, "applies"},
		{"default wrong type", manifest.Setting{Key: "x", Type: manifest.SettingBoolean, Default: "yes", Label: "L", Description: "d"}, manifest.CodeSetting, "default"},
		{"default not in enum", manifest.Setting{Key: "x", Type: manifest.SettingString, Enum: []string{"a"}, Default: "b", Label: "L", Description: "d"}, manifest.CodeSetting, "default"},
		{"default above max", manifest.Setting{Key: "x", Type: manifest.SettingInteger, Min: &one, Max: &ten, Default: 11, Label: "L", Description: "d"}, manifest.CodeSetting, "default"},
		{"default fraction", manifest.Setting{Key: "x", Type: manifest.SettingInteger, Min: &one, Default: 1.5, Label: "L", Description: "d"}, manifest.CodeSetting, "default"},
		{"default list of numbers", manifest.Setting{Key: "x", Type: manifest.SettingStringList, Default: []any{1}, Label: "L", Description: "d"}, manifest.CodeSetting, "default"},
		{"default duration out of range", manifest.Setting{Key: "x", Type: manifest.SettingString, Widget: config.WidgetDuration, Default: "5s", MinDuration: "1m", Label: "L", Description: "d"}, manifest.CodeSetting, "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mustLoad(t, ciNotify)
			m.Settings = []manifest.Setting{tc.s}
			// The sample's template names the setting "signal".
			m.Extensions.Handler[0].Actions[0].Invoke.Input["signal"] = "x"
			ps := problems(t, manifest.Validate(m))
			if tc.code == "" {
				if len(ps) != 0 {
					t.Errorf("a valid setting was refused:%s", dump(ps))
				}
				return
			}
			if !has(ps, tc.code, "settings[0]."+tc.path) {
				t.Errorf("want %s at settings[0].%s, got:%s", tc.code, tc.path, dump(ps))
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	m := mustLoad(t, ciNotify)
	m.ID, m.Version, m.Name = "Bad", "x", ""
	err := manifest.Validate(m)
	ps := problems(t, err)
	if len(ps) != 3 {
		t.Errorf("want 3 problems, got:%s", dump(ps))
	}
	if err.Error() != "invalid plugin manifest" {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestOneProblemErrorNamesIt(t *testing.T) {
	m := mustLoad(t, ciNotify)
	m.Version = "x"
	err := manifest.Validate(m)
	if !strings.Contains(err.Error(), "semantic version") {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestCovers(t *testing.T) {
	for _, tc := range []struct {
		granted, wanted string
		want            bool
	}{
		{"gh.ci.updated", "gh.ci.updated", true},
		{"gh.ci.updated", "gh.ci.*", false},
		{"gh.ci.*", "gh.ci.updated", true},
		{"gh.ci.*", "gh.ci.*", true},
		{"gh.*.updated", "gh.ci.updated", true},
		{"gh.**", "gh.ci.updated", true},
		{"gh.**", "gh.ci.*", true},
		{"gh.**", "gh.**", true},
		{"gh.**", "gh", false},
		{"gh.**", "git.head.changed", false},
		{"gh.*", "gh.ci.updated", false},
		{"gh.*", "gh.**", false},
		{"op.**", "op.failed", true},
		{"op.failed", "op.**", false},
		{"**", "gh.ci.updated", true},
		{"git.head.changed", "git.head.changed.more", false},
		{"git.head.changed.more", "git.head.changed", false},
	} {
		if got := manifest.Covers(tc.granted, tc.wanted); got != tc.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", tc.granted, tc.wanted, got, tc.want)
		}
	}
}

func TestSubscribable(t *testing.T) {
	for p, want := range map[string]bool{
		"git.**": true, "config.layer.changed": true, "gh.ci.updated": true, "op.failed": true,
		"commit.created": true, "hook.ran": true, "plugin.annotation.added": true,
		"approval.requested": false, "approval.**": false, "client.settings.changed": false, "ui.**": false,
		"plugin.lifecycle.changed": false, "plugin.**": false, "stream.overflow": false, "**": false, "*.x.y": false, "git": false,
	} {
		if got := manifest.Subscribable(p); got != want {
			t.Errorf("Subscribable(%q) = %v, want %v", p, got, want)
		}
	}
}
