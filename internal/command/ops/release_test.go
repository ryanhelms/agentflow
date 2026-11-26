package ops_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
)

// offline makes every HTTP request of the test fail, so a write that passes
// its gate cannot reach a real service.
func offline(t *testing.T) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline fixture") })
	t.Cleanup(func() { http.DefaultTransport = old })
}

func TestReleasePublishPublishesRepositoryWideEvents(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", confirmEverything)
	t.Setenv("GH_TOKEN", "fixture")
	offline(t)
	r.git(t, "remote", "add", "origin", "https://github.com/acme/app.git")
	sha := r.git(t, "rev-parse", "HEAD")

	// A dry run previews and publishes nothing.
	em, env, err := r.cli(t, false, "release", "publish", "v1.0.0", "--revision", sha, "--dry-run")
	if err != nil || !strings.Contains(env.Out.String(), `"tag": "v1.0.0"`) {
		t.Fatalf("dry run: %v, output %q", err, env.Out.String())
	}
	wantOpTypes(t, em)

	// A bad revision is refused before the gate.
	em, _, err = r.cli(t, false, "release", "publish", "v1.0.0", "--revision", "abc")
	if err == nil || !strings.Contains(err.Error(), "full commit sha") {
		t.Fatalf("short revision: %v", err)
	}
	evs := wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	wantWorktreeScope(t, evs, true)
	if p, _ := commandtest.Payload(evs[1]); p.Code != "error" || strings.Contains(string(evs[1].Payload), "abc") {
		t.Errorf("refusal payload %s", evs[1].Payload)
	}

	// A "no" at the prompt leaves it asking.
	em, _, err = r.cli(t, false, "release", "publish", "v1.0.0", "--revision", sha)
	if err == nil {
		t.Fatal("declined prompt succeeded")
	}
	evs = wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval)
	wantWorktreeScope(t, evs, true)
	if p := wantOpPayload(t, evs[0], "release.publish", "release_publish", "remote"); p.Subject != "publish release v1.0.0" {
		t.Errorf("subject %q", p.Subject)
	}

	// Confirmed, the write passes the gate and the offline provider fails it:
	// the same operation ends in op.failed.
	em, _, err = r.cli(t, true, "release", "publish", "v1.0.0", "--revision", sha, "--yes")
	if err == nil {
		t.Fatal("publication reached an offline provider")
	}
	evs = wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpFailed)
	wantWorktreeScope(t, evs, true)
	if evs[3].CorrelationID != evs[1].CorrelationID || evs[3].CausationID != evs[2].ID {
		t.Errorf("failure not joined to its approval: %+v", evs)
	}
	noTokensInEvents(t, em)
	// The method keeps its own audit line, with the failure.
	lines := r.auditLines(t)
	if len(lines) == 0 || lines[len(lines)-1]["action"] != "release_publish" {
		t.Errorf("audit lines %v", lines)
	}
}

func TestReleaseReadsPublishNothing(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", "")
	r.git(t, "tag", "v0.1.0")
	writeOpsFile(t, filepath.Join(r.dir, ".agentflow", "releases.yml"),
		"strategy: trunk\nunits:\n  - {name: app, path: ., release_type: simple, builds: [binary]}\n")
	em, env, err := r.cli(t, false, "release", "preview")
	if err != nil || !strings.Contains(env.Out.String(), `"since": "v0.1.0"`) {
		t.Fatalf("release preview: %v, output %q", err, env.Out.String())
	}
	wantOpTypes(t, em)
	em, out, isErr := r.tool(t, "release.preview", map[string]any{})
	if isErr || !strings.Contains(out, `"since":"v0.1.0"`) {
		t.Fatalf("preview tool: %v %s", isErr, out)
	}
	wantOpTypes(t, em)
	// The legacy command accepted the flags of its other subcommands.
	em, env, err = r.cli(t, false, "release", "plan", "--since", "v0.1.0", "--json")
	if err != nil {
		t.Fatalf("release plan --json: %v", err)
	}
	wantOpTypes(t, em)
	if !strings.Contains(env.Out.String(), `"unit"`) && !strings.Contains(env.Out.String(), "app") {
		t.Errorf("release plan output %q", env.Out.String())
	}
	if _, _, err := r.cli(t, false, "release", "publish"); err == nil || !strings.HasPrefix(err.Error(), "usage: agentflow release publish TAG") {
		t.Errorf("release publish without a tag: %v", err)
	}
	if _, _, err := r.cli(t, false, "release", "publish", "--revision", "x", "v1"); err == nil || err.Error() != "pass the release tag before its options" {
		t.Errorf("release publish with a leading flag: %v", err)
	}
}

func writeOpsFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}
