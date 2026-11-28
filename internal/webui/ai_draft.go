package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// The console's draft requests (phase 8; ADR "Phase 8 AI projection and
// console routes"). Both sit behind the console's session, origin and
// request-header checks, and invoke the ai.draft command through an
// Invoker as a person at the console, for the worktree this console
// serves: the page never names a path, a harness or a worktree. At confirm
// the console keeps the confirm token in its own memory under a random
// confirm ID for five minutes and gives the page the manifest; the person's
// Send is the confirmation. The page never sees a confirm token or a
// prompt, and the result arrives over the stream.

const (
	// AIDraftCommand is the Spec the routes invoke.
	AIDraftCommand    = "ai.draft"
	aiDraftPerMinute  = 6
	aiDraftConfirmTTL = 5 * time.Minute
	aiDraftTimeout    = 2 * time.Minute
)

var aiConfirmRule = regexp.MustCompile(`^cf_[0-9a-f]{32}$`)

// aiDraftInput is the tool input the page may send: no confirm_token and
// no dry_run.
type aiDraftInput struct {
	Paths            []string `json:"paths,omitempty"`
	Text             string   `json:"text,omitempty"`
	Template         string   `json:"template,omitempty"`
	TemplateRevision string   `json:"template_revision,omitempty"`
	Project          string   `json:"project,omitempty"`
	Type             string   `json:"type,omitempty"`
	LinkTo           string   `json:"link_to,omitempty"`
	Key              string   `json:"key,omitempty"`
	Thread           string   `json:"thread,omitempty"`
	Hint             string   `json:"hint,omitempty"`
}

type aiDraftRequest struct {
	Feature string       `json:"feature"`
	Input   aiDraftInput `json:"input"`
}

// keptConfirm is one confirmation the console holds for the page.
type keptConfirm struct {
	server  *server
	input   json.RawMessage
	token   string
	expires time.Time
}

var aiDrafts struct {
	mu      sync.Mutex
	confirm map[string]keptConfirm
	seen    map[*server][]time.Time
}

// allowDraft counts each console's draft requests in the last minute.
func allowDraft(s *server, now time.Time) bool {
	aiDrafts.mu.Lock()
	defer aiDrafts.mu.Unlock()
	if aiDrafts.seen == nil {
		aiDrafts.seen = map[*server][]time.Time{}
	}
	recent := aiDrafts.seen[s][:0]
	for _, t := range aiDrafts.seen[s] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= aiDraftPerMinute {
		aiDrafts.seen[s] = recent
		return false
	}
	aiDrafts.seen[s] = append(recent, now)
	return true
}

func keepConfirm(k keptConfirm) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	id := "cf_" + hex.EncodeToString(b)
	aiDrafts.mu.Lock()
	defer aiDrafts.mu.Unlock()
	if aiDrafts.confirm == nil {
		aiDrafts.confirm = map[string]keptConfirm{}
	}
	now := aiNow()
	for k2, v := range aiDrafts.confirm {
		if now.After(v.expires) {
			delete(aiDrafts.confirm, k2)
		}
	}
	aiDrafts.confirm[id] = k
	return id
}

// takeConfirm removes and returns a kept confirmation of console s: each is
// used once.
func takeConfirm(s *server, id string) (keptConfirm, bool) {
	aiDrafts.mu.Lock()
	defer aiDrafts.mu.Unlock()
	k, ok := aiDrafts.confirm[id]
	if !ok || k.server != s {
		return keptConfirm{}, false
	}
	delete(aiDrafts.confirm, id)
	if aiNow().After(k.expires) {
		return keptConfirm{}, false
	}
	return k, true
}

// aiDraftRefusal is the answer for a refused draft: agentflow's own code,
// message and fix.
type aiDraftRefusal struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

func (s *server) aiDraft(w http.ResponseWriter, r *http.Request) {
	var in aiDraftRequest
	if !readHarnessBody(w, r, &in) {
		return
	}
	if !ai.Known(ai.FeatureID(in.Feature)) {
		problem(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input, err := json.Marshal(struct {
		Feature string `json:"feature"`
		aiDraftInput
	}{in.Feature, in.Input})
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid request body")
		return
	}
	s.aiDraftInvoke(w, r, input, "")
}

type aiDraftConfirmRequest struct {
	Confirm string `json:"confirm"`
}

func (s *server) aiDraftConfirm(w http.ResponseWriter, r *http.Request) {
	var in aiDraftConfirmRequest
	if !readHarnessBody(w, r, &in) {
		return
	}
	if !aiConfirmRule.MatchString(in.Confirm) {
		problem(w, http.StatusBadRequest, "invalid request body")
		return
	}
	k, ok := takeConfirm(s, in.Confirm)
	if !ok {
		reply(w, http.StatusConflict, aiDraftRefusal{Code: "expired", Message: "This confirmation is no longer valid.", Fix: "Request the draft again."})
		return
	}
	s.aiDraftInvoke(w, r, k.input, k.token)
}

func (s *server) aiDraftInvoke(w http.ResponseWriter, r *http.Request, input json.RawMessage, token string) {
	cmd, ok := aiCommands().Lookup(AIDraftCommand)
	jc, isJSON := cmd.(command.JSONCommand)
	if !ok || !isJSON {
		problem(w, http.StatusServiceUnavailable, "AI drafts are not available in this build")
		return
	}
	if !allowDraft(s, aiNow()) {
		problem(w, http.StatusTooManyRequests, "too many draft requests: wait a minute")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), aiDraftTimeout)
	defer cancel()
	a, err := app.Open(ctx, s.repo.Dir, aiClient)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "cannot open the repository")
		return
	}
	inv := command.NewInvoker(command.Options{Auditor: command.AuditLog{}})
	res := jc.InvokeJSON(consoleRequester(ctx), inv, a, input, token, command.Origin{Source: "daemon", Client: aiClient})
	if res.Err != nil {
		var ref *aisession.Refusal
		if errors.As(res.Err, &ref) {
			reply(w, http.StatusConflict, aiDraftRefusal{Code: ref.Code, Message: ref.Message, Fix: ref.Fix})
			return
		}
		if c := policy.ErrorCode(res.Err); c != "" {
			reply(w, http.StatusConflict, aiDraftRefusal{Code: c, Message: "What the draft would send changed since you saw it, so nothing was sent.", Fix: "Request the draft again and read the new manifest."})
			return
		}
		fmt.Fprintf(os.Stderr, "agentflow: %s from the console failed\n", AIDraftCommand)
		reply(w, http.StatusConflict, aiDraftRefusal{Code: "error", Message: "The draft did not start.", Fix: "Request the draft again."})
		return
	}
	if p, ok := res.Value.(*app.Pending); ok {
		if p.Status != "needs_confirmation" || p.ConfirmToken == "" {
			// suggest (or a held approval) refuses here: the console only
			// confirms with a person's click.
			reply(w, http.StatusConflict, aiDraftRefusal{Code: p.Status, Message: "Your autonomy settings do not let the console start this draft.", Fix: "Run agentflow ai draft in your own terminal."})
			return
		}
		var m ai.Manifest
		b, _ := json.Marshal(p.Summary)
		_ = json.Unmarshal(b, &m)
		id := keepConfirm(keptConfirm{server: s, input: input, token: p.ConfirmToken, expires: aiNow().Add(aiDraftConfirmTTL)})
		reply(w, http.StatusOK, struct {
			Confirm  string      `json:"confirm"`
			Manifest ai.Manifest `json:"manifest"`
		}{id, m})
		return
	}
	reply(w, http.StatusAccepted, struct{}{})
}
