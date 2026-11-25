package webui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// AGENTFLOW_TEST_CHROME is an installed Chrome or Chromium executable. The test
// loads the same embedded editor as the console and uses a private profile.
func TestTypedEditorBrowser(t *testing.T) {
	runBrowserFixture(t, "", func(mux *http.ServeMux, _ string) {
		editor, err := assets.ReadFile("static/typed-editor.js")
		if err != nil {
			t.Fatal(err)
		}
		checks, err := os.ReadFile("testdata/typed-editor-test.js")
		if err != nil {
			t.Fatal(err)
		}
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><body><main id="fixture"></main><script src="/editor.js"></script><script src="/checks.js"></script></body></html>`)
		})
		for route, script := range map[string][]byte{"/editor.js": editor, "/checks.js": checks} {
			mux.HandleFunc("GET "+route, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/javascript")
				_, _ = w.Write(script)
			})
		}
	})
}

func runBrowserFixture(t *testing.T, path string, configure func(*http.ServeMux, string)) {
	t.Helper()
	chrome := os.Getenv("AGENTFLOW_TEST_CHROME")
	if chrome == "" {
		t.Skip("set AGENTFLOW_TEST_CHROME to an installed Chrome or Chromium executable")
	}
	mux := http.NewServeMux()
	srv := httptest.NewUnstartedServer(mux)
	defer srv.Close()
	configure(mux, "http://"+srv.Listener.Addr().String())
	result := make(chan string, 1)
	mux.HandleFunc("POST /result", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			http.Error(w, "invalid fixture result", http.StatusBadRequest)
			return
		}
		select {
		case result <- string(body):
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv.Start()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	profile := t.TempDir()
	cmd := exec.CommandContext(ctx, chrome, "--headless", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--disable-background-networking", "--disable-component-update", "--disable-sync", "--disable-default-apps",
		"--disable-extensions", "--enable-automation", "--use-mock-keychain", "--remote-debugging-port=0",
		"--proxy-server=http://127.0.0.1:1", "--proxy-bypass-list=127.0.0.1", "--user-data-dir="+profile, "about:blank")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var exitErr error
	done := make(chan struct{})
	go func() { exitErr = cmd.Wait(); close(done) }()
	defer func() { cancel(); <-done }()
	// Explicitly open the fixture. Managed browsers can select a startup page
	// regardless of a URL passed on the command line.
	openFixtureTab(t, ctx, profile, srv.URL+path)
	select {
	case status := <-result:
		if status != "passed" {
			t.Fatalf("Chromium regression checks failed: %s", status)
		}
	case <-done:
		t.Fatalf("Chromium exited before reporting fixture results: %v", exitErr)
	case <-ctx.Done():
		t.Fatal("timed out waiting for Chromium to run the local fixture")
	}
}

func openFixtureTab(t *testing.T, ctx context.Context, profile, fixture string) {
	t.Helper()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("Chromium did not provide a local debugging port")
		case <-tick.C:
			b, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
			line, _, _ := strings.Cut(string(b), "\n")
			port, parseErr := strconv.ParseUint(line, 10, 16)
			if err != nil || parseErr != nil || port == 0 {
				continue
			}
			target := "http://127.0.0.1:" + line + "/json/new?" + url.QueryEscape(fixture)
			req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("open fixture tab: %s", resp.Status)
			}
			return
		}
	}
}
