//go:build darwin || linux

package gh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func mockCLI(t *testing.T, host string) *Client {
	t.Helper()
	dir := t.TempDir()
	command := filepath.Join(dir, "gh")
	const script = `#!/bin/sh
set -eu
printf 'invoked\n' >> invocations
printf '%s\n' "$@" > args
test "$1" = api
test -z "${GH_TOKEN+x}${GITHUB_TOKEN+x}${GH_ENTERPRISE_TOKEN+x}${GITHUB_ENTERPRISE_TOKEN+x}"
test -z "${GH_HOST+x}${GH_REPO+x}${GH_FORCE_TTY+x}${GH_DEBUG+x}${DEBUG+x}"
test "$GH_PROMPT_DISABLED" = 1
test "$NO_COLOR" = 1
test "$GH_NO_UPDATE_NOTIFIER" = 1
test "$GH_TELEMETRY" = false
/bin/cat > body
if [ "${DEVFLOW_GH_MOCK_WAIT:-0}" = 1 ]; then
	exec /bin/sleep 10
fi
printf '%s' "$DEVFLOW_GH_MOCK_STDOUT"
printf '%s' "$DEVFLOW_GH_MOCK_STDERR" >&2
exit "$DEVFLOW_GH_MOCK_EXIT"
`
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVFLOW_GH_MOCK_STDERR", "")
	t.Setenv("DEVFLOW_GH_MOCK_WAIT", "0")
	t.Setenv("DEVFLOW_GH_MOCK_STDOUT", "")
	t.Setenv("DEVFLOW_GH_MOCK_EXIT", "0")
	return NewCLI(command, dir, host)
}

func fixtureResponse(t *testing.T, status, headers, body, exit string) {
	t.Helper()
	t.Setenv("DEVFLOW_GH_MOCK_STDOUT", "HTTP/2.0 "+status+"\r\n"+headers+"\r\n"+body)
	t.Setenv("DEVFLOW_GH_MOCK_EXIT", exit)
}

