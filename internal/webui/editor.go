package webui

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ryanhelms/agentflow/internal/configconsole"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type edit struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Base     string `json:"base_digest"`
	Title    string `json:"title"`
	Task     string `json:"task_ref"`
	Draft    bool   `json:"draft"`
	Approval string `json:"approval,omitempty"`
}

func (s *server) compose(w http.ResponseWriter, r *http.Request) {
	var change struct {
		Path      string          `json:"path"`
		Content   string          `json:"content"`
		Base      string          `json:"base_digest"`
		Role      string          `json:"role"`
		Setting   string          `json:"setting"`
		Value     json.RawMessage `json:"value"`
		Operation string          `json:"operation"`
	}
	if !read(w, r, &change) {
		return
	}
	if change.Path != consolecfg.MainFile {
		problem(w, 400, "provider controls edit repository role settings")
		return
	}
	switch change.Operation {
	case "remove", "set":
		if change.Setting == "" {
			problem(w, 400, "choose a setting")
			return
		}
	case "select":
		change.Setting = ""
	default:
		problem(w, 400, "unknown edit operation")
		return
	}
	out, err := configconsole.Compose(r.Context(), s.repo.Dir, change.Base, &change.Content, change.Role, change.Operation, change.Setting, change.Value)
	if err != nil {
		problem(w, configconsole.ErrorStatus(err, 422), err.Error())
		return
	}
	reply(w, 200, out)
}

func (s *server) preview(w http.ResponseWriter, r *http.Request) { s.review(w, r, false) }
func (s *server) publish(w http.ResponseWriter, r *http.Request) { s.review(w, r, true) }

func (s *server) review(w http.ResponseWriter, r *http.Request, publish bool) {
	var in edit
	if !read(w, r, &in) {
		return
	}
	ctx := r.Context()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	candidate := configconsole.Edit{Path: in.Path, Content: in.Content, Base: in.Base, Title: in.Title, Task: in.Task, Draft: in.Draft}
	out, err := configconsole.Review(ctx, s.repo, s.gate, s.session, candidate, in.Approval, publish, s.publishTo)
	if err != nil {
		problem(w, configconsole.ErrorStatus(err, 422), err.Error())
		return
	}
	if out.Gate != nil {
		s.gate = out.Gate
	}
	if publish {
		if out.Published == nil {
			problem(w, 403, "review and confirm this change under the current repository policy")
			return
		}
		reply(w, 200, out.Published)
		return
	}
	// The browser calls its bearer "approval" and its shared metadata "request".
	reply(w, 200, map[string]any{
		"plan": out.Plan, "before": out.Before, "after": out.After, "changes": out.Plan.Changes,
		"ci": out.CI, "destination": out.Destination, "warning": out.Warning, "approval": out.Token,
		"reason": out.Reason, "policy": out.Policy, "request": out.Approval,
	})
}

func (s *server) checkGate(ctx context.Context, class policy.Class, action string, payload any, token string) (policy.Decision, error) {
	gate, err := configconsole.CurrentGate(ctx, s.repo, s.gate, "repository-console")
	if err != nil {
		return policy.Decision{}, err
	}
	s.gate = gate
	return gate.Check(class, action, payload, token, true)
}
