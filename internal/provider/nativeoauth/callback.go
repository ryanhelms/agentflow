package nativeoauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type callbackResult struct {
	code string
	err  error
}

// WaitCallback accepts one browser authorization response on the registered
// authority and path. The caller binds and secures the listener before launch.
func WaitCallback(ctx context.Context, ln net.Listener, host, path, issuer, state string, requireIssuer bool, launch func() error) (string, error) {
	results := make(chan callbackResult, 1)
	var once sync.Once
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		q, err := url.ParseQuery(r.URL.RawQuery)
		if r.Method != http.MethodGet || r.Host != host || r.URL.IsAbs() ||
			r.URL.EscapedPath() != path || err != nil || len(q["state"]) != 1 ||
			subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result := callbackResult{code: q.Get("code")}
		if q.Has("error") || len(q["code"]) != 1 || !Opaque(result.code) ||
			(requireIssuer && !q.Has("iss")) || (q.Has("iss") && (len(q["iss"]) != 1 || q.Get("iss") != issuer)) {
			result = callbackResult{err: errors.New("consent denied or invalid OAuth callback")}
		}
		consumed := false
		once.Do(func() {
			consumed = true
			_, _ = io.WriteString(w, "Return to Agentflow to see the enrollment result.")
			results <- result
		})
		if !consumed {
			w.WriteHeader(http.StatusGone)
		}
	})
	server := &http.Server{Handler: handler, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
		MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = server.Serve(ln)
	}()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
	}()
	launched := make(chan error, 1)
	go func(done chan<- error) { done <- launch() }(launched)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-stopped:
			return "", errors.New("lost the OAuth callback listener")
		case err := <-launched:
			if err != nil {
				return "", errors.New("could not open system browser for OAuth enrollment")
			}
			launched = nil
		case result := <-results:
			return result.code, result.err
		}
	}
}
