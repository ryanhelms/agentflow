package app

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider/native"
)

func TestJiraCommentCLIRouting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("twg fixture uses /bin/sh")
	}
	a, host, ctx := jiraNativeApp(t)
	for _, name := range []string{"JIRA_USER", "JIRA_TOKEN", "CI", "GITHUB_ACTIONS", "DEVFLOW_OPERATION_ID"} {
		t.Setenv(name, "")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	exe := filepath.Join(dir, "twg")
	const issue = `{"data":[{"id":"10001","key":"PLAT-1","project":{"key":"PLAT"}}]}`
	writeFile := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("issue", issue)
	const profile = `{"data":{"storage":"keychain","activeEnvironment":"prod","defaults":{"prod":"work-id"},"profiles":[{"id":"work-id","name":"work","environment":"prod","authMethod":"oauth","site":"https://tasks.example","userId":"fixture-account"}]}}`
	writeFile("profile", profile)
	writeFile("receipt", `{"command":"jira.workitem.comment.create","request":{"site":"https://tasks.example","issueId":"10001"},"data":{"id":"102","author":{"accountId":"fixture-account"},"body":{"version":1,"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Ready\n\n(via devflow)"}]}]}}}`)
	writeFile("twg", fmt.Sprintf(`#!/bin/sh
set -eu
cd %q
case "$3" in
list) echo profile >> reads; exec /bin/cat profile ;;
get) echo get >> reads; exec /bin/cat issue ;;
comment)
  test "$4 $5 $6" = 'create --issue-id 10001'
  echo create >> writes
  exec /bin/cat receipt ;;
*) exit 1 ;;
esac
`, dir))
	a.Gate.Autonomy.Remote = config.Auto
	ctx = WithJiraCommentCLI(ctx)
	host.prepareErr = native.ErrDenied
	if _, err := a.Comment(ctx, "PLAT-1", "Ready", ""); !errors.Is(err, native.ErrDenied) {
		t.Fatal("MCP denial", err)
	}
	host.prepareErr = nil
	host.missing = "addOrEditJiraIssueComment"
	if _, err := a.Comment(ctx, "OTHER-1", "Ready", ""); !errors.Is(err, native.ErrDenied) {
		t.Fatal("scope denial", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "reads")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected CLI preflight", err)
	}
	preview := func(text string) string {
		t.Helper()
		out, err := a.Comment(ctx, "PLAT-1", text, "")
		if err != nil {
			t.Fatal(err)
		}
		token := confirmation(t, out)
		summary := out.(*Pending).Summary.(map[string]string)
		if summary["backend"] != "twg" || summary["site"] != "https://tasks.example" || summary["issue_id"] != "10001" ||
			summary["key"] != "PLAT-1" || summary["project"] != "PLAT" || summary["comment"] != text+"\n\n(via devflow)" {
			t.Fatal(summary)
		}
		return token
	}
	token := preview("Ready")
	reject := func(text string) {
		t.Helper()
		if _, err := a.Comment(ctx, "PLAT-1", text, token); !errors.Is(err, policy.ErrToken) {
			t.Fatal("stale approval", err)
		}
	}
	reject("Edited")
	writeFile("profile", strings.ReplaceAll(profile, "work-id", "new-id"))
	reject("Ready")
	writeFile("profile", strings.Replace(profile, "oauth", "pat", 1))
	if _, err := a.Comment(ctx, "PLAT-1", "Ready", ""); !errors.Is(err, native.ErrSignIn) {
		t.Fatal("non-OAuth default profile", err)
	}
	writeFile("profile", profile)
	writeFile("issue", strings.Replace(issue, "10001", "10002", 1))
	reject("Ready")
	writeFile("issue", issue)
	host.missing = ""
	reject("Ready")
	host.missing = "addOrEditJiraIssueComment"
	if err := os.Rename(exe, exe+".absent"); err != nil {
		t.Fatal(err)
	}
	reject("Ready")
	if err := os.Rename(exe+".absent", exe); err != nil {
		t.Fatal(err)
	}
	for code, want := range map[string]error{"ACCESS_DENIED": native.ErrDenied, "AUTH_REQUIRED": native.ErrSignIn} {
		writeFile("issue", fmt.Sprintf(`{"error":{"code":%q}}`, code))
		if _, err := a.Comment(ctx, "PLAT-1", "Ready", ""); !errors.Is(err, want) {
			t.Fatal(code, err)
		}
	}
	writeFile("issue", issue)
	if _, err := os.Stat(filepath.Join(dir, "writes")); !errors.Is(err, os.ErrNotExist) || len(host.writes) != 0 {
		t.Fatal("preview or rejected approval dispatched", err)
	}
	out, err := a.Comment(ctx, "PLAT-1", "Ready", token)
	if err != nil || out.(map[string]any)["comment_id"] != "102" {
		t.Fatal(out, err)
	}
	restartGate := func() {
		t.Helper()
		gate, err := policy.NewGate(a.Home, a.Gate.Autonomy, a.Client)
		if err != nil {
			t.Fatal(err)
		}
		a.Gate = gate
	}
	restartGate()
	reject("Ready")
	token = preview("Another")
	writeFile("receipt", "{")
	if _, err := a.Comment(ctx, "PLAT-1", "Another", token); !errors.Is(err, native.ErrWriteUncertain) {
		t.Fatal("uncertain delivery", err)
	}
	restartGate()
	reject("Another")
	trace, err := os.ReadFile(filepath.Join(dir, "writes"))
	if err != nil || string(trace) != "create\ncreate\n" || len(host.writes) != 0 {
		t.Fatal("unexpected dispatch count", string(trace), err)
	}
}

