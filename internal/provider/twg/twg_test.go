package twg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nscaledev/devflow/internal/provider"
)

const testSite = "https://example.atlassian.net"

const jiraRead = `{"data":[{"key":"ENG-42","url":"https://example.atlassian.net/browse/ENG-42",
  "summary":"Bound task","description":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Task details"}]}]},
  "status":{"id":"10000","name":"Closed","statusCategory":{"key":"done"}},"issueType":"Task",
  "priority":{"name":"High"},"assignee":{"displayName":"Person"},"labels":["fixture"],
  "updated":"2026-11-18T01:00:00.000+0000","parent":{"key":"ENG-2","summary":"Parent task"}}]}`

const jiraQuery = `{"data":{"issues":[{"key":"ENG-42","fields":{
  "summary":"Bound task","status":{"name":"Open"},"project":{"key":"ENG"}}}],"pageInfo":{"hasNextPage":false}}}`

const confluenceRead = `{"data":{"id":"55","title":"Bound page","contentType":"page",
  "url":"https://example.atlassian.net/wiki/spaces/DOC/pages/55/Bound+page",
  "version":{"number":8},"body":{"value":"# Full page\n\nBody."}}}`

const confluenceQuery = `{"data":{"results":[{"title":"Bound page","url":"/spaces/DOC/pages/55/Bound+page",
  "content":{"id":"55","type":"page"},"excerpt":"@@@hl@@@Bound@@@endhl@@@ page"}]}}`

