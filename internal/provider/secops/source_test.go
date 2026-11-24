package secops_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/nscaledev/agentflow/internal/gh"
	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/secops"
)

var _ secops.GitHubClient = (*gh.Client)(nil)

type githubCall func(context.Context, string, string, any, any) error

func (call githubCall) Do(ctx context.Context, method, path string, in, out any) error {
	return call(ctx, method, path, in, out)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func githubSource(t *testing.T, kind secops.GitHubAlertSource) provider.Findings {
	t.Helper()
	data := fixture(t, string(kind))
	client := githubCall(func(_ context.Context, method, path string, in, out any) error {
		t.Helper()
		wantPath := "/repos/example/project/" + string(kind) + "/alerts?state=open&per_page=100"
		if method != http.MethodGet || path != wantPath || in != nil {
			t.Fatalf("request = %s %s %v, want GET %s with no body", method, path, in, wantPath)
		}
		return json.Unmarshal(data, out)
	})
	source, err := secops.NewGitHubFindings(client, " github-security ", " Example/Project ", kind)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestGitHubFindings(t *testing.T) {
	cutoff := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		kind       secops.GitHubAlertSource
		want       provider.Finding
		secondRank string
	}{
		{
			kind: secops.Dependabot,
			want: provider.Finding{
				Ref:   provider.Ref{Instance: "github-security", Scope: "example/project", ID: "dependabot:7"},
				Title: "Update the parser dependency", Severity: "high",
				URL: "https://github.com/Example/Project/security/dependabot/7",
			},
			secondRank: "medium",
		},
		{
			kind: secops.CodeScanning,
			want: provider.Finding{
				Ref:   provider.Ref{Instance: "github-security", Scope: "example/project", ID: "code-scanning:7"},
				Title: "Review input validation", Severity: "critical",
				URL: "https://github.com/Example/Project/security/code-scanning/7?ref=main",
			},
			secondRank: "medium",
		},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			source := githubSource(t, tc.kind)
			all, err := source.Findings(t.Context(), time.Time{})
			if err != nil || len(all) != 2 || all[0] != tc.want || all[1].Severity != tc.secondRank {
				t.Fatalf("all findings = %+v, %v", all, err)
			}
			recent, err := source.Findings(t.Context(), cutoff)
			if err != nil || !slices.Equal(recent, []provider.Finding{tc.want}) {
				t.Fatalf("recent findings = %+v, %v", recent, err)
			}
		})
	}
}

func TestGitHubFindingsErrors(t *testing.T) {
	apiError := errors.New("fixture API failure")
	client := githubCall(func(context.Context, string, string, any, any) error { return apiError })
	if _, err := secops.NewGitHubFindings(client, "github-security", "org/../repo", secops.Dependabot); err == nil {
		t.Fatal("accepted an invalid repository")
	}
	source, err := secops.NewGitHubFindings(client, "github-security", "org/repo", secops.Dependabot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Findings(t.Context(), time.Time{}); !errors.Is(err, apiError) {
		t.Fatalf("findings error = %v, want %v", err, apiError)
	}
}

func TestFixtureIncidents(t *testing.T) {
	source, err := secops.NewFixtureIncidents("response", "  Team A ", fixture(t, "incidents"))
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Incident{
		Ref:   provider.Ref{Instance: "response", Scope: "Team A", ID: "INC-7"},
		Title: "Service restoration", Body: "The response team restored the service.", Severity: "high",
		URL: "https://status.example.test/incidents/7",
	}
	all, err := source.Incidents(t.Context(), time.Time{})
	if err != nil || len(all) != 2 || all[0] != want || all[1].Ref.ID != "INC-4" {
		t.Fatalf("all incidents = %+v, %v", all, err)
	}
	all[0].Ref.ID = "mutated"
	cutoff := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	recent, err := source.Incidents(t.Context(), cutoff)
	if err != nil || !slices.Equal(recent, []provider.Incident{want}) {
		t.Fatalf("recent incidents = %+v, %v", recent, err)
	}
	if _, err := secops.NewFixtureIncidents("response", "Team A", []byte("null")); err == nil {
		t.Fatal("accepted a non-array fixture")
	}
}
