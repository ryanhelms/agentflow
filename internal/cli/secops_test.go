package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/provider"
)

func TestSecOpsTriageCLIFixture(t *testing.T) {
	fixture, err := os.ReadFile("../provider/secops/testdata/incidents.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.Mkdir(".devflow", 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".devflow/incidents.json", fixture)
	write(".devflow/config.yml", []byte(`secops:
  settings:
    findings: []
    incidents: {fixture: .devflow/incidents.json, scope: service}
task_management:
  provider: filesystem
  settings: {scope: response}
  templates:
    catalog: .devflow/templates.json
    default: {name: incident, revision: "1"}
`))
	write(".devflow/templates.json", []byte(`[{"name":"incident","revision":"1","required":["priority"]}]`))
	if err := run(t.Context(), "secops", []string{"triage"}); err == nil || !strings.Contains(err.Error(), "priority") {
		t.Fatalf("missing template field error = %v", err)
	}
	write(".devflow/templates.json", []byte(`[{"name":"incident","revision":"1","required":["links"],"checklist":["Review incident"]}]`))
	capture := func(args ...string) *json.Decoder {
		t.Helper()
		f, err := os.CreateTemp(t.TempDir(), "triage")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		stdout := os.Stdout
		os.Stdout = f
		defer func() { os.Stdout = stdout }()
		if code := Main(append([]string{"secops", "triage"}, args...)); code != 0 {
			t.Fatalf("devflow secops triage %v returned %d", args, code)
		}
		if _, err := f.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		return json.NewDecoder(f)
	}
	var plans []app.SecOpsTaskPlan
	if err := capture("--dry-run").Decode(&plans); err != nil || len(plans) != 1 {
		t.Fatalf("preview = %+v, %v", plans, err)
	}
	if _, err := os.Stat(".devflow/tasks"); !os.IsNotExist(err) {
		t.Fatalf("preview touched task directory: %v", err)
	}
	plan := plans[0]
	if plan.Task.Template == nil || plan.Task.Template.Revision != "1" || len(plan.Task.Links) != 1 ||
		plan.Task.Links[0].URL != "https://status.example.test/incidents/7" {
		t.Fatalf("incident draft = %+v", plan.Task)
	}
	apply := func() provider.WriteResult {
		t.Helper()
		dec := capture("--source", plan.Source, "--apply", "--yes")
		var preview map[string]any
		if err := dec.Decode(&preview); err != nil || preview["secops"] == nil {
			t.Fatalf("confirmation = %+v, %v", preview, err)
		}
		var result provider.WriteResult
		if err := dec.Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first, replay := apply(), apply()
	if first.Status != provider.Applied || replay.Status != provider.AlreadyApplied || first.Ref != replay.Ref {
		t.Fatalf("first = %+v, replay = %+v", first, replay)
	}
	records, err := filepath.Glob(filepath.Join(dir, ".devflow", "tasks", "*.md"))
	if err != nil || len(records) != 1 {
		t.Fatalf("persisted records = %v, %v", records, err)
	}
}