func fakeTWG(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("twg subprocess fixtures use /bin/sh")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	trace := filepath.Join(dir, "args")
	script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$@\" > " + shellQuote(trace) + "\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "twg"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return trace
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func printJSON(value string) string {
	return "printf '%s\\n' " + shellQuote(value)
}

func assertArgs(t *testing.T, trace string, want ...string) {
	t.Helper()
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	want = append(want, "--site", testSite, "--output", "json", "--output-summary", "none")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func newTestJira(t *testing.T, projects ...string) *Jira {
	t.Helper()
	adapter, err := NewJira("jira", testSite, projects)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func newTestConfluence(t *testing.T, spaces ...string) *Confluence {
	t.Helper()
	adapter, err := NewConfluence("wiki", testSite, spaces)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestJiraGet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TWG_TOKEN", "fixture")
	t.Setenv("TWG_USER", "fixture")
	t.Setenv("TWG_PROFILE", "fixture")
	t.Setenv("TWG_AUTH_STORAGE_KEY", "fixture")
	t.Setenv("JIRA_TOKEN", "fixture")
	trace := fakeTWG(t, `
test "${CI:-}" = true
test "$HOME" = `+shellQuote(os.Getenv("HOME"))+`
test -z "${TWG_TOKEN+x}${TWG_USER+x}${TWG_PROFILE+x}${TWG_AUTH_STORAGE_KEY+x}${JIRA_TOKEN+x}"
`+printJSON(jiraRead))
	j := newTestJira(t, "eng")
	got, err := j.Get(t.Context(), provider.Ref{Instance: "jira", Scope: "eng", ID: "eng-42"})
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Task{
		Ref: j.ref("ENG", "ENG-42"), Key: "ENG-42", URL: testSite + "/browse/ENG-42",
		Title: "Bound task", Description: "Task details", Type: "Task", Priority: "High", Assignee: "Person",
		State:  provider.TaskState{ID: "10000", Name: "Closed", Category: "done", Terminal: true},
		Labels: []string{"fixture"}, Revision: "2026-11-18T01:00:00.000+0000",
	}
	parent := j.ref("ENG", "ENG-2")
	want.Links = []provider.Link{{Ref: &parent, Title: "Parent task", URL: testSite + "/browse/ENG-2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("task = %#v, want %#v", got, want)
	}
	assertArgs(t, trace, "jira", "workitem", "get", "ENG-42", "--fields", jiraFields)
}

func TestJiraFindAndMyWork(t *testing.T) {
	trace := fakeTWG(t, printJSON(jiraQuery))
	projects := []string{"ENG", "PLAT"}
	j := newTestJira(t, projects...)
	projects[0] = "OTHER"
	tasks, err := j.Find(t.Context(), provider.TaskQuery{Text: `a "quote" and \ slash`, AssignedToMe: true, OpenOnly: true, Limit: 999})
	if err != nil || len(tasks) != 1 || tasks[0].Ref != j.ref("ENG", "ENG-42") || tasks[0].State.Name != "Open" {
		t.Fatalf("tasks = %#v, err = %v", tasks, err)
	}
	assertArgs(t, trace, "jira", "workitem", "query", "--jql",
		`project in ("ENG", "PLAT") AND text ~ "a \"quote\" and \\ slash" AND assignee = currentUser() AND statusCategory != Done ORDER BY priority DESC, updated DESC`,
		"--fields", jiraFields, "--limit", "100")

	for _, test := range []struct {
		projects []string
		active   bool
		jql      string
	}{
		{[]string{"ENG"}, true, `project = "ENG" AND assignee = currentUser() AND statusCategory != Done AND sprint in openSprints()`},
		{nil, false, "assignee = currentUser() AND statusCategory != Done"},
	} {
		j := newTestJira(t, test.projects...)
		tasks, err := j.FindMyWork(t.Context(), test.active, 5)
		if err != nil || len(tasks) != 1 {
			t.Fatalf("my work = %#v, err = %v", tasks, err)
		}
		assertArgs(t, trace, "jira", "workitem", "query", "--jql", test.jql+" ORDER BY priority DESC, updated DESC",
			"--fields", jiraFields, "--limit", "5")
	}
}

func TestConfluenceGetAndFind(t *testing.T) {
	trace := fakeTWG(t, printJSON(confluenceRead))
	c := newTestConfluence(t, "DOC")
	doc, err := c.Get(t.Context(), c.ref("DOC", "55"))
	want := provider.Document{
		Ref: c.ref("DOC", "55"), Title: "Bound page", Revision: "8",
		URL: testSite + "/wiki/spaces/DOC/pages/55", Body: "# Full page\n\nBody.",
	}
	if !reflect.DeepEqual(doc, want) || err != nil {
		t.Fatalf("document = %#v, err = %v", doc, err)
	}
	assertArgs(t, trace, "confluence", "content", "get", "55", "--detail", "full", "--format", "md")

	trace = fakeTWG(t, printJSON(confluenceQuery))
	for _, test := range []struct {
		spaces []string
		cql    string
	}{
		{[]string{"DOC"}, `type = page AND space = "DOC" AND text ~ "a \"quote\""`},
		{nil, `type = page AND text ~ "a \"quote\""`},
	} {
		c := newTestConfluence(t, test.spaces...)
		docs, err := c.Find(t.Context(), provider.DocumentQuery{Text: `a "quote"`})
		want.Body, want.Revision = "Bound page", ""
		if err != nil || !reflect.DeepEqual(docs, []provider.Document{want}) {
			t.Fatalf("documents = %#v, err = %v", docs, err)
		}
		assertArgs(t, trace, "confluence", "search", "query", "--cql", test.cql, "--limit", "10")
	}
}

func TestScopeRejectedBeforeStartingTool(t *testing.T) {
	trace := fakeTWG(t, "exit 9")
	j := newTestJira(t, "ENG")
	c := newTestConfluence(t, "DOC")
	_, wrongInstance := j.Get(t.Context(), provider.Ref{Instance: "another", Scope: "ENG", ID: "ENG-42"})
	_, wrongJiraScope := j.Get(t.Context(), j.ref("ENG", "OTHER-42"))
	_, jiraQueryScope := j.Find(t.Context(), provider.TaskQuery{Scope: "OTHER"})
	_, wrongConfluenceScope := c.Get(t.Context(), c.ref("OTHER", "55"))
	_, foreignURL := c.Get(t.Context(), c.ref("DOC", "https://other.atlassian.net/wiki/spaces/DOC/pages/55"))
	_, confluenceQueryScope := c.Find(t.Context(), provider.DocumentQuery{Scope: "DOC\" OR type = page"})
	for _, err := range []error{wrongInstance, wrongJiraScope, jiraQueryScope, wrongConfluenceScope, foreignURL, confluenceQueryScope} {
		if !errors.Is(err, ErrAccessDenied) {
			t.Fatalf("scope rejection = %v", err)
		}
	}
	if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("twg ran for rejected input: %v", err)
	}
	for _, site := range []string{"", "http://example.atlassian.net", "https://user@example.atlassian.net",
		"https://example.atlassian.net:443", "https://example.atlassian.net?x=y", "https://example.atlassian.net/wiki"} {
		if _, err := NewConfluence("wiki", site, []string{"DOC"}); err == nil {
			t.Fatalf("invalid site accepted: %q", site)
		}
	}
}

func TestRejectsUnexpectedIdentityAndShape(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		run           func(*testing.T) error
	}{
		{"jira returned another issue", strings.ReplaceAll(jiraRead, "ENG-42", "ENG-43"), readJiraFixture},
		{"jira returned another site", strings.ReplaceAll(jiraRead, testSite, "https://other.atlassian.net"), readJiraFixture},
		{"jira returned another project", strings.ReplaceAll(jiraQuery, "ENG-42", "OTHER-42"), findJiraFixture},
		{"jira omitted result array", `{"data":{"unknown":[]}}`, findJiraFixture},
		{"confluence wrong id", strings.ReplaceAll(confluenceRead, "55", "56"), readConfluenceFixture},
		{"confluence wrong space", strings.ReplaceAll(confluenceRead, "/DOC/", "/OTHER/"), readConfluenceFixture},
		{"confluence wrong site", strings.ReplaceAll(confluenceRead, testSite, "https://other.atlassian.net"), readConfluenceFixture},
		{"confluence omitted space and link", `{"data":{"id":"55","title":"Page","body":{"value":"text"}}}`, readConfluenceFixture},
		{"confluence body file only", `{"data":{"id":"55","title":"Page","spaceKey":"DOC","body":{"outputFile":"/irrelevant"}}}`, readConfluenceFixture},
		{"confluence nil result list", `{"data":{"results":null}}`, findConfluenceFixture},
		{"confluence search wrong space", strings.ReplaceAll(confluenceQuery, "/DOC/", "/OTHER/"), findConfluenceFixture},
		{"confluence conflicting search urls", strings.Replace(confluenceQuery, `"id":"55"`,
			`"id":"55","url":"https://other.atlassian.net/wiki/spaces/DOC/pages/55"`, 1), findConfluenceFixture},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeTWG(t, printJSON(tc.payload))
			if err := tc.run(t); !errors.Is(err, ErrFailure) {
				t.Fatalf("expected invalid response failure, got %v", err)
			}
		})
	}
}

