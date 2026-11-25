// Package asana implements the provider task contract against Asana projects.
package asana

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/nscaledev/agentflow/internal/provider"
)

// These state IDs represent tasks without a section and completed tasks.
// An incomplete task in a section uses the section GID as its state ID.
const (
	StateOpen      = "open"
	StateCompleted = "completed"
)

const taskFields = "gid,name,notes,permalink_url,resource_subtype,assignee.gid," +
	"completed,memberships.project.gid,memberships.section.gid,memberships.section.name"

type Config struct {
	Instance string
	// Sections lists allowed destination section GIDs by project GID.
	// Any project GID may be used for reads, creation, and completion.
	Sections map[string][]string
}

type Adapter struct {
	client   http.Client
	token    string
	instance string
	sections map[string]map[string]struct{}
}

var _ provider.Tasks = (*Adapter)(nil)

// New constructs an adapter using the supplied HTTP client and bearer token.
// Requests use the official Asana endpoint and do not follow redirects.
func New(client *http.Client, token string, config Config) (*Adapter, error) {
	if client == nil {
		return nil, errors.New("asana requires an http client")
	}
	if token == "" || strings.ContainsFunc(token, unicode.IsSpace) || strings.ContainsFunc(token, unicode.IsControl) {
		return nil, errors.New("asana requires a valid bearer token")
	}
	if strings.TrimSpace(config.Instance) == "" || strings.ContainsFunc(config.Instance, unicode.IsControl) {
		return nil, errors.New("asana requires a valid provider instance")
	}
	a := &Adapter{client: *client, token: token, instance: config.Instance, sections: make(map[string]map[string]struct{})}
	a.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for project, sections := range config.Sections {
		if !ValidGID(project) {
			return nil, errors.New("asana requires a valid project gid in sections")
		}
		a.sections[project] = make(map[string]struct{}, len(sections))
		for _, section := range sections {
			if !ValidGID(section) || section == StateOpen || section == StateCompleted {
				return nil, errors.New("asana requires a valid section gid in sections")
			}
			a.sections[project][section] = struct{}{}
		}
	}
	return a, nil
}

type resource struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type taskData struct {
	GID          string   `json:"gid"`
	Name         string   `json:"name"`
	Notes        string   `json:"notes"`
	PermalinkURL string   `json:"permalink_url"`
	Subtype      string   `json:"resource_subtype"`
	Assignee     resource `json:"assignee"`
	Completed    *bool    `json:"completed"`
	Memberships  []struct {
		Project resource `json:"project"`
		Section resource `json:"section"`
	} `json:"memberships"`
}

// ValidGID accepts an opaque Asana identifier usable as one URL path segment.
func ValidGID(gid string) bool {
	return gid != "" && gid != "." && gid != ".." && !strings.ContainsAny(gid, "/\\") &&
		!strings.ContainsFunc(gid, unicode.IsSpace) && !strings.ContainsFunc(gid, unicode.IsControl)
}

func (a *Adapter) validateRef(ref provider.Ref) error {
	if ref.Instance != a.instance || !ValidGID(ref.Scope) || !ValidGID(ref.ID) {
		return errors.New("asana requires a reference for this instance with a project gid and a task gid")
	}
	return nil
}

func (a *Adapter) taskInProject(project string, data taskData) (provider.Task, error) {
	for _, membership := range data.Memberships {
		if membership.Project.GID != project {
			continue
		}
		if !ValidGID(data.GID) || data.Completed == nil {
			return provider.Task{}, errors.New("asana task is missing a valid gid or completion status")
		}
		state := provider.TaskState{ID: StateOpen, Name: "Open"}
		switch {
		case *data.Completed:
			state = provider.TaskState{ID: StateCompleted, Name: "Completed", Terminal: true}
		case membership.Section.GID != "":
			state = provider.TaskState{ID: membership.Section.GID, Name: membership.Section.Name}
		}
		description, meta, err := decodeNotes(data.Notes)
		if err != nil {
			return provider.Task{}, err
		}
		return provider.Task{
			Template: meta.Template, Links: meta.Links,
			Ref: provider.Ref{Instance: a.instance, Scope: project, ID: data.GID},
			URL: data.PermalinkURL, Title: data.Name, Description: description,
			Type: data.Subtype, Assignee: data.Assignee.GID, State: state,
		}, nil
	}
	return provider.Task{}, fmt.Errorf("asana task is outside project %q: %w", project, provider.ErrNotFound)
}

func (a *Adapter) Get(ctx context.Context, ref provider.Ref) (provider.Task, error) {
	if err := a.validateRef(ref); err != nil {
		return provider.Task{}, err
	}
	result, err := request[taskData](ctx, a, http.MethodGet, taskPath(ref.ID), url.Values{"opt_fields": {taskFields}}, nil)
	if err != nil {
		return provider.Task{}, err
	}
	if result.Data.GID != ref.ID {
		return provider.Task{}, errors.New("asana returned a different task gid")
	}
	return a.taskInProject(ref.Scope, *result.Data)
}

// Find lists tasks in query.Scope and matches Text against names and notes.
// A zero Limit reads all pages.
func (a *Adapter) Find(ctx context.Context, query provider.TaskQuery) ([]provider.Task, error) {
	if !ValidGID(query.Scope) || query.Limit < 0 {
		return nil, errors.New("asana task search requires a project gid and a nonnegative limit")
	}
	if query.AssignedToMe {
		return nil, provider.Unsupported(a.instance, "search for tasks assigned to the current user")
	}
	path := "/projects/" + url.PathEscape(query.Scope) + "/tasks"
	pageSize := 100
	if query.Limit > 0 {
		pageSize = min(pageSize, query.Limit)
	}
	params := url.Values{"opt_fields": {taskFields}, "limit": {strconv.Itoa(pageSize)}}
	if query.OpenOnly {
		params.Set("completed_since", "now")
	}
	search := strings.ToLower(query.Text)
	var tasks []provider.Task
	offsets := make(map[string]struct{})
	for {
		page, err := request[[]taskData](ctx, a, http.MethodGet, path, params, nil)
		if err != nil {
			return nil, err
		}
		for _, data := range *page.Data {
			task, err := a.taskInProject(query.Scope, data)
			if errors.Is(err, provider.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if query.OpenOnly && task.State.Terminal {
				continue
			}
			if !strings.Contains(strings.ToLower(task.Title), search) && !strings.Contains(strings.ToLower(task.Description), search) {
				continue
			}
			tasks = append(tasks, task)
			if query.Limit > 0 && len(tasks) == query.Limit {
				return tasks, nil
			}
		}
		if page.NextPage == nil {
			return tasks, nil
		}
		offset := page.NextPage.Offset
		if _, seen := offsets[offset]; seen || offset == "" {
			return nil, errors.New("asana returned an invalid pagination offset")
		}
		offsets[offset] = struct{}{}
		params.Set("offset", offset)
	}
}

func taskPath(gid string) string {
	return "/tasks/" + url.PathEscape(gid)
}
