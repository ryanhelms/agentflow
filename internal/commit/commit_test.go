package commit

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
)

var rules = config.Commit{Types: config.DefaultTypes, SubjectMax: 72}

func TestParse(t *testing.T) {
	m := Parse("fix(INFRA-88,INFRA-90)!: rotate etcd certs before expiry #done #time 2h\n\nCerts expired in staging.\n\nAgentflow: skip\n")
	if !m.Valid || m.Type != "fix" || !m.Breaking {
		t.Fatalf("bad parse: %+v", m)
	}
	if !reflect.DeepEqual(m.Keys, []string{"INFRA-88", "INFRA-90"}) {
		t.Errorf("keys = %v", m.Keys)
	}
	if m.Summary != "rotate etcd certs before expiry" {
		t.Errorf("summary = %q", m.Summary)
	}
	if !reflect.DeepEqual(m.Verbs, []string{"done"}) || m.Time != "2h" || !m.Skip {
		t.Errorf("verbs=%v time=%q skip=%v", m.Verbs, m.Time, m.Skip)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		msg   string
		codes []string
	}{
		{"feat(PLAT-123): add per-tenant GPU quota API #review", nil},
		{"Merge branch 'main' into PLAT-1-x", nil},
		{"updated stuff", []string{"format"}},
		{"feature(PLAT-1): add thing", []string{"type"}},
		{"feat(PLAT-1): added thing", []string{"mood"}},
		{"feat(PLAT-1): Add thing.", []string{"period", "case"}},
		{"feat(PLAT-1): API keys can be rotated", nil},
		{"feat(PLAT-1): " + strings.Repeat("x", 80), []string{"length"}},
	}
	for _, c := range cases {
		var got []string
		for _, i := range Validate(c.msg, rules, "") {
			got = append(got, i.Code)
		}
		if !reflect.DeepEqual(got, c.codes) {
			t.Errorf("%q: got %v want %v", c.msg, got, c.codes)
		}
	}
}

func TestSuggestFix(t *testing.T) {
	iss := Validate("Fixed the quota bug", rules, "PLAT-7")
	if len(iss) != 1 || iss[0].Fix != "fix(PLAT-7): fix the quota bug" {
		t.Fatalf("got %+v", iss)
	}
	iss = Validate("PLAT-9 add retries", rules, "")
	if iss[0].Fix != "feat(PLAT-9): add retries" {
		t.Fatalf("got %+v", iss)
	}
}

func TestPlan(t *testing.T) {
	g := Plan([]string{"api/quota.go", "api/quota_test.go", ".github/workflows/ci.yml", "web/app.ts", "README.md"})
	if len(g) != 4 {
		t.Fatalf("groups = %+v", g)
	}
	if g[0].Files[0] != "api/quota.go" || g[2].Type != "docs" || g[3].Type != "ci" {
		t.Errorf("groups = %+v", g)
	}
	g = Plan([]string{"api/a.go", "docs/api.md"})
	if len(g) != 1 {
		t.Errorf("docs should join single code area: %+v", g)
	}
}

func TestBreaking(t *testing.T) {
	hits := Breaking([]string{"api/v1/quota.proto", "migrations/0003_add.sql", "main.go"},
		[]string{"api/**/*.proto", "migrations/**"})
	if len(hits) != 2 {
		t.Errorf("hits = %v", hits)
	}
}

func TestStripAITrailers(t *testing.T) {
	in := "feat(PLAT-1): add x\n\nbody\n\nCo-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>\nCo-authored-by: Jane <jane@nscale.com>\n"
	out := StripAITrailers(in)
	if strings.Contains(out, "anthropic") || !strings.Contains(out, "jane@nscale.com") {
		t.Errorf("out = %q", out)
	}
}

func TestFormat(t *testing.T) {
	got := Format("feat", []string{"PLAT-1"}, true, "drop v1 quota field", "Why.", []string{"review"}, []string{"Refs: PLAT-2"})
	want := "feat(PLAT-1)!: drop v1 quota field #review\n\nWhy.\n\nRefs: PLAT-2\n"
	if got != want {
		t.Errorf("got %q", got)
	}
}

func TestKeyOptional(t *testing.T) {
	off := false
	loose := config.Commit{Types: config.DefaultTypes, SubjectMax: 72, RequireKey: &off}
	for _, ok := range []string{"docs: add enterprise readme", "feat(cli): add release plan", "fix(PLAT-9): handle nil", "ci!: drop release-please"} {
		if iss := Validate(ok, loose, ""); len(iss) != 0 {
			t.Errorf("%q should pass without a key: %+v", ok, iss)
		}
	}
	if iss := Validate("updated stuff", loose, ""); len(iss) != 1 || iss[0].Code != "format" {
		t.Errorf("non-conventional subject must still fail: %+v", iss)
	}
	if iss := Validate("feature: add x", loose, ""); len(iss) != 1 || iss[0].Code != "type" {
		t.Errorf("unknown type must still fail: %+v", iss)
	}
	if iss := Validate("docs: add readme", rules, ""); len(iss) != 1 {
		t.Errorf("keys are required by default: %+v", iss)
	}
	m := Parse("feat(cli)!: drop flag")
	if !m.Conventional || m.Valid || m.Type != "feat" || m.Scope != "cli" || !m.Breaking {
		t.Errorf("parse = %+v", m)
	}
}