func readJiraFixture(t *testing.T) error {
	t.Helper()
	j := newTestJira(t, "ENG")
	_, err := j.Get(t.Context(), j.ref("ENG", "ENG-42"))
	return err
}

func findJiraFixture(t *testing.T) error {
	t.Helper()
	_, err := newTestJira(t, "ENG").Find(t.Context(), provider.TaskQuery{})
	return err
}

func readConfluenceFixture(t *testing.T) error {
	t.Helper()
	c := newTestConfluence(t, "DOC")
	_, err := c.Get(t.Context(), c.ref("DOC", "55"))
	return err
}

func findConfluenceFixture(t *testing.T) error {
	t.Helper()
	_, err := newTestConfluence(t, "DOC").Find(t.Context(), provider.DocumentQuery{})
	return err
}

func TestClassifiesToolErrors(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if err := readJiraFixture(t); !errors.Is(err, ErrToolAbsent) {
			t.Fatalf("absence error = %v", err)
		}
	})
	const diagnostic = "synthetic-private-message"
	for _, tc := range []struct {
		name, script string
		want         error
	}{
		{"missing oauth", printJSON(`{"error":{"code":"AUTH_REQUIRED","message":"`+diagnostic+`"}}`) + "; exit 77", ErrAuthRequired},
		{"expired oauth via stderr", printJSON(`{"error":{"code":"AUTH_INVALID"}}`) + " >&2; exit 1", ErrAuthRequired},
		{"documented auth exit", "exit 77", ErrAuthRequired},
		{"denied", printJSON(`{"error":{"code":"FORBIDDEN"}}`) + "; exit 1", ErrAccessDenied},
		{"explicit http denial", printJSON(`{"error":{"code":"AUTH_INVALID","statusCode":403}}`) + "; exit 77", ErrAccessDenied},
		{"explicit not found", printJSON(`{"error":{"statusCode":404}}`) + "; exit 1", provider.ErrNotFound},
		{"partial row failure", printJSON(`{"data":[{"key":"ENG-42","error":{"code":"ACCESS_DENIED"}}]}`) + "; exit 3", ErrAccessDenied},
		{"partial process", printJSON(jiraRead) + "; exit 3", ErrFailure},
		{"partial envelope", printJSON(strings.Replace(jiraRead, `{"data"`, `{"partial":true,"data"`, 1)), ErrFailure},
		{"structured network failure", printJSON(`{"error":{"code":"NETWORK_ERROR","message":"`+diagnostic+`"}}`) + "; exit 1", ErrFailure},
		{"invalid output", printJSON(diagnostic), ErrFailure},
		{"invalid output with auth exit", printJSON(diagnostic) + "; exit 77", ErrFailure},
		{"wrong command", "exit 127", ErrFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeTWG(t, tc.script)
			err := readJiraFixture(t)
			if !errors.Is(err, tc.want) || strings.Contains(fmt.Sprint(err), diagnostic) {
				t.Fatalf("error = %v, want %v without diagnostic body", err, tc.want)
			}
		})
	}
}