func cliFixtureFile(t *testing.T, client *Client, name string) string {
	t.Helper()
	value, err := os.ReadFile(filepath.Join(client.cli.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}

func assertCLIArgs(t *testing.T, client *Client, fixed, flags []string, endpoint string) {
	t.Helper()
	args := strings.Split(strings.TrimSuffix(cliFixtureFile(t, client, "args"), "\n"), "\n")
	if len(args) != len(fixed)+2*len(flags)+2 || !slices.Equal(args[:len(fixed)], fixed) ||
		!slices.Equal(args[len(args)-2:], []string{"--", endpoint}) {
		t.Fatalf("unexpected gh arguments: %q", args)
	}
	headers := make([]string, 0, len(flags))
	for i := len(fixed); i < len(args)-2; i += 2 {
		if args[i] != "--header" {
			t.Fatalf("unexpected gh flag: %q", args[i])
		}
		headers = append(headers, args[i+1])
	}
	slices.Sort(headers)
	slices.Sort(flags)
	if !slices.Equal(headers, flags) {
		t.Fatalf("headers = %q, want %q", headers, flags)
	}
}

func TestCLIFileRetainsRequestAndResponseHeaders(t *testing.T) {
	client := mockCLI(t, "")
	t.Setenv("PATH", client.cli.dir)
	client = NewCLI("", client.cli.dir, "")
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
		"GH_HOST", "GH_REPO", "GH_FORCE_TTY", "GH_DEBUG", "DEBUG"} {
		t.Setenv(name, "parent-fixture")
	}
	client.Token = "ignored-explicit-token"
	client.API = "http://wrong.example"
	fixtureResponse(t, "200 OK", "ETag: \"fresh\"\r\nContent-Length: 100000\r\n",
		`{"content":"Zml4dHVyZQ==","encoding":"base64"}`, "0")
	data, etag, err := client.File(t.Context(), "acme/repo", "topic/one", "docs/{repo}.md", `"stale"`)
	if err != nil || string(data) != "fixture" || etag != `"fresh"` {
		t.Fatalf("file = %q, %q, %v", data, etag, err)
	}
	assertCLIArgs(t, client, []string{"api", "--hostname", "github.com", "--method", "GET", "--include"},
		[]string{"Accept: application/vnd.github+json", "X-Github-Api-Version: 2022-11-28", `If-None-Match: "stale"`},
		"repos/acme/repo/contents/docs/%7Brepo%7D.md?ref=topic%2Fone")
	if body := cliFixtureFile(t, client, "body"); body != "" {
		t.Fatalf("unexpected read input: %q", body)
	}

	fixtureResponse(t, "304 Not Modified", "ETag: \"fresh\"\r\n", "", "1")
	if _, _, err := client.File(t.Context(), "acme/repo", "topic/one", "README.md", etag); !errors.Is(err, ErrNotModified) {
		t.Fatalf("file error = %v, want ErrNotModified", err)
	}
}

func TestCLIRESTAndGraphQLWrites(t *testing.T) {
	client := mockCLI(t, "GitHub.Example.com")
	fixtureResponse(t, "201 Created", "Content-Type: application/json\r\n", `{"number":42}`, "0")
	pr, err := client.CreatePR(t.Context(), "acme/repo", "Title", "Body", "topic", "main", true)
	if err != nil || pr.Number != 42 {
		t.Fatalf("create pull request = %+v, %v", pr, err)
	}
	fixed := []string{"api", "--hostname", "github.example.com", "--method", "POST", "--include", "--input", "-"}
	headers := []string{"Accept: application/vnd.github+json", "Content-Type: application/json", "X-Github-Api-Version: 2022-11-28"}
	assertCLIArgs(t, client, fixed, headers, "repos/acme/repo/pulls")
	want := map[string]any{"title": "Title", "body": "Body", "head": "topic", "base": "main", "draft": true}
	var payload map[string]any
	if err := json.Unmarshal([]byte(cliFixtureFile(t, client, "body")), &payload); err != nil || !reflect.DeepEqual(payload, want) {
		t.Fatalf("pull request input = %#v, %v", payload, err)
	}

	fixtureResponse(t, "200 OK", "", `{"data":{"addPullRequestReviewThreadReply":{"comment":{"id":"reply"}}}}`, "0")
	if err := client.ReplyToThread(t.Context(), "thread", "A review reply"); err != nil {
		t.Fatal(err)
	}
	assertCLIArgs(t, client, fixed, headers, "graphql")
	var gql struct {
		Query     string
		Variables map[string]string
	}
	if err := json.Unmarshal([]byte(cliFixtureFile(t, client, "body")), &gql); err != nil ||
		!strings.HasPrefix(gql.Query, "mutation(") || gql.Variables["id"] != "thread" || gql.Variables["body"] != "A review reply" {
		t.Fatalf("graphql input = %+v, %v", gql, err)
	}

	fixtureResponse(t, "204 No Content", "", "", "0")
	if err := client.Do(t.Context(), http.MethodDelete, "/repos/acme/repo/issues/42/assignees", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCLIRESTFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		status  int
		headers string
		want    error
	}{
		{http.StatusUnauthorized, "", ErrAuthentication},
		{http.StatusForbidden, "X-GitHub-SSO: required\r\n", ErrPermission},
		{http.StatusForbidden, "X-RateLimit-Remaining: 0\r\n", ErrRateLimited},
		{http.StatusNotFound, "", nil},
		{http.StatusUnprocessableEntity, "", nil},
		{http.StatusTooManyRequests, "Retry-After: 60\r\n", ErrRateLimited},
		{http.StatusRequestTimeout, "", ErrWriteOutcomeUnknown},
		{http.StatusBadGateway, "", ErrWriteOutcomeUnknown},
	} {
		t.Run(fmt.Sprintf("%d %s", tc.status, strings.TrimSpace(tc.headers)), func(t *testing.T) {
			client := mockCLI(t, "")
			fixtureResponse(t, fmt.Sprintf("%d %s", tc.status, http.StatusText(tc.status)), "X-GitHub-Request-Id: request-id\r\n"+tc.headers,
				`{"message":"request failed"}`, "1")
			const path = "/repos/acme/repo/issues"
			err := client.Do(t.Context(), http.MethodPost, path, map[string]string{"title": "Example"}, nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status || apiErr.Header.Get("X-GitHub-Request-Id") != "request-id" ||
				!strings.HasPrefix(apiErr.Error(), fmt.Sprintf("github POST %s: HTTP %d: ", path, tc.status)) {
				t.Fatalf("unexpected API error: %#v (%v)", apiErr, err)
			}
			for _, kind := range []error{ErrCLIUnavailable, ErrAuthentication, ErrPermission, ErrRateLimited, ErrWriteOutcomeUnknown} {
				if errors.Is(err, kind) != errors.Is(tc.want, kind) {
					t.Fatalf("errors.Is(%v, %v) = %t", err, kind, errors.Is(err, kind))
				}
			}
			if calls := cliFixtureFile(t, client, "invocations"); calls != "invoked\n" {
				t.Fatalf("unexpected attempts: %q", calls)
			}
		})
	}
}

func TestCLIGraphQLErrorEnvelopeOnNonZeroExit(t *testing.T) {
	client := mockCLI(t, "")
	fixtureResponse(t, "200 OK", "Content-Type: application/json\r\n",
		`{"data":null,"errors":[{"type":"FORBIDDEN","message":"read:org scope required"}]}`, "1")
	var out map[string]any
	err := client.GraphQL(t.Context(), "query{viewer{login}}", nil, &out)
	if !errors.Is(err, ErrPermission) || err.Error() != "github graphql: read:org scope required" {
		t.Fatalf("unexpected GraphQL error: %v", err)
	}
}

func TestCLIProcessFailures(t *testing.T) {
	for _, tc := range []struct {
		name, method, stdout, exit string
		want                       error
	}{
		{"missing login", http.MethodPost, "", "4", ErrAuthentication},
		{"network failure", http.MethodGet, "", "1", nil},
		{"ambiguous write", http.MethodPost, "", "1", ErrWriteOutcomeUnknown},
		{"malformed output", http.MethodPost, "not an included response", "0", ErrWriteOutcomeUnknown},
		{"invalid json", http.MethodPost, "HTTP/2.0 201 Created\r\n\r\n{", "0", ErrWriteOutcomeUnknown},
		{"late process failure", http.MethodPost, "HTTP/2.0 201 Created\r\n\r\n{}", "1", ErrWriteOutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := mockCLI(t, "")
			t.Setenv("DEVFLOW_GH_MOCK_STDOUT", tc.stdout)
			t.Setenv("DEVFLOW_GH_MOCK_EXIT", tc.exit)
			t.Setenv("DEVFLOW_GH_MOCK_STDERR", "sensitive diagnostic fixture")
			var out map[string]any
			err := client.Do(t.Context(), tc.method, "/repos/acme/repo/issues", nil, &out)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) ||
				strings.Contains(err.Error(), "sensitive diagnostic fixture") || errors.Is(err, ErrCLIUnavailable) {
				t.Fatalf("unexpected CLI error: %v", err)
			}
			if calls := cliFixtureFile(t, client, "invocations"); calls != "invoked\n" {
				t.Fatalf("unexpected attempts: %q", calls)
			}
		})
	}
}

