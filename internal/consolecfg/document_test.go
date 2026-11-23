package consolecfg

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"gopkg.in/yaml.v3"
)

func at(t *testing.T, s fieldSpec, path ...string) fieldSpec {
	t.Helper()
	for _, k := range path {
		switch {
		case k == "*" && s.Items != nil:
			s = *s.Items
		case k == "{}" && s.AdditionalProperties != nil:
			s = *s.AdditionalProperties
		default:
			var ok bool
			s, ok = s.Properties[k]
			if !ok {
				t.Fatalf("missing %s in %v", k, path)
			}
		}
	}
	return s
}

func TestDescribeEveryConfigurationFormat(t *testing.T) {
	root, _ := fixture(t)
	src := map[string]string{
		MainFile:                       "devflow: 1\n",
		definitionsDir + "/custom.yml": "version: 1\nproviders: {}\n",
		".devflow/releases.yml":        "strategy: trunk\nunits: []\n",
		"flow-map.yml":                 "defaults: {}\n",
		".devflow/task-templates.json": "[]",
		"docs/confluence.yml":          "space: EX\npages: []\n",
		".jira/config.yml":             "deployments: {environmentMapping: {}}\n",
	}
	for path, content := range src {
		put(t, root, path, content)
	}
	describe := func(name, content string) Document {
		t.Helper()
		doc, err := DescribeDocument(context.Background(), root, name, content, digest(src[name]))
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	main := describe(MainFile, src[MainFile])
	if at(t, main.Schema, "devops", "checks", "provider").Default != "github" || !slices.Contains(at(t, main.Schema, "commit", "types", "*").Suggestions, "feat") || at(t, main.Schema, "commit", "require_key").Type != "boolean" {
		t.Fatal("missing main fields")
	}
	if at(t, main.Schema, "task_management", "provider").Default != "none" || len(at(t, main.Schema, "task_management", "settings").Properties) != 0 {
		t.Fatal("unconfigured task management should have no provider settings")
	}
	main = describe(MainFile, "task_management: {provider: jira}\n")
	if at(t, main.Schema, "task_management", "settings", "projects", "*").Type != "string" || at(t, main.Schema, "task_management", "settings", "projects").Nullable {
		t.Fatal("schema should use selected Jira provider")
	}
	main = describe(MainFile, "task_management: {provider: github-issues, settings: {search: {limit: -12}}}\n")
	if !at(t, main.Schema, "task_management", "settings", "search", "limit").Nonnegative || main.Positions["/task_management/settings/search/limit"].Line != 1 {
		t.Fatal("candidate provider settings unavailable")
	}
	defs := describe(definitionsDir+"/custom.yml", src[definitionsDir+"/custom.yml"]).Schema
	if at(t, defs, "providers", "fixture", "devops.deployments", "settings", "path").Type != "string" || at(t, defs, "providers", "repository", "knowledge_base", "settings", "format").Enum[0] != "okf" {
		t.Fatal("missing definition settings")
	}
	units := describe(".devflow/releases.yml", src[".devflow/releases.yml"]).Schema
	if !at(t, units, "units", "*", "path").Required || !slices.Contains(at(t, units, "units", "*", "builds", "*").Enum, "image") {
		t.Fatal("invalid release unit schema")
	}
	flow := describe("flow-map.yml", src["flow-map.yml"]).Schema
	if !at(t, flow, "projects", "{}", "verbs", "{}").Nullable || !slices.Contains(at(t, flow, "defaults").KeySuggestions, "pr.merged") {
		t.Fatal("flow inheritance schema missing")
	}
	tpl := describe(".devflow/task-templates.json", src[".devflow/task-templates.json"])
	if tpl.Format != "json" || tpl.Schema.Nullable || !at(t, tpl.Schema, "*", "defaults", "labels").Nullable || !at(t, tpl.Schema, "*", "name").Required || at(t, tpl.Schema, "*", "defaults", "links", "*", "ref", "instance").Type != "string" {
		t.Fatal("template schema missing")
	}
	docs := describe("docs/confluence.yml", src["docs/confluence.yml"])
	if at(t, docs.Schema, "pages", "*", "parent_id").Type != "string" {
		t.Fatal("missing page override")
	}
	env := describe(".jira/config.yml", src[".jira/config.yml"])
	if !slices.Contains(at(t, env.Schema, "deployments", "environmentMapping").KeySuggestions, "production") {
		t.Fatal("missing environment keys")
	}
	bad := []struct{ p, c, base string }{{MainFile, "a: [", digest(src[MainFile])}, {MainFile, "a: b", "stale"}, {"../secret.yml", "a: b", ""}, {"flow-map.yml", "a: 9007199254740993", digest(src["flow-map.yml"])}}
	for _, b := range bad {
		if _, err := DescribeDocument(context.Background(), root, b.p, b.c, b.base); err == nil {
			t.Errorf("unsafe describe: %v", b)
		}
	}
}

func TestYAMLDocumentEditsPreserveSourceAndOmission(t *testing.T) {
	src := "# policy\nknown:\n    option: '001' # keep this\n    unrelated: [no, '008'] # keep style\nextra: future\n"
	next, err := EditDocument(src, "yaml", []string{"known", "option"}, json.RawMessage(`"002"`), false)
	if err != nil || !strings.Contains(next, "option: '002' # keep this") || !strings.Contains(next, "unrelated: [no, '008'] # keep style") || !strings.Contains(next, "extra: future") {
		t.Fatalf("rewrite: %v\n%s", err, next)
	}
	same, err := EditDocument(src, "yaml", []string{"known", "option"}, json.RawMessage(`"001"`), false)
	if err != nil || same != src {
		t.Fatal("no-op reformatted source")
	}
	src = "defaults: {verbs: {start: Ready}}\n"
	next, err = EditDocument(src, "yaml", []string{"defaults", "verbs", "done"}, json.RawMessage("null"), false)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]map[string]map[string]any
	if err := yaml.Unmarshal([]byte(next), &d); err != nil {
		t.Fatal(err)
	}
	if v, ok := d["defaults"]["verbs"]["done"]; !ok || v != nil {
		t.Fatal("null lost")
	}
	next, err = EditDocument(next, "yaml", []string{"defaults", "verbs", "done"}, nil, true)
	if err != nil || strings.Contains(next, "done") {
		t.Fatal("remove did not restore inheritance")
	}
	if _, err := EditDocument("shared: &x 2\nother: *x\n", "yaml", []string{"shared"}, json.RawMessage("1"), false); err == nil {
		t.Fatal("removed anchor")
	}
	if _, err := EditDocument("x: 1\n", "yaml", nil, json.RawMessage("9007199254740993"), false); err == nil {
		t.Fatal("accepted JS-unsafe number")
	}
}

