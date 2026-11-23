package twg

import (
	"errors"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/provider/native"
)

// twg 1.4.6 emits the create request alongside Jira's REST v3 comment object.
const commentReceipt = `{"command":"jira.workitem.comment.create","request":{"site":"https://example.atlassian.net","issueId":"10042"},"data":{"id":"101","author":{"accountId":"fixture-user"},"body":{"version":1,"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"--literal '<tag>' & $"}]}]}}}`

const oauthProfile = `{"data":{"storage":"keychain","activeEnvironment":"prod","defaults":{"prod":"fixture-id"},"profiles":[{"id":"fixture-id","name":"work","authMethod":"oauth","environment":"prod","userId":"fixture-user","site":"example"}]}}`

func jiraWriteTarget(t *testing.T, result string) string {
	t.Helper()
	return fakeTWG(t, `if [ "$1 $2 $3" = "auth profile list" ]; then
`+printJSON(oauthProfile)+"\nexit 0\nfi\n"+printJSON(result))
}

func TestJiraCommentCommand(t *testing.T) {
	const text = "--literal '<tag>' & $"
	j := newTestJira(t, "ENG")
	trace := jiraWriteTarget(t, `{"data":[{"id":"10042","key":"ENG-42","project":{"key":"ENG"}}]}`)
	p, err := j.PrepareComment(t.Context(), j.ref("eng", "eng-42"), text)
	if err != nil {
		t.Fatal(err)
	}
	assertArgs(t, trace, "jira", "workitem", "get", "ENG-42", "--fields", "project", "--profile", "work")
	t.Setenv("TWG_USER", "fixture")
	t.Setenv("TWG_TOKEN", "fixture")
	t.Setenv("TWG_SITE", "foreign.example")
	t.Setenv("TWG_PROFILE", "other")
	t.Setenv("JIRA_TOKEN", "fixture")
	trace = fakeTWG(t, `test "$CI" = true
test -z "${TWG_USER+x}${TWG_TOKEN+x}${TWG_SITE+x}${TWG_PROFILE+x}${JIRA_TOKEN+x}"
`+printJSON(commentReceipt))
	id, err := p.Apply(t.Context())
	if err != nil || id != "101" {
		t.Fatal(id, err)
	}
	assertArgs(t, trace, "jira", "workitem", "comment", "create", "--issue-id", "10042", "--body="+text, "--body-format", "plain", "--profile", "work")
	for _, tc := range []struct{ name, old, replacement string }{
		{"different issue", "10042", "10043"},
		{"different site", testSite, "https://foreign.example"},
		{"different body", text, "different"},
		{"different account", "fixture-user", "other-user"},
		{"missing comment id", `"101"`, `""`},
		{"partial metadata", `"command":`, `"meta":{"partial":true},"command":`},
		{"partial acknowledgement", `"command":`, `"partial":true,"command":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeTWG(t, printJSON(strings.Replace(commentReceipt, tc.old, tc.replacement, 1)))
			id, err := p.Apply(t.Context())
			if id != "" || !errors.Is(err, native.ErrWriteUncertain) {
				t.Fatal(id, err)
			}
		})
	}
	fakeTWG(t, printJSON(`{"error":{"code":"ACCESS_DENIED"}}`)+"\nexit 1")
	if _, err := p.Apply(t.Context()); !errors.Is(err, native.ErrWriteUncertain) || !errors.Is(err, ErrAccessDenied) {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := p.Apply(t.Context()); !errors.Is(err, native.ErrWriteNotDispatched) || errors.Is(err, native.ErrWriteUncertain) {
		t.Fatal(err)
	}
}

func TestJiraCommentTargetAndLiteralText(t *testing.T) {
	j := newTestJira(t, "ENG")
	ref := j.ref("ENG", "ENG-42")
	for _, row := range []string{
		`{"id":"10042","key":"ENG-43","project":{"key":"ENG"}}`,
		`{"id":"10042","key":"ENG-42","project":{"key":"OTHER"}}`,
		`{"id":"10042","key":"ENG-42"}`,
		`{"key":"ENG-42","project":{"key":"ENG"}}`,
		`{"id":"10042","key":"ENG-42","project":{"key":"ENG"},"url":"https://foreign.example/browse/ENG-42"}`,
	} {
		jiraWriteTarget(t, `{"data":[`+row+`]}`)
		if p, err := j.PrepareComment(t.Context(), ref, "test"); p != nil || !errors.Is(err, ErrAccessDenied) {
			t.Fatal(row, p, err)
		}
	}
	jiraWriteTarget(t, `{"data":[{"id":"10042","key":"ENG-42","project":{"key":"ENG"}}]}`)
	for _, body := range []string{"", " ends ", "starts\uFEFF", "NUL\x00", "[~accountId:123456789012345678901234]"} {
		if p, err := j.PrepareComment(t.Context(), ref, body); p != nil || err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
}

func TestJiraCommentSelectsSiteProfile(t *testing.T) {
	j := newTestJira(t, "ENG")
	foreign := `{"id":"foreign-id","name":"foreign","authMethod":"oauth","environment":"prod","userId":"foreign-user","site":"foreign"}`
	target := `{"id":"local-id","name":"by-site","authMethod":"oauth","environment":"prod","userId":"fixture-user","sites":[{"site":"example"}]}`
	catalog := func(profiles string) string {
		return `{"data":{"storage":"file","activeEnvironment":"prod","defaults":{"prod":"foreign-id"},"profiles":[` + profiles + `]}}`
	}
	fakeTWG(t, printJSON(catalog(foreign+","+target)))
	profile, selector, err := j.jiraProfile(t.Context())
	if err != nil || selector != "by-site" || profile.UserID != "fixture-user" {
		t.Fatal(selector, err)
	}
	fakeTWG(t, printJSON(catalog(foreign)))
	if _, _, err := j.jiraProfile(t.Context()); !errors.Is(err, ErrAuthRequired) {
		t.Fatal("foreign profile selected", err)
	}
	fakeTWG(t, printJSON(catalog(foreign+","+target+","+strings.ReplaceAll(target, "local-id", "other-id"))))
	if _, _, err := j.jiraProfile(t.Context()); !errors.Is(err, ErrAccessDenied) {
		t.Fatal("ambiguous site profile selected", err)
	}
}
