package webui

import (
	"errors"
	"net/http"

	"github.com/ryanhelms/agentflow/internal/configconsole"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
)

const approvalConfigFile = ".agentflow/config.yml"

type autonomyEdit struct {
	Scope        string                       `json:"scope"`
	Settings     *consolecfg.ApprovalSettings `json:"settings"`
	UserRevision string                       `json:"user_revision"`
	Content      string                       `json:"content"`
	Base         string                       `json:"base_digest"`
}

func (s *server) autonomy(w http.ResponseWriter, r *http.Request) {
	var change autonomyEdit
	if r.Method == http.MethodPost && !read(w, r, &change) {
		return
	}
	var content *string
	if change.Base != "" {
		content = &change.Content
	}
	var out any
	var err error
	switch {
	case r.Method != http.MethodPost || r.URL.Path != "/api/autonomy":
		out, err = configconsole.InspectAutonomy(r.Context(), s.repo.Dir, change.Base, content)
	case change.Settings == nil || change.Scope != "personal" && change.Scope != "repository":
		problem(w, 400, "choose personal or repository settings")
		return
	case change.Scope == "repository":
		if change.Base == "" {
			problem(w, 409, "reload the repository configuration before staging edits")
			return
		}
		out, err = configconsole.StageAutonomyForm(r.Context(), s.repo.Dir, change.Base, content, *change.Settings)
	default:
		out, err = configconsole.SavePersonalForm(r.Context(), s.repo.Dir, change.Base, content, change.UserRevision, *change.Settings)
	}
	if err != nil {
		code := configconsole.ErrorStatus(err, 422)
		if errors.Is(err, configconsole.ErrUserRevision) {
			code = 409
		}
		problem(w, code, err.Error())
		return
	}
	reply(w, 200, out)
}