func TestJSONEditsSpliceOnlyChangedMember(t *testing.T) {
	src := "[\r\n  { \"name\" : \"a\", \"revision\" : \"001\", \"checklist\" : [\"x\",\"y\"] },\r\n  {\"name\":\"\\ud83d\\ude00\",\"revision\":\"2\"}\r\n]\r\n"
	set := func(s string, p []string, raw string) string {
		t.Helper()
		tgt, err := EditDocument(s, "json", p, json.RawMessage(raw), raw == "")
		if err != nil {
			t.Fatal(err)
		}
		return tgt
	}
	if got := set(src, []string{"0", "revision"}, `"002"`); got != strings.Replace(src, `"001"`, `"002"`, 1) {
		t.Fatalf("nonlocal JSON rewrite:\n%s", got)
	}
	if got := set(src, []string{"0", "checklist", "0"}, ""); got != strings.Replace(src, `["x","y"]`, `["y"]`, 1) {
		t.Fatal(got)
	}
	if got := set(src, []string{"0", "checklist", "-"}, `"z"`); got != strings.Replace(src, `["x","y"]`, `["x","y","z"]`, 1) {
		t.Fatal(got)
	}
	if got := set(src, []string{"0", "checklist"}, ""); !strings.Contains(got, `"revision" : "001" }`) {
		t.Fatal(got)
	}
	next := set("[]", []string{"-", "defaults", "title"}, `"new"`)
	if next != `[{"defaults":{"title":"new"}}]` {
		t.Fatal(next)
	}
	if same := set(src, []string{"1", "name"}, `"😀"`); same != src {
		t.Fatal("JSON no-op lost escapes")
	}
	if _, err := EditDocument(src, "json", []string{"99"}, nil, true); err == nil {
		t.Fatal("accepted invalid array index")
	}
	if _, _, err := readDocument(`{"key":1,"key":2}`, "json"); err == nil {
		t.Fatal("duplicate JSON key accepted")
	}
}

