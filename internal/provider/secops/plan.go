// Package secops reads security events and plans tasks from them.
package secops

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/nscaledev/agentflow/internal/provider"
)

type EventKind string

const (
	FindingEvent  EventKind = "finding"
	IncidentEvent EventKind = "incident"
)

// Event supplies normalized source data to the caller's task renderer.
type Event struct {
	Kind     EventKind
	Ref      provider.Ref
	Title    string
	Body     string
	Severity string
	URL      string
}

// TaskTarget identifies the caller's selected Tasks instance and its scope.
type TaskTarget struct {
	Instance string
	Scope    string
}

// TaskRenderer renders a draft from an event using the caller's selected
// template. A nil renderer copies the event title and body.
type TaskRenderer func(Event) (provider.TaskDraft, error)

// TaskPlan contains the inputs for provider.Tasks.Create.
type TaskPlan struct {
	Target  TaskTarget
	Draft   provider.TaskDraft
	Options provider.WriteOptions
}

func PlanFinding(finding provider.Finding, target TaskTarget, render TaskRenderer) (TaskPlan, error) {
	return planTask(Event{
		Kind: FindingEvent, Ref: finding.Ref, Title: finding.Title,
		Body: finding.Body, Severity: finding.Severity, URL: finding.URL,
	}, target, render)
}

func PlanIncident(incident provider.Incident, target TaskTarget, render TaskRenderer) (TaskPlan, error) {
	return planTask(Event{
		Kind: IncidentEvent, Ref: incident.Ref, Title: incident.Title,
		Body: incident.Body, Severity: incident.Severity, URL: incident.URL,
	}, target, render)
}

func planTask(event Event, target TaskTarget, render TaskRenderer) (TaskPlan, error) {
	if err := event.normalize(); err != nil {
		return TaskPlan{}, fmt.Errorf("plan %s task: %w", event.Kind, err)
	}
	// Keep the operation key stable when event details or the selected
	// template change. Include the destination so each route can replay.
	key := provider.Ref{
		Instance: target.Instance,
		Scope:    target.Scope,
		ID:       string(event.Kind) + ":" + event.Ref.String(),
	}
	if err := key.Validate(); err != nil {
		return TaskPlan{}, fmt.Errorf("invalid task target: %w", err)
	}

	draft := provider.TaskDraft{Title: event.Title, Description: event.Body}
	if render != nil {
		var err error
		draft, err = render(event)
		if err != nil {
			return TaskPlan{}, fmt.Errorf("render %s task: %w", event.Kind, err)
		}
		draft.Title = singleLine(draft.Title)
		if draft.Title == "" {
			return TaskPlan{}, errors.New("rendered task requires a title")
		}
	}
	draft.Scope = target.Scope
	draft.Labels = slices.Clone(draft.Labels)
	source := provider.Link{Title: "Source " + string(event.Kind), URL: event.URL, Ref: &event.Ref}
	draft.Links = append(slices.Clone(draft.Links), source)
	return TaskPlan{
		Target:  target,
		Draft:   draft,
		Options: provider.WriteOptions{OperationID: "secops-" + key.StorageKey()},
	}, nil
}

func (e *Event) normalize() error {
	if err := e.Ref.Validate(); err != nil {
		return fmt.Errorf("invalid source: %w", err)
	}
	e.Title = singleLine(e.Title)
	if e.Title == "" {
		return errors.New("security event requires a title")
	}
	e.Body = strings.TrimSpace(e.Body)
	e.Severity = normalizeSeverity(e.Severity)
	if e.Severity == "" {
		return errors.New("security event requires a supported severity")
	}
	e.URL = normalizeURL(e.URL)
	if e.URL == "" {
		return errors.New("security event requires an absolute http or https url")
	}
	return nil
}

func singleLine(value string) string {
	line := strings.Join(strings.Fields(value), " ")
	if strings.ContainsFunc(line, unicode.IsControl) {
		return ""
	}
	return line
}

func normalizeSeverity(value string) string {
	switch severity := strings.ToLower(strings.TrimSpace(value)); severity {
	case "critical", "high", "medium", "low":
		return severity
	case "moderate":
		return "medium"
	default:
		return ""
	}
}

func normalizeURL(value string) string {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Hostname() == "" || u.User != nil {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	switch u.Scheme {
	case "https":
		u.Host = strings.TrimSuffix(u.Host, ":443")
	case "http":
		u.Host = strings.TrimSuffix(u.Host, ":80")
	default:
		return ""
	}
	return u.String()
}
