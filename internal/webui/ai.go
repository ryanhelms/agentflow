package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
)

// The AI panel's two requests (lane 7.2; ADR "Phase 7 AI projection",
// Requests from the page). Both sit behind the console's session, origin
// and request-header checks (server.handler), run the ai.respond and
// ai.cancel commands through an Invoker as a person at the console, and
// answer 202 with no session data: results arrive over the stream. The
// Manager computes the allow's HMAC from the session's answer.key; the
// page never sees the key.

const (
	// AIRespondCommand and AICancelCommand are the Specs the routes invoke
	// (lane 7.3).
	AIRespondCommand = "ai.respond"
	AICancelCommand  = "ai.cancel"
	aiClient         = "repository-console"
	aiPerMinute      = 30
	aiCallTimeout    = 15 * time.Second
)

// The formats the page sends: a session ID, an ask ID ("ask_" and 16
// hex), and the fingerprint it showed (64 hex).
var (
	aiAskRule         = regexp.MustCompile(`^ask_[0-9a-f]{16}$`)
	aiFingerprintRule = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// aiCommands is the registry the routes look the commands up in; tests
// replace it.
var aiCommands = func() *command.Registry { return command.Default }

var aiNow = time.Now

// aiLimits counts each console's requests in the last minute.
var aiLimits struct {
	mu   sync.Mutex
	seen map[*server][]time.Time
}

func allowAI(s *server, now time.Time) bool {
	aiLimits.mu.Lock()
	defer aiLimits.mu.Unlock()
	if aiLimits.seen == nil {
		aiLimits.seen = map[*server][]time.Time{}
	}
	recent := aiLimits.seen[s][:0]
	for _, t := range aiLimits.seen[s] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= aiPerMinute {
		aiLimits.seen[s] = recent
		return false
	}
	aiLimits.seen[s] = append(recent, now)
	return true
}

// consoleRequester marks ctx as a person at the console. Only these
// handlers, behind the console's checks, call it.
func consoleRequester(ctx context.Context) context.Context {
	return aisession.WithRequester(ctx, aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceConsole, Client: aiClient})
}

type aiRespondRequest struct {
	Session     string `json:"session"`
	Ask         string `json:"ask"`
	Decision    string `json:"decision"`
	Fingerprint string `json:"fingerprint"`
}

func (s *server) aiRespond(w http.ResponseWriter, r *http.Request) {
	var in aiRespondRequest
	if !readHarnessBody(w, r, &in) {
		return
	}
	if !aisession.ValidID(in.Session) || !aiAskRule.MatchString(in.Ask) || !aiFingerprintRule.MatchString(in.Fingerprint) ||
		in.Decision != "allow" && in.Decision != "reject" {
		problem(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input, _ := json.Marshal(in)
	s.aiInvoke(w, r, AIRespondCommand, input)
}

type aiCancelRequest struct {
	Session string `json:"session"`
}

func (s *server) aiCancel(w http.ResponseWriter, r *http.Request) {
	var in aiCancelRequest
	if !readHarnessBody(w, r, &in) {
		return
	}
	if !aisession.ValidID(in.Session) {
		problem(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input, _ := json.Marshal(in)
	s.aiInvoke(w, r, AICancelCommand, input)
}

func (s *server) aiInvoke(w http.ResponseWriter, r *http.Request, name string, input []byte) {
	cmd, ok := aiCommands().Lookup(name)
	jc, isJSON := cmd.(command.JSONCommand)
	if !ok || !isJSON {
		problem(w, http.StatusServiceUnavailable, "AI sessions are not available in this build")
		return
	}
	if !allowAI(s, aiNow()) {
		problem(w, http.StatusTooManyRequests, "too many requests: wait a minute")
		return
	}
	// The answer ends with the 202; the command runs to its end.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), aiCallTimeout)
	defer cancel()
	a, err := app.Open(ctx, s.repo.Dir, aiClient)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "cannot open the repository")
		return
	}
	inv := command.NewInvoker(command.Options{Auditor: command.AuditLog{}})
	// ai.cancel needs no confirmation from here: the person's click is it
	// (the Spec skips the gate for a person at the console).
	res := jc.InvokeJSON(consoleRequester(ctx), inv, a, input, "", command.Origin{Source: "daemon", Client: aiClient})
	if res.Err != nil && !errors.Is(res.Err, context.Canceled) {
		// The refusal reaches the page as the stream's state; the log line
		// names the command only.
		fmt.Fprintf(os.Stderr, "agentflow: %s from the console failed\n", name)
	}
	reply(w, http.StatusAccepted, struct{}{})
}