func TestJiraCommentCLIEligibility(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("twg fixture uses /bin/sh")
	}
	posts := 0
	a := legacyApp(t, func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		if !ok || user != "fixture" || secret != "fixture" || r.Method != "POST" ||
			r.URL.String() != "https://tasks.example/rest/api/3/issue/PLAT-1/comment" {
			t.Fatalf("unexpected fixture REST request %s %s", r.Method, r.URL.Path)
		}
		posts++
		w.WriteHeader(http.StatusCreated)
	})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "twg"), []byte("#!/bin/sh\nexit 15\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, tc := range []struct {
		name, ci, actions, user, token string
		optIn                          bool
	}{
		{"unattended", "", "", "", "", false},
		{"CI", "true", "", "", "", true},
		{"numeric CI", "1", "", "", "", true},
		{"actions", "", "true", "", "", true},
		{"operation ID", "", "", "", "", true},
		{"service account", "", "", "fixture", "fixture", true},
		{"interactive but twg absent", "", "", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CI", tc.ci)
			t.Setenv("GITHUB_ACTIONS", tc.actions)
			t.Setenv("JIRA_USER", tc.user)
			t.Setenv("JIRA_TOKEN", tc.token)
			t.Setenv("DEVFLOW_OPERATION_ID", "")
			if tc.name == "operation ID" {
				t.Setenv("DEVFLOW_OPERATION_ID", "fixture-automation")
			}
			callCtx := t.Context()
			if tc.optIn {
				callCtx = WithJiraCommentCLI(callCtx)
			}
			if tc.name == "interactive but twg absent" {
				t.Setenv("PATH", t.TempDir())
			}
			out, err := a.Comment(callCtx, "PLAT-1", "Ready", "")
			if err != nil || out.(*Pending).Summary.(map[string]string)["backend"] != "" {
				t.Fatal("legacy preview", out, err)
			}
			if tc.name == "service account" {
				out, err = a.Comment(callCtx, "PLAT-1", "Ready", confirmation(t, out))
				if err != nil || out.(map[string]any)["commented"] != true || posts != 1 {
					t.Fatal("headless service write", out, err, posts)
				}
			}
		})
	}
}
