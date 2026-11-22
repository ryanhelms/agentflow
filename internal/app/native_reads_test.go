package app

import (
	"testing"

	"github.com/nscaledev/devflow/internal/provider/native"
)

func TestExplicitAtlassianCredentialsKeepHeadlessRoute(t *testing.T) {
	const site = "https://sample.atlassian.net"
	t.Setenv("JIRA_USER", "fixture")
	t.Setenv("JIRA_TOKEN", "fixture")
	t.Setenv("CONFLUENCE_USER", "")
	t.Setenv("CONFLUENCE_TOKEN", "")

	for _, name := range []string{"jira", "confluence"} {
		if useAtlassianCLI(t.Context(), site, name) {
			t.Errorf("%s replaced the explicit service account with CLI auth", name)
		}
		if !useAtlassianCLI(native.WithHost(t.Context(), native.Client{}), site, name) {
			t.Errorf("%s interactive path did not offer CLI fallback", name)
		}
	}
}

func TestConfluenceMCPResultsPreserveRevisionAndScope(t *testing.T) {
	pages, err := decodeConfluencePages([]byte(`{"results":[{"content":{"id":"123","title":"Fixture","version":{"number":4},"space":{"key":"APP"}}}]}`), "https://sample.atlassian.net", "APP")
	if err != nil || len(pages) != 1 || pages[0].Revision != "4" || pages[0].Space != "APP" {
		t.Fatalf("mapped results %+v: %v", pages, err)
	}
	if _, err := decodeConfluencePages([]byte(`{"results":[{"content":{"id":"123","title":"Fixture"}}]}`), "https://sample.atlassian.net", "APP"); err == nil {
		t.Fatal("scoped query accepted a provider hit without a space")
	}
}
