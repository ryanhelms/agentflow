package webui

import (
	"crypto/hmac"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const rememberBrowserFor = 30 * 24 * time.Hour
const tabSessionFor = 12 * time.Hour

// Grants use separate purposes and keys. A remembered browser can resume a
// worktree in its clone; only that worktree's short-lived session authorizes API calls.
func grantToken(key, purpose string, until time.Time) string {
	body := strconv.FormatInt(until.Unix(), 10) + "." + randomKey()
	return body + "." + signature(key, purpose, body)
}

func grantDeadline(token, key, purpose string, now time.Time, limit time.Duration) (time.Time, bool) {
	if key == "" || len(token) > 128 {
		return time.Time{}, false
	}
	stamp, rest, ok := strings.Cut(token, ".")
	if !ok {
		return time.Time{}, false
	}
	nonce, mac, ok := strings.Cut(rest, ".")
	end, err := strconv.ParseInt(stamp, 10, 64)
	if !ok || len(nonce) != 43 || err != nil || end <= now.Unix() || end > now.Add(limit).Unix() ||
		!hmac.Equal([]byte(mac), []byte(signature(key, purpose, stamp+"."+nonce))) {
		return time.Time{}, false
	}
	return time.Unix(end, 0), true
}

func (s *server) authorized(r *http.Request) bool {
	token, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	_, valid := grantDeadline(token, s.session, "tab/v1", time.Now(), tabSessionFor)
	return bearer && valid
}

func (s *server) resume(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Remember string `json:"remember"`
	}
	if !read(w, r, &in) {
		return
	}
	now := time.Now()
	until, valid := grantDeadline(in.Remember, s.rememberKey, "browser/v1", now, rememberBrowserFor)
	if !valid {
		problem(w, http.StatusUnauthorized, "reconnect this browser to the repository")
		return
	}
	if maxSession := now.Add(tabSessionFor); maxSession.Before(until) {
		until = maxSession
	}
	// Resuming a tab never extends the browser grant past its original deadline.
	reply(w, http.StatusOK, map[string]string{"session": grantToken(s.session, "tab/v1", until)})
}