func TestCLICallerDeadline(t *testing.T) {
	client := mockCLI(t, "")
	t.Setenv("DEVFLOW_GH_MOCK_WAIT", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err := client.Do(ctx, http.MethodPost, "/repos/acme/repo/issues", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("deadline error = %v", err)
	}
}

func TestCLIRejectsUnscopedRequests(t *testing.T) {
	client := mockCLI(t, "")
	for _, path := range []string{"https://example.com/user", "//example.com/user", "/https://example.com/user",
		"/search/issues?q=https://example.com", "/\\example.com/user", "/user#fragment", "/user%xx", "/user\n"} {
		if err := client.Do(t.Context(), http.MethodGet, path, nil, nil); err == nil {
			t.Errorf("accepted path %q", path)
		}
	}
	for _, host := range []string{"https://github.com", "github.com:443", "a@github.com", "github.com/user",
		"github.localhost", "github.com?other.example", "--hostname"} {
		unscoped := NewCLI(client.cli.command, client.cli.dir, host)
		if err := unscoped.Do(t.Context(), http.MethodGet, "/user", nil, nil); err == nil {
			t.Errorf("accepted host %q", host)
		}
	}
	if _, err := client.do(t.Context(), http.MethodGet, "/user", nil, nil, map[string]string{"authorization": "ignored"}); err == nil {
		t.Error("accepted authorization override")
	}
	if _, err := os.Stat(filepath.Join(client.cli.dir, "invocations")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected process invocation: %v", err)
	}
}

func TestCLIMissingExecutable(t *testing.T) {
	client := NewCLI(filepath.Join(t.TempDir(), "no-gh"), "", "")
	_, err := client.Viewer(t.Context())
	if !errors.Is(err, ErrCLIUnavailable) || errors.Is(err, ErrAuthentication) {
		t.Fatalf("unexpected launch error: %v", err)
	}
}
