package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the embedded console and real session exchange on a disposable
// worktree. The fixture can issue test tickets or expire one state request.
func TestConsoleConnectionBrowser(t *testing.T) {
	runBrowserFixture(t, "/w/first/#providers?role=task_management", func(mux *http.ServeMux, origin string) {
		s := testServer(t)
		s.origin = origin
		s.worktreePath = "/w/first/"
		handler := s.handler()
		second := testServer(t)
		second.origin, second.worktreePath = origin, "/w/second/"
		second.rememberKey, second.tabs = s.rememberKey, s.tabRoutes()
		secondRoutes := http.NewServeMux()
		secondRoutes.Handle("/", second.handler())
		worktree := http.NewServeMux()
		scoped := http.StripPrefix("/w/first", worktree)
		mux.Handle("/w/first/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Agentflow-Commander") != "" && r.URL.Path != "/w/first/api/activity" && r.URL.Path != "/w/first/api/reply" && r.URL.Path != "/w/first/api/tabs/events" {
				t.Errorf("Commander credential sent to %s", r.URL.Path)
			}
			scoped.ServeHTTP(w, r)
		}))
		mux.Handle("/w/second/", http.StripPrefix("/w/second", secondRoutes))
		worktree.Handle("/", handler)
		installBrowserCommanderFixture(t, worktree, handler)
		page, err := assets.ReadFile("static/index.html")
		if err != nil {
			t.Fatal(err)
		}
		checks, err := os.ReadFile("testdata/connection-test.js")
		if err != nil {
			t.Fatal(err)
		}
		index := func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, strings.Replace(string(page), "</head>", `<script src="/fixture/checks.js" defer></script></head>`, 1))
		}
		worktree.HandleFunc("GET /{$}", index)
		secondRoutes.HandleFunc("GET /{$}", index)
		mux.HandleFunc("GET /fixture/checks.js", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(checks)
		})
		mux.HandleFunc("POST /fixture/ticket", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, mintLaunch(s.instanceKey, time.Now()))
		})
		mux.HandleFunc("POST /fixture/navigate", func(w http.ResponseWriter, r *http.Request) {
			d := Destination{Screen: r.URL.Query().Get("screen"), Role: r.URL.Query().Get("role")}
			var target string
			for _, other := range []*server{s, second} {
				if r.URL.Query().Get("worktree") == strings.Trim(other.worktreePath, "/") {
					target = origin + other.worktreePath + "#launch=" + mintLaunch(other.instanceKey, time.Now()) + "&" + d.Fragment()
				}
			}
			reused := s.tabRoutes().navigateTo(r.Context(), d, r.URL.Query().Has("auto"), target)
			if err := reused.err(); err != nil {
				problem(w, http.StatusConflict, err.Error())
				return
			}
			reply(w, http.StatusOK, reused == tabAccepted)
		})
		mux.HandleFunc("GET /fixture/live", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for s.tabRoutes().navigateTo(ctx, Destination{}, true, "") != tabAccepted {
				select {
				case <-ctx.Done():
					problem(w, http.StatusRequestTimeout, "no live tab")
					return
				case <-tick.C:
				}
			}
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("POST /fixture/revise", func(w http.ResponseWriter, _ *http.Request) {
			file, err := os.OpenFile(filepath.Join(s.repo.Dir, ".agentflow", "config.yml"), os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				http.Error(w, "fixture checkout unavailable", http.StatusInternalServerError)
				return
			}
			defer file.Close()
			if _, err = io.WriteString(file, "\n# concurrent checkout change\n"); err != nil {
				http.Error(w, "fixture write failed", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		var expire atomic.Bool
		var denyResume atomic.Bool
		worktree.HandleFunc("POST /api/resume", func(w http.ResponseWriter, r *http.Request) {
			if denyResume.Swap(false) {
				problem(w, http.StatusUnauthorized, "expired fixture grant")
				return
			}
			handler.ServeHTTP(w, r)
		})
		mux.HandleFunc("POST /fixture/expire", func(w http.ResponseWriter, r *http.Request) {
			expire.Store(true)
			denyResume.Store(r.URL.Query().Has("remember"))
			w.WriteHeader(http.StatusNoContent)
		})
		worktree.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
			if expire.Swap(false) {
				problem(w, 401, "expired fixture")
				return
			}
			handler.ServeHTTP(w, r)
		})
	})
}

// Model the transport boundary. The first two successful direct launches
// carry a Commander grant; later launches are config-only. A reviewed reply loses access
// before confirmation, just like an expired or superseded actor context.
func installBrowserCommanderFixture(t *testing.T, routes *http.ServeMux, handler http.Handler) {
	t.Helper()
	key := randomKey()
	var issued atomic.Int32
	var reviewed atomic.Bool
	routes.HandleFunc("POST /api/session", func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		var data map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		delete(data, "commander")
		if issued.Add(1) <= 2 {
			data["commander"] = key
		}
		reply(w, http.StatusOK, data)
	})
	routes.HandleFunc("GET /api/tabs/events", func(w http.ResponseWriter, r *http.Request) {
		if issued.Load() == 1 && !reviewed.Load() && r.Header.Get("X-Agentflow-Commander") != key {
			t.Error("the scoped tab stream is missing its launch context")
		}
		handler.ServeHTTP(w, r)
	})
	routes.HandleFunc("GET /api/activity", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Agentflow-Commander") != key {
			reply(w, http.StatusOK, map[string]any{"commander_reconnect": true, "attention": []item{{Title: "Fixture pull request"}}})
			return
		}
		reply(w, http.StatusOK, map[string]any{"attention": []item{{Run: "worker-run", Request: "worker-request", Title: "Fixture worker question"}}, "handoff": "Fixture worker question"})
	})
	routes.HandleFunc("POST /api/reply", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Agentflow-Commander") != key || reviewed.Swap(true) {
			reply(w, http.StatusForbidden, map[string]any{"commander_reconnect": true, "error": "Reopen from the Commander session."})
			return
		}
		reply(w, http.StatusOK, map[string]string{"approval": "fixture-review"})
	})
}
