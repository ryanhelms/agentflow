package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/ryanhelms/agentflow/internal/provider/native"
)

func TestJiraCreateStopsOnMCPFailure(t *testing.T) {
	a, host, ctx := jiraNativeApp(t)
	if _, err := a.Repo.Git(ctx, "switch", "main"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"JIRA_USER", "JIRA_TOKEN", "CI", "GITHUB_ACTIONS", "AGENTFLOW_OPERATION_ID"} {
		t.Setenv(name, "")
	}
	t.Setenv("PATH", t.TempDir()) // Any accidental fallback would offer a REST preview.
	ctx = WithJiraWriteCLI(ctx)
	create := func(token string) (any, error) {
		return a.CreateTicket(ctx, "PLAT", "Task", "Follow up", "Details", "", token)
	}
	for _, failure := range []error{native.ErrSignIn, native.ErrDenied, context.DeadlineExceeded, errors.Join(native.ErrUnavailable, native.ErrDenied)} {
		host.prepareErr = failure
		if _, err := create(""); !errors.Is(err, failure) {
			t.Fatal("prepare", err)
		}
	}
	host.prepareErr = nil
	out, err := create("")
	if err != nil {
		t.Fatal(err)
	}
	host.writeErr = native.ErrWriteUncertain
	if _, err = create(confirmation(t, out)); !errors.Is(err, native.ErrWriteUncertain) || len(host.writes) != 1 {
		t.Fatal("dispatch", err, host.writes)
	}
}

func TestJiraCreateServiceRESTFallback(t *testing.T) {
	var posts int
	a := legacyApp(t, func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "jira-user" || p != "jira-fixture" || r.Method != "POST" || r.URL.String() != "https://tasks.example/rest/api/3/issue" {
			t.Fatalf("unexpected service request %s %s", r.Method, r.URL)
		}
		posts++
		fmt.Fprint(w, `{"key":"PLAT-20"}`)
	})
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("AGENTFLOW_OPERATION_ID", "")
	ctx := WithJiraWriteCLI(t.Context())
	if _, err := a.Repo.Git(ctx, "switch", "main"); err != nil {
		t.Fatal(err)
	}
	create := func(token string) (any, error) {
		return a.CreateTicket(ctx, "PLAT", "Task", "Follow up", "Details", "", token)
	}
	out, err := create("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.(*Pending).Summary.(map[string]string); !ok || posts != 0 {
		t.Fatal("expected REST preview", out)
	}
	out, err = create(confirmation(t, out))
	if err != nil || posts != 1 || out.(map[string]string)["key"] != "PLAT-20" {
		t.Fatal(out, err, posts)
	}
}
