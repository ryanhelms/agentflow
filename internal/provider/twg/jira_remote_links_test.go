package twg

import (
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/atlassian"
)

func TestJiraRemoteLinksListsExactWebLinks(t *testing.T) {
	const linkURL = "https://docs.example.com/releases/%2F?a=1&b=2#v1"
	const rows = `"remoteLinks":[
		{"object":{"url":"` + linkURL + `","title":"  Release: v1  "}},
		{"object":{"url":"` + linkURL + `","title":"Another title"}}
	],`
	// jiraRead includes a Jira parent, which must never satisfy a web-link lookup.
	reply := strings.Replace(jiraRead, `"parent":`, rows+`"parent":`, 1)
	for _, name := range []string{"TWG_TOKEN", "TWG_USER", "JIRA_TOKEN", "TWG_PROFILE"} {
		t.Setenv(name, "fixture")
	}
	trace := fakeTWG(t, `test -z "${TWG_TOKEN+x}${TWG_USER+x}${JIRA_TOKEN+x}${TWG_PROFILE+x}"`+"\n"+printJSON(reply))
	j := newTestJira(t, "ENG")
	got, err := j.RemoteLinks(t.Context(), j.ref("ENG", "ENG-42"))
	want := []atlassian.Page{{Title: "  Release: v1  ", URL: linkURL}, {Title: "Another title", URL: linkURL}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("RemoteLinks() = %+v, %v; want %+v", got, err, want)
	}
	assertArgs(t, trace, "jira", "workitem", "get", "ENG-42", "--fields", jiraFields, "--remote-links")
}

func TestJiraRemoteLinksRejectsIncompleteOrMismatchedRead(t *testing.T) {
	for _, reply := range []string{
		jiraRead, // The requested remoteLinks field is absent.
		`{"data":[{"key":"ENG-43","summary":"Other","status":"Open","remoteLinks":[]}]}`,
		`{"partial":true,"data":[{"key":"ENG-42","summary":"Work","status":"Open","remoteLinks":[]}]}`,
	} {
		fakeTWG(t, printJSON(reply))
		j := newTestJira(t, "ENG")
		if links, err := j.RemoteLinks(t.Context(), j.ref("ENG", "ENG-42")); err == nil || links != nil {
			t.Fatalf("RemoteLinks() = %+v, %v for %s", links, err, reply)
		}
	}
}
