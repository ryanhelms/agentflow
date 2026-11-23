// Package webui serves one worktree to an authenticated browser on loopback.
package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nscaledev/devflow/internal/consolepublish"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
)

//go:embed static
var assets embed.FS

type server struct {
	repo                    *gitx.Repo
	origin, launch, session string
	launchMu                sync.Mutex
	// Serialize approval consumption and publishing. Re-read policy for every save.
	writeMu   sync.Mutex
	gate      *policy.Gate
	publishTo func(context.Context, string, string, string, string, string, string, bool, ...string) (consolepublish.Result, error)
}

func randomKey() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Serve opens a loopback listener and supplies its one-time launch URL to ready.
func Serve(ctx context.Context, dir string, port int, ready func(string)) error {
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("open console listener: %w", err)
	}
	s := &server{repo: repo, origin: "http://" + l.Addr().String(), launch: randomKey(), session: randomKey(), publishTo: consolepublish.PublishTo}
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	stop := context.AfterFunc(ctx, func() { _ = srv.Close() })
	defer stop()
	ready(s.origin + "/#launch=" + s.launch)
	err = srv.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *server) handler() http.Handler {
	files, _ := fs.Sub(assets, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(files))
	mux.HandleFunc("POST /api/session", s.exchange)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/activity", s.activity)
	mux.HandleFunc("POST /api/readiness", s.readiness)
	mux.HandleFunc("POST /api/reply", s.replyToWorker)
	mux.HandleFunc("POST /api/compose", s.compose)
	mux.HandleFunc("POST /api/document", s.document)
	mux.HandleFunc("POST /api/document/edit", s.editDocument)
	mux.HandleFunc("POST /api/preview", s.preview)
	mux.HandleFunc("POST /api/publish", s.publish)
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
			if (origin != "" && origin != s.origin) || (r.Method == http.MethodPost && (origin != s.origin || r.Header.Get("X-Devflow-Request") != "console")) {
				problem(w, 403, "expected a same-origin console request")
				return
			}
			if r.URL.Path != "/api/session" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.session)) != 1 {
				problem(w, 401, "open the launch URL printed by devflow ui")
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
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	if s.launch == "" || subtle.ConstantTimeCompare([]byte(in.Launch), []byte(s.launch)) != 1 {
		problem(w, 401, "launch URL already used or invalid")
		return
	}
	s.launch = ""
	reply(w, 200, map[string]string{"session": s.session})
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
