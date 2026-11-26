package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/settings"
)

// The settings endpoints (ADR, "Phase 4 console endpoints"). They sit behind
// the console's session, origin and request-header checks (server.handler)
// and only decode and encode: each runs the config.* Spec through the
// daemon's Invoker, which internal/command/ops provides as the
// SettingsBackend (ops imports webui, so webui cannot import ops).

// SettingsCall is one console request to a settings command.
type SettingsCall struct {
	// Dir is the worktree root.
	Dir string
	// Repo and Worktree name the stream the call's op.* events belong to.
	Repo     bus.RepoID
	Worktree bus.WorktreeID
	// Publish puts an event on the daemon's bus; nil when the repository
	// has no event store, and the call then publishes nothing.
	Publish func(ctx context.Context, e bus.Event) error
}

// SettingsBackend runs the settings commands for the console with the
// daemon's Origin.
type SettingsBackend interface {
	// Catalog is config.fields with every field and no values.
	Catalog(ctx context.Context, call SettingsCall) (any, error)
	Preview(ctx context.Context, call SettingsCall, cs settings.ChangeSet) (any, error)
	Save(ctx context.Context, call SettingsCall, cs settings.ChangeSet, token string) (any, error)
	Undo(ctx context.Context, call SettingsCall, req settings.UndoRequest, token string) (any, error)
}

var (
	settingsMu      sync.RWMutex
	settingsBackend SettingsBackend
)

// RegisterSettingsBackend sets the backend of the settings endpoints.
func RegisterSettingsBackend(b SettingsBackend) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	settingsBackend = b
}

func currentSettingsBackend() SettingsBackend {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return settingsBackend
}

func (s *server) settingsCall() SettingsCall {
	call := SettingsCall{Dir: s.repo.Dir, Worktree: bus.WorktreeID(strings.Trim(strings.TrimPrefix(s.worktreePath, "/w/"), "/"))}
	if r := s.events; r != nil && r.bus != nil {
		call.Repo = r.id
		call.Publish = func(ctx context.Context, e bus.Event) error {
			_, err := r.bus.Publish(ctx, e)
			return err
		}
	}
	return call
}

type settingsSave struct {
	ChangeSet settings.ChangeSet `json:"change_set"`
	Approval  string             `json:"approval,omitempty"`
}

type settingsUndo struct {
	Undo     settings.UndoRequest `json:"undo"`
	Approval string               `json:"approval,omitempty"`
}

func (s *server) settingsCatalog(w http.ResponseWriter, r *http.Request) {
	s.settingsReply(w, r, func(b SettingsBackend) (any, error) { return b.Catalog(r.Context(), s.settingsCall()) })
}

func (s *server) settingsPreview(w http.ResponseWriter, r *http.Request) {
	var cs settings.ChangeSet
	if !read(w, r, &cs) {
		return
	}
	s.settingsReply(w, r, func(b SettingsBackend) (any, error) { return b.Preview(r.Context(), s.settingsCall(), cs) })
}

func (s *server) settingsSave(w http.ResponseWriter, r *http.Request) {
	var in settingsSave
	if !read(w, r, &in) {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.settingsReply(w, r, func(b SettingsBackend) (any, error) {
		return b.Save(r.Context(), s.settingsCall(), in.ChangeSet, in.Approval)
	})
}

func (s *server) settingsUndo(w http.ResponseWriter, r *http.Request) {
	var in settingsUndo
	if !read(w, r, &in) {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.settingsReply(w, r, func(b SettingsBackend) (any, error) {
		return b.Undo(r.Context(), s.settingsCall(), in.Undo, in.Approval)
	})
}

// settingsReply runs fn and encodes its answer: a Pending carries its token
// as "approval", the name the browser uses for its bearer; a policy failure
// is 409 with its code; other failures are 422.
func (s *server) settingsReply(w http.ResponseWriter, _ *http.Request, fn func(SettingsBackend) (any, error)) {
	b := currentSettingsBackend()
	if b == nil {
		problem(w, 503, "settings are not available in this console")
		return
	}
	out, err := fn(b)
	switch {
	case errors.Is(err, settings.ErrNothingToUndo):
		problem(w, 404, err.Error())
		return
	case err != nil && policy.ErrorCode(err) != "":
		reply(w, 409, map[string]string{"error": err.Error(), "code": policy.ErrorCode(err)})
		return
	case err != nil:
		problem(w, 422, err.Error())
		return
	}
	if p, ok := out.(*app.Pending); ok {
		reply(w, 200, pendingAnswer(p))
		return
	}
	reply(w, 200, out)
}

// pendingAnswer is a Pending with its token as "approval".
func pendingAnswer(p *app.Pending) map[string]any {
	raw, _ := json.Marshal(p)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	delete(out, "confirm_token")
	if p.ConfirmToken != "" {
		out["approval"] = p.ConfirmToken
	}
	return out
}
