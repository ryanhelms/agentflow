// Package webui serves one worktree to an authenticated browser on loopback.
package webui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/configconsole"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/policy"
)

//go:embed static
var assets embed.FS

type server struct {
	commander                    *commanderScope
	repo                         *gitx.Repo
	origin, instanceKey, session string
	rememberKey                  string
	usedLaunches                 map[string]int64
	launchMu                     sync.Mutex
	tabs                         *tabHub
	tabsOnce                     sync.Once
	// tabsStreamed says that something other than the event runtime serves
	// the tabs state on this server's stream (test fixtures do). Without
	// either, a tab cannot receive navigation and is not attached.
	tabsStreamed bool
	worktreePath string
	// Serialize approval consumption and publishing. Re-read policy for every save.
	writeMu   sync.Mutex
	gate      *policy.Gate
	publishTo configconsole.Publisher
	// Live events (stream.go): nil outside the daemon or when the
	// repository's store could not be opened.
	events      *repoEvents
	streams     *streamLimit
	streamsOnce sync.Once
}

func randomKey() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Serve opens or reuses the console for dir at its overview.
func Serve(ctx context.Context, dir string, port int, ready func(string)) error {
	return ServeAt(ctx, dir, port, Destination{}, func(rawURL string, _ bool) { ready(rawURL) })
}

func (s *server) handler() http.Handler {
	files, _ := fs.Sub(assets, "static")
	if s.events != nil {
		// The tabs projection reads this clone's hub, which publishes
		// ui.tabs.changed to the repository's stream.
		s.events.bindTabs(s.tabRoutes())
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(files))
	mux.HandleFunc("POST /api/session", s.exchange)
	mux.HandleFunc("POST /api/resume", s.resume)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET "+EventStreamPath, s.eventStream)
	mux.HandleFunc("POST /api/tabs/attach", s.tabAttach)
	mux.HandleFunc("POST /api/tabs/ack", s.tabAck)
	mux.HandleFunc("POST /api/tabs/leave", s.tabLeave)
	mux.HandleFunc("GET /api/activity", s.activity)
	mux.HandleFunc("GET /api/autonomy", s.autonomy)
	mux.HandleFunc("POST /api/autonomy", s.autonomy)
	mux.HandleFunc("POST /api/autonomy/inspect", s.autonomy)
	mux.HandleFunc("POST /api/readiness", s.readiness)
	mux.HandleFunc("POST /api/reply", s.replyToWorker)
	mux.HandleFunc("POST /api/compose", s.compose)
	mux.HandleFunc("POST /api/document", s.document)
	mux.HandleFunc("POST /api/document/edit", s.editDocument)
	mux.HandleFunc("POST /api/preview", s.preview)
	mux.HandleFunc("POST /api/publish", s.publish)
	mux.HandleFunc("GET /api/settings/catalog", s.settingsCatalog)
	mux.HandleFunc("POST /api/settings/preview", s.settingsPreview)
	mux.HandleFunc("POST /api/settings/save", s.settingsSave)
	mux.HandleFunc("POST /api/settings/undo", s.settingsUndo)
	mux.HandleFunc("POST /api/harnesses/check", s.harnessesCheck)
	mux.HandleFunc("POST /api/harnesses/probe", s.harnessesProbe)
	mux.HandleFunc("POST /api/ai/respond", s.aiRespond)
	mux.HandleFunc("POST /api/ai/cancel", s.aiCancel)
	mux.HandleFunc("POST /api/ai/draft", s.aiDraft)
	mux.HandleFunc("POST /api/ai/draft/confirm", s.aiDraftConfirm)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if "http://"+r.Host != s.origin {
			problem(w, 403, "unexpected console host")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			origin := r.Header.Get("Origin")
			if (origin != "" && origin != s.origin) || (r.Method == http.MethodPost && (origin != s.origin || r.Header.Get("X-Agentflow-Request") != "console")) {
				problem(w, 403, "expected a same-origin console request")
				return
			}
			if r.URL.Path != "/api/session" && r.URL.Path != "/api/resume" && !s.authorized(r) {
				problem(w, 401, "open the launch URL printed by agentflow ui")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *server) exchange(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Launch string `json:"launch"`
	}
	if !read(w, r, &in) {
		return
	}
	if !s.consumeLaunch(in.Launch) {
		problem(w, 401, "launch URL expired, already used or invalid")
		return
	}
	now := time.Now()
	credentials := map[string]string{
		"session":  grantToken(s.session, "tab/v1", now.Add(tabSessionFor)),
		"remember": grantToken(s.rememberKey, "browser/v1", now.Add(rememberBrowserFor)),
	}
	if cap := s.exchangeCommander(in.Launch, now); cap != "" {
		credentials["commander"] = cap
	}
	reply(w, 200, credentials)
}

func reply(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func problem(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}

func read(w http.ResponseWriter, r *http.Request, v any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		problem(w, 415, "expected application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		problem(w, 400, "invalid request body")
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		problem(w, 400, "expected a single request")
		return false
	}
	return true
}
