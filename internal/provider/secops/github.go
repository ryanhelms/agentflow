package secops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/provider"
)

// GitHubClient is the REST subset of gh.Client used by the alert sources.
type GitHubClient interface {
	Do(ctx context.Context, method, path string, in, out any) error
}

type GitHubAlertSource string

const (
	Dependabot   GitHubAlertSource = "dependabot"
	CodeScanning GitHubAlertSource = "code-scanning"
)

// GitHubFindings reads the first page of up to 100 open alerts from one
// GitHub alert source. Each source can use its own authenticated client.
type GitHubFindings struct {
	client     GitHubClient
	instance   string
	repository string
	source     GitHubAlertSource
}

var _ provider.Findings = (*GitHubFindings)(nil)

var githubRepoComponent = regexp.MustCompile(`^[a-z0-9_.-]+$`)

func NewGitHubFindings(client GitHubClient, instance, repository string, source GitHubAlertSource) (*GitHubFindings, error) {
	if client == nil {
		return nil, errors.New("github findings require a client")
	}
	if source != Dependabot && source != CodeScanning {
		return nil, fmt.Errorf("unsupported github alert source %q", source)
	}
	instance = strings.TrimSpace(instance)
	repository = strings.ToLower(strings.TrimSpace(repository))
	owner, name, ok := strings.Cut(repository, "/")
	if !ok || !validGitHubComponent(owner) || !validGitHubComponent(name) {
		return nil, errors.New("github findings require an owner/name repository")
	}
	if err := (provider.Ref{Instance: instance, Scope: repository, ID: string(source)}).Validate(); err != nil {
		return nil, fmt.Errorf("invalid github findings source: %w", err)
	}
	return &GitHubFindings{client: client, instance: instance, repository: repository, source: source}, nil
}

func validGitHubComponent(value string) bool {
	return value != "." && value != ".." && githubRepoComponent.MatchString(value)
}

type githubAlert struct {
	Number    int64     `json:"number"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Advisory  struct {
		Summary  string `json:"summary"`
		Severity string `json:"severity"`
	} `json:"security_advisory"`
	Rule struct {
		Description string `json:"description"`
		Severity    string `json:"security_severity_level"`
	} `json:"rule"`
}

// Findings returns alerts at or after since, using their update time when
// present and otherwise their creation time. A zero cutoff includes alerts
// without timestamps.
func (g *GitHubFindings) Findings(ctx context.Context, since time.Time) ([]provider.Finding, error) {
	path := "/repos/" + g.repository + "/" + string(g.source) + "/alerts?state=open&per_page=100"
	var entries []json.RawMessage
	if err := g.client.Do(ctx, http.MethodGet, path, nil, &entries); err != nil {
		return nil, fmt.Errorf("list github %s alerts: %w", g.source, err)
	}
	if entries == nil {
		return nil, fmt.Errorf("github %s alerts response requires an array", g.source)
	}

	findings := make([]provider.Finding, 0, len(entries))
	for _, entry := range entries {
		var alert githubAlert
		if err := json.Unmarshal(entry, &alert); err != nil || alert.Number <= 0 {
			continue
		}
		if alert.State != "" && alert.State != "open" {
			continue
		}
		at := alert.UpdatedAt
		if at.IsZero() {
			at = alert.CreatedAt
		}
		if !atOrAfter(at, since) {
			continue
		}

		event := Event{
			Kind: FindingEvent,
			Ref: provider.Ref{
				Instance: g.instance, Scope: g.repository,
				ID: string(g.source) + ":" + strconv.FormatInt(alert.Number, 10),
			},
			URL: alert.HTMLURL,
		}
		if g.source == Dependabot {
			event.Title, event.Severity = alert.Advisory.Summary, alert.Advisory.Severity
		} else {
			event.Title, event.Severity = alert.Rule.Description, alert.Rule.Severity
		}
		if err := event.normalize(); err == nil {
			findings = append(findings, provider.Finding{
				Ref: event.Ref, Title: event.Title, Severity: event.Severity, URL: event.URL,
			})
		}
	}
	return findings, nil
}

func atOrAfter(at, since time.Time) bool {
	return since.IsZero() || (!at.IsZero() && !at.Before(since))
}