func TestCommandDeadline(t *testing.T) {
	fakeTWG(t, "exec /bin/sleep 5")
	j := newTestJira(t)
	start := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := j.Get(ctx, j.ref("ENG", "ENG-42"))
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrFailure) || time.Since(start) > 2*time.Second {
		t.Fatalf("deadline error = %v after %v", err, time.Since(start))
	}
}

func TestOutputBound(t *testing.T) {
	fakeTWG(t, printJSON(jiraRead)+fmt.Sprintf("\n/usr/bin/yes ' ' | /usr/bin/head -c %d", stdoutLimit+1))
	if err := readJiraFixture(t); !errors.Is(err, ErrFailure) {
		t.Fatalf("oversized output error = %v", err)
	}
}

func TestUnsupportedWritesNeverLaunchTWG(t *testing.T) {
	trace := fakeTWG(t, "exit 9")
	j := newTestJira(t, "ENG")
	c := newTestConfluence(t, "DOC")
	opts := provider.WriteOptions{OperationID: "fixture", ExpectedRevision: "1"}
	_, create := j.Create(t.Context(), provider.TaskDraft{}, opts)
	_, transition := j.Transition(t.Context(), j.ref("ENG", "ENG-42"), provider.TaskState{}, opts)
	_, publish := c.Publish(t.Context(), c.ref("DOC", "55"), provider.DocumentDraft{}, opts)
	for _, err := range []error{create, transition, publish} {
		if !errors.Is(err, provider.ErrUnsupported) {
			t.Fatalf("unsupported write error = %v", err)
		}
	}
	if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("twg ran for write: %v", err)
	}
}
