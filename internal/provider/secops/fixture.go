package secops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nscaledev/agentflow/internal/provider"
)

// FixtureIncidents serves a fixed set of incidents from an in-memory fixture.
type FixtureIncidents struct {
	incidents []fixtureIncident
}

type fixtureIncident struct {
	incident provider.Incident
	at       time.Time
}

var _ provider.Incidents = (*FixtureIncidents)(nil)

// NewFixtureIncidents reads an array of incidents with id, title, severity,
// url, and optional body and updated_at fields. Malformed rows are skipped.
func NewFixtureIncidents(instance, scope string, data []byte) (*FixtureIncidents, error) {
	ref := provider.Ref{Instance: strings.TrimSpace(instance), Scope: strings.TrimSpace(scope), ID: "fixture"}
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("invalid fixture incident source: %w", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("decode incident fixture: %w", err)
	}
	if entries == nil {
		return nil, errors.New("incident fixture requires an array")
	}

	source := &FixtureIncidents{incidents: make([]fixtureIncident, 0, len(entries))}
	for _, entry := range entries {
		var row struct {
			ID        string    `json:"id"`
			Title     string    `json:"title"`
			Body      string    `json:"body"`
			Severity  string    `json:"severity"`
			URL       string    `json:"url"`
			UpdatedAt time.Time `json:"updated_at"`
		}
		if err := json.Unmarshal(entry, &row); err != nil {
			continue
		}
		ref.ID = strings.TrimSpace(row.ID)
		event := Event{Kind: IncidentEvent, Ref: ref, Title: row.Title, Body: row.Body, Severity: row.Severity, URL: row.URL}
		if err := event.normalize(); err == nil {
			source.incidents = append(source.incidents, fixtureIncident{
				incident: provider.Incident{
					Ref: event.Ref, Title: event.Title, Body: event.Body, Severity: event.Severity, URL: event.URL,
				},
				at: row.UpdatedAt,
			})
		}
	}
	return source, nil
}

// Incidents returns a fresh slice of incidents updated at or after since.
// A zero cutoff includes incidents without timestamps.
func (f *FixtureIncidents) Incidents(ctx context.Context, since time.Time) ([]provider.Incident, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]provider.Incident, 0, len(f.incidents))
	for _, record := range f.incidents {
		if atOrAfter(record.at, since) {
			out = append(out, record.incident)
		}
	}
	return out, nil
}
