package webui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nscaledev/devflow/internal/commit"
	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/consolecfg"
	"github.com/nscaledev/devflow/internal/consolepublish"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
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

func currentFile(ctx context.Context, root, path, digest string) (consolecfg.File, error) {
	snapshot, err := consolecfg.View(ctx, root)
	if err != nil {
		return consolecfg.File{}, err
	}
	for _, f := range snapshot.Files {
		if f.Path != path {
			continue
		}
		if !f.Editable {
			return consolecfg.File{}, errors.New("this source is managed outside the repository editor")
		}
		if f.Digest != digest {
			return consolecfg.File{}, errors.New("the source changed; reload and review your edit")
		}
		return f, nil
	}
	return consolecfg.File{}, errors.New("unknown configuration source")
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
	if change.Path != ".devflow/config.yml" {
		problem(w, 400, "provider controls edit repository role settings")
		return
	}
	if _, err := currentFile(r.Context(), s.repo.Dir, change.Path, change.Base); err != nil {
		problem(w, 409, err.Error())
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
	content, err := consolecfg.EditSetting(change.Content, change.Role, change.Setting, change.Value, change.Operation == "remove")
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	s.composed(w, r, change.Path, content, change.Base)
}

func (s *server) composed(w http.ResponseWriter, r *http.Request, path, content, base string) {
	out := map[string]any{"content": content}
	if plan, err := consolecfg.Preview(r.Context(), s.repo.Dir, path, content, base); err == nil {
		out["providers"] = plan.Providers
	} else {
		// A candidate may need several field edits before the resolver accepts it.
		out["warning"] = err.Error()
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
	file, err := currentFile(ctx, s.repo.Dir, in.Path, in.Base)
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	plan, err := consolecfg.Preview(ctx, s.repo.Dir, in.Path, in.Content, in.Base)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	if file.Content == in.Content {
		problem(w, 422, "there are no changes to review")
		return
	}
	ci := ciImpact(s.repo.Dir, in.Path, "")
	result := map[string]any{"plan": plan, "before": file.Content, "after": in.Content, "changes": plan.Changes, "ci": ci, "approval": ""}
	target, targetErr := consolepublish.Destination(ctx, s.repo.Dir)
	if targetErr != nil {
		if publish {
			problem(w, 422, targetErr.Error())
		} else {
			result["warning"] = targetErr.Error()
			reply(w, 200, result)
		}
		return
	}
	// Bind the reviewed effective values as well as the file, destination and Git action.
	evidence, err := json.Marshal(plan)
	if err != nil {
		problem(w, 500, "could not encode the preview")
		return
	}
	head, err := s.repo.Git(ctx, "rev-parse", "HEAD")
	if err != nil {
		problem(w, 409, "repository has no commit to review")
		return
	}
	token := in.Approval
	if !publish {
		token = ""
	}
	in.Approval = ""
	payload := struct {
		Edit                                           edit
		Root, Destination, Head, Evidence, Session, CI string
	}{in, s.repo.Dir, target, strings.TrimSpace(head), hash(evidence), hash([]byte(s.session)), ci}
	dec, err := s.authorize(ctx, in.Title, payload, token)
	if err != nil {
		if publish {
			problem(w, 409, err.Error())
		} else {
			result["warning"] = err.Error()
			reply(w, 200, result)
		}
		return
	}
	if !publish {
		result["approval"] = dec.Token
		result["reason"], result["policy"], result["request"] = dec.Reason, dec.Policy, dec.Approval
		result["destination"] = target
		if dec.Outcome == policy.SuggestOnly {
			result["warning"] = dec.Reason
		}
		reply(w, 200, result)
		return
	}
	if dec.Outcome != policy.Proceed || token == "" {
		problem(w, 403, "review and confirm this change under the current repository policy")
		return
	}
	out, err := s.publishTo(ctx, s.repo.Dir, target, in.Path, file.Content, in.Content, in.Title, in.Draft, in.Task)
	if err != nil {
		s.gate.Audit(policy.Remote, "console.publish", payload, "error")
		problem(w, 409, err.Error())
		return
	}
	s.gate.Audit(policy.Remote, "console.publish", payload, out.State)
	reply(w, 200, out)
}

func hash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func (s *server) authorize(ctx context.Context, title string, payload any, token string) (policy.Decision, error) {
	cfg, _, err := config.LoadRepoCore(s.repo.Dir)
	if err != nil {
		return policy.Decision{}, err
	}
	message := commit.Parse(title)
	if strings.ContainsAny(title, "\r\n") || !message.Conventional || message.Exempt {
		return policy.Decision{}, errors.New("enter a conventional commit title")
	}
	if issues := commit.Validate(title, cfg.Commit, gitx.KeyFromBranch(s.repo.Branch(ctx))); len(issues) > 0 {
		return policy.Decision{}, errors.New(issues[0].Message)
	}
	return s.checkGate(ctx, policy.Remote, "console.publish", payload, token)
}

func (s *server) checkGate(ctx context.Context, class policy.Class, action string, payload any, token string) (policy.Decision, error) {
	cfg, _, err := config.LoadRepoCore(s.repo.Dir)
	if err != nil {
		return policy.Decision{}, err
	}
	user, err := config.LoadUser()
	if err != nil {
		return policy.Decision{}, err
	}
	level := config.Effective(user, cfg)
	if s.gate == nil {
		home, homeErr := config.Home()
		if homeErr != nil {
			return policy.Decision{}, homeErr
		}
		s.gate, err = policy.NewGate(home, level, "repository-console")
		if err != nil {
			return policy.Decision{}, fmt.Errorf("open confirmation policy: %w", err)
		}
	}
	s.gate.Autonomy = level
	s.gate.Configure(s.repo.GitDir(ctx), user, cfg)
	return s.gate.Check(class, action, payload, token, true)
}
