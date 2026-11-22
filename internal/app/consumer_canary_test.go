package app

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nscaledev/devflow/internal/gh"
	"github.com/nscaledev/devflow/internal/provider"
)

// TestProviderConsumerCanary uses normal application entry points and a
// disposable consumer profile. No hosted service receives fixture writes.
func TestProviderConsumerCanary(t *testing.T) {
	root := newRepo(t)
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	run := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var acceptedSlack, webhookAttempts, acceptedRelease atomic.Int32
	const revision = "0123456789abcdef0123456789abcdef01234567"
	const published = `{"tag_name":"v0.2.0","name":"Canary 0.2.0","body":"Fixture","html_url":"https://github.com/example/consumer/releases/tag/v0.2.0"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/slack":
			acceptedSlack.Add(1)
			fmt.Fprint(w, "ok")
		case req.Method == http.MethodPost && req.URL.Path == "/webhook":
			if webhookAttempts.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
			}
		case req.Method == http.MethodGet && req.URL.Path == "/repos/example/consumer/commits/"+revision:
			fmt.Fprintf(w, `{"sha":%q}`, revision)
		case req.Method == http.MethodGet && req.URL.Path == "/repos/example/consumer/git/ref/tags/v0.2.0":
			if acceptedRelease.Load() == 0 {
				http.NotFound(w, req)
				return
			}
			fmt.Fprintf(w, `{"object":{"type":"commit","sha":%q}}`, revision)
		case req.Method == http.MethodGet && req.URL.Path == "/repos/example/consumer/releases/tags/v0.2.0":
			if acceptedRelease.Load() == 0 {
				http.NotFound(w, req)
				return
			}
			fmt.Fprint(w, published)
		case req.Method == http.MethodPost && req.URL.Path == "/repos/example/consumer/releases":
			acceptedRelease.Add(1)
			// The effect happened before the producer received a usable response.
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected fixture request: %s %s", req.Method, req.URL.Path)
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	t.Setenv("CANARY_SLACK_URL", server.URL+"/slack")
	t.Setenv("CANARY_WEBHOOK_URL", server.URL+"/webhook")

	const legacy = "devflow: 1\njira: {site: https://jira.example.invalid, projects: [PLAT]}\nconfluence: {site: https://docs.example.invalid}\n"
	const candidate = `devflow: 1
task_management:
  provider: filesystem
  settings: {root: .devflow/tasks, scope: canary}
  templates:
    catalog: .devflow/task-templates.json
    default: {name: incident, revision: "1"}
knowledge_base:
  provider: filesystem
  settings: {root: .devflow/knowledge}
devops:
  releases:
    provider: github
    settings: {repository: example/consumer}
secops:
  provider: github
  settings:
    findings: []
    incidents: {fixture: .devflow/incidents.json, scope: canary}
notifications:
  provider: router
  settings:
    destinations:
      slack: {provider: slack, credential_ref: CANARY_SLACK_URL}
      webhook: {provider: webhook, credential_ref: CANARY_WEBHOOK_URL, rejected_statuses: [429]}
    routes:
      incident.recorded: [slack, webhook]
`
	run(".devflow/config.yml", legacy)
	baseline, err := Open(t.Context(), root, "consumer-canary")
	if err != nil {
		t.Fatal(err)
	}
	run(".devflow/config.yml", candidate)
	run(".devflow/incidents.json", `[{"id":"replay","title":"Consumer canary","severity":"high","url":"https://status.example.invalid/incidents/replay"}]`)
	run(".devflow/task-templates.json", `[{"name":"incident","revision":"1","required":["title"],"defaults":{"type":"Task"}}]`)

	open := func() *App {
		t.Helper()
		a, err := Open(t.Context(), root, "consumer-canary")
		if err != nil {
			t.Fatal(err)
		}
		a.ghc = gh.New("fixture")
		a.ghc.API = server.URL
		return a
	}
	ctx := t.Context()
	source := provider.Ref{Instance: "incident-fixture", Scope: "canary", ID: "replay"}
	doc := DocsPublishInput{
		Ref:   provider.Ref{Instance: "filesystem", Scope: "canary", ID: "evidence"},
		Title: "Canary evidence", Body: "Release and rollback evidence.", OperationID: "canary-doc-1",
	}
	if err := os.MkdirAll(filepath.Join(root, ".devflow", "knowledge"), 0o700); err != nil {
		t.Fatal(err)
	}
	notify := NotifyInput{Signal: "incident.recorded", OperationID: "canary-notify-1", Title: "Canary replay"}
	doWrite := func(fn func(string) (any, error), want provider.WriteStatus) provider.WriteResult {
		t.Helper()
		out, err := fn("")
		if err != nil {
			t.Fatal(err)
		}
		if _, pending := out.(*Pending); pending {
			out, err = fn(confirmation(t, out))
		}
		result, ok := out.(provider.WriteResult)
		if err != nil || !ok || result.Status != want {
			t.Fatalf("provider write = %+v, %v; want %s", out, err, want)
		}
		return result
	}
	effects := func(a *App, want provider.WriteStatus, first, second NotificationStatus) {
		t.Helper()
		doWrite(func(token string) (any, error) {
			return a.SecOpsTriage(ctx, SecOpsTriageInput{Source: source, Apply: true}, token)
		}, want)
		doWrite(func(token string) (any, error) { return a.DocsPublish(ctx, doc, token) }, want)
		deliveries, pending, err := a.Notify(ctx, notify, "")
		if err == nil && pending != nil {
			deliveries, _, err = a.Notify(ctx, notify, confirmation(t, pending))
		}
		if err != nil || len(deliveries.Deliveries) != 2 ||
			deliveries.Deliveries[0].Status != first || deliveries.Deliveries[1].Status != second {
			t.Fatalf("notification delivery = %+v, %v; want %s / %s", deliveries, err, first, second)
		}
	}

	effects(open(), provider.Applied, NotificationDelivered, NotificationRejected)
	t.Log("interrupted after the first delivery; the remaining destination rejected the attempt")
	a := open()
	effects(a, provider.AlreadyApplied, NotificationAlreadyDelivered, NotificationDelivered)
	release := ReleasePublishInput{Tag: "v0.2.0", Revision: revision, Title: "Canary 0.2.0", Notes: "Fixture"}
	doWrite(func(token string) (any, error) { return a.ReleasePublish(ctx, release, token) }, provider.AlreadyApplied)
	a = open()
	effects(a, provider.AlreadyApplied, NotificationAlreadyDelivered, NotificationAlreadyDelivered)
	doWrite(func(token string) (any, error) { return a.ReleasePublish(ctx, release, token) }, provider.AlreadyApplied)
	tasks, err := a.MyWork(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("task count = %d, %v", len(tasks), err)
	}
	contextResult, err := a.Context(ctx, tasks[0].Key)
	if err != nil || !strings.Contains(contextResult.Text, "Consumer canary") {
		t.Fatalf("task context = %+v, %v", contextResult, err)
	}
	docs, err := a.DocsSearch(ctx, "Canary evidence", "canary")
	pages, _ := docs["pages"].([]provider.Document)
	if err != nil || len(pages) != 1 {
		t.Fatalf("document count = %d, %v", len(pages), err)
	}
	if acceptedSlack.Load() != 1 || webhookAttempts.Load() != 2 || acceptedRelease.Load() != 1 {
		t.Fatalf("unexpected fixture write counts: Slack=%d webhook attempts=%d release=%d", acceptedSlack.Load(), webhookAttempts.Load(), acceptedRelease.Load())
	}

	run(".devflow/config.yml", legacy)
	restored := open()
	actual, err := os.ReadFile(filepath.Join(root, ".devflow/config.yml"))
	if err != nil || !bytes.Equal(actual, []byte(legacy)) ||
		restored.Cfg.Providers.TaskManagement.Provider != baseline.Cfg.Providers.TaskManagement.Provider ||
		restored.Cfg.Providers.KnowledgeBase.Provider != baseline.Cfg.Providers.KnowledgeBase.Provider {
		t.Fatalf("rollback did not restore the baseline: %v", err)
	}
	t.Log("restored legacy settings after one task, one document, one release and two accepted notification deliveries")
}
