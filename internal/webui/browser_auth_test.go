package webui

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBrowserGrantsResumeOnlyInTheSameClone(t *testing.T) {
	s := testServer(t)
	exchanged := request(t, s, "POST", "/api/session", s.origin, "", map[string]string{"launch": mintLaunch(s.instanceKey, time.Now())})
	var first struct{ Session, Remember string }
	if exchanged.Code != 200 || json.Unmarshal(exchanged.Body.Bytes(), &first) != nil || first.Session == "" || first.Remember == "" {
		t.Fatal("one-time launch did not issue a tab session and browser grant")
	}
	sibling := testServer(t)
	sibling.rememberKey = s.rememberKey
	payload := map[string]string{"remember": first.Remember}
	if request(t, sibling, "GET", "/api/state", sibling.origin, first.Session, nil).Code != 401 {
		t.Fatal("tab credential crossed worktrees")
	}
	resumed := request(t, sibling, "POST", "/api/resume", sibling.origin, "", payload)
	var second struct{ Session, Remember string }
	if resumed.Code != 200 || json.Unmarshal(resumed.Body.Bytes(), &second) != nil || second.Session == "" || second.Remember != "" {
		t.Fatal("resuming on a sibling worktree failed or extended the browser grant")
	}
	if request(t, sibling, "GET", "/api/state", sibling.origin, second.Session, nil).Code != 200 ||
		request(t, s, "GET", "/api/state", s.origin, second.Session, nil).Code != 401 {
		t.Fatal("resumed session was not scoped to the requested worktree")
	}
	other := testServer(t)
	if request(t, other, "POST", "/api/resume", other.origin, "", payload).Code != 401 ||
		request(t, s, "POST", "/api/resume", "https://other.invalid", "", payload).Code != 403 ||
		request(t, s, "GET", "/api/state", s.origin, first.Remember, nil).Code != 401 {
		t.Fatal("browser grant escaped its clone, origin, or intended operation")
	}
}

func TestRememberedGrantHasAnAbsoluteDeadline(t *testing.T) {
	s := testServer(t)
	now := time.Now()
	for _, deadline := range []time.Time{now.Add(-time.Second), now.Add(31 * 24 * time.Hour)} {
		token := grantToken(s.rememberKey, "browser/v1", deadline)
		if request(t, s, "POST", "/api/resume", s.origin, "", map[string]string{"remember": token}).Code != 401 {
			t.Fatal("invalid lifetime accepted")
		}
	}
	until := now.Add(time.Hour)
	resumed := request(t, s, "POST", "/api/resume", s.origin, "", map[string]string{"remember": grantToken(s.rememberKey, "browser/v1", until)})
	var result struct{ Session string }
	if err := json.Unmarshal(resumed.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	end, ok := grantDeadline(result.Session, s.session, "tab/v1", now, tabSessionFor)
	if !ok || end.Unix() != until.Unix() {
		t.Fatal("tab session outlived remembered authorization")
	}
}
