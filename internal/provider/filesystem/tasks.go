package filesystem

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
)

// Tasks manages task records in a caller-owned directory.
type Tasks struct{ *store }

var _ provider.Tasks = (*Tasks)(nil)

// NewTasks opens an existing directory. Close the adapter when finished.
func NewTasks(instance, directory string) (*Tasks, error) {
	s, err := openStore(instance, directory, taskKind)
	if err != nil {
		return nil, err
	}
	return &Tasks{s}, nil
}

func task(r record) provider.Task {
	return provider.Task{
		Ref: r.Ref, Key: r.Ref.ID, Title: r.Title, Description: r.body, Type: r.Type,
		State: *r.State, Priority: r.Priority, Assignee: r.Assignee,
		Links: r.Links, Revision: r.revision,
	}
}

func (s *Tasks) Get(ctx context.Context, ref provider.Ref) (provider.Task, error) {
	r, err := s.get(ctx, ref)
	if err != nil {
		return provider.Task{}, err
	}
	return task(r), nil
}

func matches(r record, text string) bool {
	return strings.Contains(strings.ToLower(r.Title), text) || strings.Contains(strings.ToLower(r.body), text) ||
		strings.Contains(strings.ToLower(r.Ref.ID), text)
}

func (s *Tasks) Find(ctx context.Context, q provider.TaskQuery) ([]provider.Task, error) {
	if q.AssignedToMe {
		return nil, provider.Unsupported(s.instance, "assigned-to-me requires a user identity")
	}
	if q.Limit < 0 {
		return nil, errors.New("task query limit must be nonnegative")
	}
	rs, err := s.scan(ctx, q.Scope)
	if err != nil {
		return nil, err
	}
	var result []provider.Task
	text := strings.ToLower(q.Text)
	for _, r := range rs {
		if (q.OpenOnly && r.State.Terminal) || !matches(r, text) {
			continue
		}
		result = append(result, task(r))
		if len(result) == q.Limit {
			break
		}
	}
	return result, nil
}

func (s *Tasks) Create(ctx context.Context, draft provider.TaskDraft, opts provider.WriteOptions) (provider.WriteResult, error) {
	id := rand.Text()
	if opts.OperationID != "" {
		id = digest([]byte(opts.OperationID))
	}
	r := record{header: header{
		Version: 1, Kind: taskKind, Ref: provider.Ref{Instance: s.instance, Scope: draft.Scope, ID: id},
		Title: draft.Title, Type: draft.Type, Priority: draft.Priority, Labels: draft.Labels, Links: draft.Links,
		State: &provider.TaskState{ID: "open", Name: "Open"},
	}, body: draft.Description}
	return s.write(ctx, create, r, opts)
}

func (s *Tasks) Transition(ctx context.Context, ref provider.Ref, state provider.TaskState, opts provider.WriteOptions) (provider.WriteResult, error) {
	if strings.TrimSpace(state.ID) == "" {
		return provider.WriteResult{}, errors.New("target state.id is required")
	}
	r := record{header: header{Version: 1, Kind: taskKind, Ref: ref, State: &state}}
	return s.write(ctx, transition, r, opts)
}
