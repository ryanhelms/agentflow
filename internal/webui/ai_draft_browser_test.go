package webui

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Drive the draft view of the AI sessions page from a scripted stream and
// fake endpoints: the features and why one is off, the request, the list of
// what would be sent and the reader's Send or Discard, a draft as it runs,
// Cancel, the candidates with their commands, hostile model and repository
// text, refusals, every draft state and a lost connection.
//
// The ai projection and the draft routes (lane 8.1) are replaced here: the
// checks file writes AIState frames with the shapes of the ADR ("Phase 8 AI
// projection and console routes"), and aiDraftEndpoints answers the two
// requests. No test starts a harness, a session or a draft.
func TestAIDraftBrowser(t *testing.T) {
	runBrowserFixture(t, "/w/first/", func(mux *http.ServeMux, origin string) {
		aiDraftConsoleFixture(t, mux, origin, "testdata/ai-draft/ai-draft-test.js")
	})
}

// AGENTFLOW_AI_DRAFT_DEMO is an address such as 127.0.0.1:41737. With it
// set, this test serves the console with scripted drafts
// (testdata/ai-draft/demo.js) until the test is stopped, for a design
// review in a browser:
//
//	AGENTFLOW_AI_DRAFT_DEMO=127.0.0.1:41737 go test ./internal/webui -run TestAIDraftDemo -timeout 0
func TestAIDraftDemo(t *testing.T) {
	addr := os.Getenv("AGENTFLOW_AI_DRAFT_DEMO")
	if addr == "" {
		t.Skip("set AGENTFLOW_AI_DRAFT_DEMO to an address to serve the draft view fixture")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	aiDraftConsoleFixture(t, mux, "http://"+listener.Addr().String(), "testdata/ai-draft/demo.js")
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-t.Context().Done(); _ = srv.Close() }()
	t.Logf("serving the draft view fixture at http://%s/w/first/", listener.Addr())
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

// aiDraftEndpoints stands in for lane 8.1's POST /api/ai/draft and
// /api/ai/draft/confirm. It records each request and answers what the
// checks file set for the route: 202 {} until it sets something else.
type aiDraftEndpoints struct {
	mu       sync.Mutex
	requests []map[string]string
	answers  map[string]aiDraftAnswer
}

type aiDraftAnswer struct {
	status int
	body   string
}

func (f *aiDraftEndpoints) serve(route string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
		f.mu.Lock()
		f.requests = append(f.requests, map[string]string{
			"route": route, "body": string(body), "content_type": r.Header.Get("Content-Type"),
			"console": r.Header.Get("X-Agentflow-Request"), "authorized": strconv.FormatBool(strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")),
		})
		answer, set := f.answers[route]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !set {
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, "{}")
			return
		}
		w.WriteHeader(answer.status)
		_, _ = io.WriteString(w, answer.body)
	}
}

// shellWords has a real shell read a line the page quoted, as the arguments
// of printf: each word comes back followed by a NUL. The line is never a
// command of its own. The shell starts without its startup files, with an
// empty environment, in dir. available is false when the shell is not
// installed.
func shellWords(ctx context.Context, shell, dir, line string) (out string, available bool, err error) {
	flags := map[string][]string{"bash": {"--norc", "--noprofile"}, "zsh": {"-f"}, "fish": {"--no-config"}}[shell]
	path, lookErr := exec.LookPath(shell)
	if flags == nil || lookErr != nil {
		return "", false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, append(flags, "-c", `printf '%s\0' `+line)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
	b, err := cmd.Output()
	return string(b), true, err
}

// install adds the two routes under the console's worktree path and the
// routes the checks file reads and scripts them with:
//
//	GET  /fixture/draft/requests the draft and confirm requests seen so far
//	POST /fixture/draft/answer   answer ?route= (draft or confirm) with ?status= and the body
//	POST /fixture/draft/shell    read the body, a quoted line, back through ?shell= (bash, zsh or fish)
func (f *aiDraftEndpoints) install(t *testing.T, mux *http.ServeMux) {
	dir := t.TempDir()
	mux.HandleFunc("POST /fixture/draft/shell", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
		out, available, err := shellWords(r.Context(), r.URL.Query().Get("shell"), dir, string(body))
		// A word that ran as a command would leave a file behind.
		left, _ := os.ReadDir(dir)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"available": available, "out": out, "failed": err != nil, "files": len(left)})
	})
	mux.HandleFunc("POST /w/first/api/ai/draft", f.serve("draft"))
	mux.HandleFunc("POST /w/first/api/ai/draft/confirm", f.serve("confirm"))
	mux.HandleFunc("GET /fixture/draft/requests", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.requests)
	})
	mux.HandleFunc("POST /fixture/draft/answer", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		status, err := strconv.Atoi(r.URL.Query().Get("status"))
		if err != nil || status < 200 || status > 599 {
			http.Error(w, "invalid fixture status", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.answers[r.URL.Query().Get("route")] = aiDraftAnswer{status: status, body: string(body)}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
}

// aiDraftConsoleFixture is the AI page's fixture (the embedded console at
// /w/first/, a scripted event stream, the fake respond and cancel
// endpoints) with the fake draft endpoints added.
func aiDraftConsoleFixture(t *testing.T, mux *http.ServeMux, origin, checksFile string) {
	t.Helper()
	aiConsoleFixture(t, mux, origin, checksFile)
	endpoints := &aiDraftEndpoints{requests: []map[string]string{}, answers: map[string]aiDraftAnswer{}}
	endpoints.install(t, mux)
}
