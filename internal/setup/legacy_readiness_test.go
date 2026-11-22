package setup

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLegacyProviderReadiness(t *testing.T) {
	for _, key := range []string{"JIRA_USER", "JIRA_TOKEN", "CONFLUENCE_USER", "CONFLUENCE_TOKEN", "SLACK_WEBHOOK_URL", "TEAM_WEBHOOK"} {
		t.Setenv(key, "")
	}
	var calls, status atomic.Int32
	var redirectURL string
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("readiness followed a redirect")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(redirect.Close)
	redirectURL = redirect.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("unexpected readiness method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		user, token, ok := r.BasicAuth()
		if !ok || user != "test-user" || token != "test-token" {
			t.Error("readiness did not use the configured credentials")
		}
		switch code := int(status.Load()); code {
		case http.StatusTemporaryRedirect:
			http.Redirect(w, r, redirectURL, code)
			return
		case http.StatusOK:
		default:
			http.Error(w, "private response body", code)
			return
		}
		switch r.URL.Path {
		case "/rest/api/3/myself":
			_, _ = fmt.Fprint(w, `{"accountId":"test-account"}`)
		case "/wiki/api/v2/spaces":
			if r.URL.Query().Get("limit") != "1" {
				t.Error("expected a bounded Confluence space listing")
			}
			_, _ = fmt.Fprint(w, `{"results":[]}`)
		default:
			t.Errorf("unexpected readiness path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	cfg := strings.Replace(headlessConfig, "github-issues, settings: {repository: sample/project}", "jira, settings: {site: "+server.URL+"}", 1)
	cfg = strings.Replace(cfg, "filesystem, settings: {root: records}", "confluence, settings: {site: "+server.URL+"}", 1)
	cfg = strings.Replace(cfg, "notifications: {provider: none}", "notifications: {provider: slack, settings: {credential_ref: TEAM_WEBHOOK}}", 1)
	setupFile(t, root, ".devflow/config.yml", cfg, 0600)

	plan := planFixture(t, root, "")
	if plan.Ready || calls.Load() != 0 {
		t.Fatalf("plan with missing credentials %+v", plan)
	}
	planContains(t, plan, "run devflow login")
	planContains(t, plan, "CONFLUENCE_USER and CONFLUENCE_TOKEN")
	planContains(t, plan, "TEAM_WEBHOOK")

	t.Setenv("JIRA_USER", "test-user")
	t.Setenv("JIRA_TOKEN", "test-token")
	t.Setenv("TEAM_WEBHOOK", "invalid-webhook-secret")
	status.Store(http.StatusOK)
	plan = planFixture(t, root, "")
	if plan.Ready || calls.Load() != 2 {
		t.Fatalf("plan with invalid webhook %+v", plan)
	}
	planContains(t, plan, "can read the authenticated Jira identity")
	planContains(t, plan, "can list visible Confluence spaces")
	planContains(t, plan, "requires an absolute http or https URL")

	t.Setenv("TEAM_WEBHOOK", server.URL+"/webhook")
	plan = planFixture(t, root, "")
	if !plan.Ready || calls.Load() != 4 {
		t.Fatalf("plan with valid credentials and local webhook configuration %+v", plan)
	}
	planContains(t, plan, "Slack webhook configuration is valid")

	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTemporaryRedirect} {
		status.Store(int32(code))
		plan = planFixture(t, root, "")
		if plan.Ready {
			t.Fatalf("unexpected ready plan for HTTP %d", code)
		}
		planContains(t, plan, fmt.Sprintf("HTTP %d", code))
		for _, req := range plan.Requirements {
			if strings.Contains(req.Detail, "private response body") {
				t.Fatal("readiness leaked the remote response body")
			}
		}
	}
}