func TestProviderDynamicMapHistoryAndComments(t *testing.T) {
	root, _ := fixture(t)
	s := "task_management:\n  provider: github-issues\n  settings:\n    transitions:\n      pr.merged: closed # keep\n      custom: open\n"
	put(t, root, MainFile, s)
	got, err := View(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, role := range got.Providers.Roles {
		if role.Role != "task_management" {
			continue
		}
		for _, field := range role.Settings {
			if field.Path != "transitions" {
				continue
			}
			for _, h := range field.History {
				if h.Step == "inline" && reflect.DeepEqual(h.Value, map[string]any{"pr.merged": "closed", "custom": "open"}) {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("inline dynamic map missing")
	}
	next, err := EditSetting(s, "task_management", "transitions", json.RawMessage(`{"pr.merged":"closed","new":"none"}`), false)
	if err != nil || !strings.Contains(next, "# keep") {
		t.Fatalf("map comment lost: %s %v", next, err)
	}
	settings, _, err := config.LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	explain, err := settings.Providers.TaskManagement.Explain()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range explain {
		if v.Path == "transitions" {
			t.Fatal("changed ordinary CLI explain output")
		}
	}
}

func TestReplaceContainerRetainsAnchorsAndItemComments(t *testing.T) {
	src := "values:\n  - alpha # first\n  - &b beta # stays with beta\n  - charlie # stays with charlie\ncopy: *b\n"
	next, err := EditDocument(src, "yaml", []string{"values"}, json.RawMessage(`["charlie","beta","new"]`), false)
	if err != nil || !strings.Contains(next, "- charlie # stays with charlie") || !strings.Contains(next, "- &b beta # stays with beta") || !strings.Contains(next, "copy: *b") {
		t.Fatalf("surviving items lost their syntax: %v\n%s", err, next)
	}
	if _, err := EditDocument(src, "yaml", []string{"values"}, json.RawMessage(`["alpha"]`), false); err == nil {
		t.Fatal("removed a referenced anchor")
	}
}

func TestNumericMapKeysAndSourcePositions(t *testing.T) {
	s := object(map[string]fieldSpec{"maps": dictionary(fieldSpec{Type: "string"})})
	got, err := EditDocument("{}", "json", []string{"maps", "0"}, json.RawMessage(`"001"`), false, s)
	if err != nil || got != `{"maps":{"0":"001"}}` {
		t.Fatalf("wrong numeric map key %s %v", got, err)
	}
	n, _, err := readDocument("{\"🌳\": false, \"a/b~c\":\n 7}", "json")
	if err != nil {
		t.Fatal(err)
	}
	loc := map[string]Position{}
	positions(n.Content[0], "", loc)
	if loc["/a~1b~0c"] != (Position{2, 2}) || loc["/🌳"] != (Position{1, 7}) {
		t.Fatalf("positions: %+v", loc)
	}
}
