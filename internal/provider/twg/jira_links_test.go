package twg

import (
	"testing"

	"github.com/ryanhelms/agentflow/internal/provider"
)

func TestJiraGetWithRemoteLinks(t *testing.T) {
	const raw = `{"data":[{"key":"ENG-42","summary":"Work","status":"Open","remoteLinks":[{"object":{"title":"Runbook","url":"https://example.atlassian.net/wiki/spaces/ENG/pages/1"}}]}]}`
	trace := fakeTWG(t, printJSON(raw))
	ref := provider.Ref{Instance: "jira", Scope: "ENG", ID: "ENG-42"}
	got, err := newTestJira(t, "ENG").GetWithRemoteLinks(t.Context(), ref)
	if err != nil || len(got.Links) != 1 || got.Links[0].Title != "Runbook" || got.Links[0].Ref != nil {
		t.Fatalf("task = %+v, err = %v", got, err)
	}
	assertArgs(t, trace, "jira", "workitem", "get", "ENG-42", "--fields", jiraFields, "--remote-links")
}
