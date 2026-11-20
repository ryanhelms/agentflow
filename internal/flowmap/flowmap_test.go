package flowmap

import "testing"

const sample = `
version: 1
defaults:
  branch.created: In Progress
  pr.opened: In Review
  pr.merged: Done
  verbs: { start: In Progress, review: In Review, done: Done }
projects:
  PLAT:
    pr.merged: QA
    deploy.production: Done
    verbs: { qa: QA, block: Blocked }
    slack: "#plat-eng"
    confluence: { space: PLAT, release_parent: "Release Notes" }
    order: [To Do, In Progress, In Review, QA, Done]
  INFRA:
    pr.opened: null
`

func TestResolve(t *testing.T) {
	m, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		project, event, want string
		ok                   bool
	}{
		{"PLAT", PRMerged, "QA", true},
		{"PLAT", PROpened, "In Review", true},
		{"PLAT", "#qa", "QA", true},
		{"PLAT", "#start", "In Progress", true},
		{"INFRA", PROpened, "", false},
		{"INFRA", PRMerged, "Done", true},
		{"OTHER", DeployProduction, "", false},
		{"OTHER", "#qa", "", false},
	}
	for _, c := range cases {
		got, ok := m.Resolve(c.project, c.event)
		if got != c.want || ok != c.ok {
			t.Errorf("%s %s: got %q,%v want %q,%v", c.project, c.event, got, ok, c.want, c.ok)
		}
	}
	if !m.Backwards("PLAT", "QA", "In Review") || m.Backwards("PLAT", "In Progress", "In Review") {
		t.Error("order check wrong")
	}
	if p := m.Project("PLAT"); p.Slack != "#plat-eng" || p.Confluence.Space != "PLAT" {
		t.Errorf("project = %+v", p)
	}
}

func TestParseRef(t *testing.T) {
	r, remote := ParseRef("nscaledev/devflow@v0:flow-map.yml")
	if !remote || r.Repo != "nscaledev/devflow" || r.Ref != "v0" || r.Path != "flow-map.yml" {
		t.Errorf("got %+v", r)
	}
	if _, remote := ParseRef("flow-map.yml"); remote {
		t.Error("local path parsed as remote")
	}
}
