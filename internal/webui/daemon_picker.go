package webui

import (
	"net/http"
	"strings"
	"time"
)

func pickerAsset(w http.ResponseWriter, file, contentType string) {
	b, err := assets.ReadFile("static/" + file)
	if err != nil {
		http.Error(w, "worktree picker unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(b)
}

// Called under the registry lock on a known clone origin.
func (d *daemon) pickerResource(w http.ResponseWriter, r *http.Request, clone registeredWorktree) {
	switch r.URL.Path {
	case "/_agentflow/picker.js":
		pickerAsset(w, "workspace-picker.js", "text/javascript; charset=utf-8")
	case "/_agentflow/appearance.js":
		pickerAsset(w, "appearance.js", "text/javascript; charset=utf-8")
	case "/_agentflow/nscale-tokens.css":
		pickerAsset(w, "nscale-tokens.css", "text/css; charset=utf-8")
	case "/_agentflow/style.css":
		pickerAsset(w, "style.css", "text/css; charset=utf-8")
	case "/_agentflow/event-worker.js":
		// One origin-wide URL, so every tab of a clone shares one SharedWorker
		// and therefore one event stream.
		if _, err := assets.ReadFile("static/event-worker.js"); err != nil {
			http.NotFound(w, r)
			return
		}
		pickerAsset(w, "event-worker.js", "text/javascript; charset=utf-8")
	case "/_agentflow/worktrees":
		origin := "http://" + d.publicHost(clone.Host)
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		_, valid := grantDeadline(token, signature(d.identity, "remember/v1", clone.CloneID+"\x00"+origin), "browser/v1", time.Now(), rememberBrowserFor)
		if !ok || !valid || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin) {
			problem(w, http.StatusUnauthorized, "connect your browser from this clone")
			return
		}
		type choice struct {
			Path string `json:"path"`
			Root string `json:"root"`
		}
		choices := []choice{}
		for _, entry := range d.entries {
			if entry.Host == clone.Host && entry.available() {
				choices = append(choices, choice{"/w/" + entry.ID + "/", entry.Root})
			}
		}
		reply(w, http.StatusOK, choices)
	default:
		http.NotFound(w, r)
	}
}
