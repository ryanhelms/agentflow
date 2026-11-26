package webui

import (
	"crypto/sha256"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCommanderScopeCapturedAtLaunch(t *testing.T) {
	s := testServer(t)
	var calls atomic.Int32
	serveCommander(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-lead" {
			t.Error("wrong launch identity")
		}
		_, _ = io.WriteString(w, `{"runs":[]}`)
	})
	t.Setenv("AGENT_COMMANDER_TOKEN", "fixture-lead")
	t.Setenv("AGENT_COMMANDER_WORKER_ID", "lead")
	op, err := openRequest(t.Context(), filepath.Join(t.TempDir(), "registry"), s.repo.Dir, Destination{}, "link")
	if err != nil {
		t.Fatal(err)
	}
	serveCommander(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("consulted the process environment after launch")
		http.Error(w, "unexpected socket", 500)
	})
	capability := issueCommanderCapability(t, s, op.Commander)
	header := httptest.NewRequest(http.MethodGet, "/", nil)
	header.Header.Set("X-Agentflow-Commander", capability)
	binding, scope := s.commanderFor(header)
	if binding != op.Commander || scope == "" {
		t.Fatal("lost originating launch identity")
	}
	_, _, err = binding.activity(t.Context(), s.repo.Dir)
	if err != nil || calls.Load() != 1 {
		t.Fatalf("originating Commander calls: %d, %v", calls.Load(), err)
	}

	t.Setenv("AGENT_COMMANDER_WORKER_ID", "restricted")
	if launchCommanderBinding() != (commanderBinding{}) {
		t.Fatal("missing worker token permitted operator access")
	}
	for _, k := range []string{"AGENT_COMMANDER_ROLE", "AGENT_COMMANDER_RUN_ID"} {
		t.Setenv("AGENT_COMMANDER_WORKER_ID", "")
		t.Setenv(k, "restricted")
		if launchCommanderBinding() != (commanderBinding{}) {
			t.Fatalf("missing token with %s permitted operator access", k)
		}
		t.Setenv(k, "")
	}
}

func TestCommanderConnectionFailureHidesPrivateSocket(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private.sock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := (commanderBinding{Socket: path}).activity(t.Context(), root)
	if err == nil || strings.Contains(err.Error(), path) {
		t.Fatal("connection failure exposed private socket")
	}
}

func TestCommanderGrantsRespectWorktreeAndForegroundActor(t *testing.T) {
	s, linked := testServer(t), testServer(t)
	s.commander = &commanderScope{}
	linked.commander = s.commander
	other := testServer(t)
	operator := commanderBinding{Socket: filepath.Join(t.TempDir(), "operator.sock")}
	worker := commanderBinding{Socket: filepath.Join(t.TempDir(), "worker.sock"), Token: randomKey(), Worker: "one"}
	oldCap := issueCommanderCapability(t, s, operator)
	oldTicket := s.scopedLaunch(time.Now(), s.commander.bind(operator))
	separateCap := issueCommanderCapability(t, other, operator)
	requestFor := func(capability string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Agentflow-Commander", capability)
		return req
	}
	if _, scope := linked.commanderFor(requestFor(oldCap)); scope != "" {
		t.Fatal("worktree accepted sibling grant")
	}
	newCap := issueCommanderCapability(t, linked, worker)
	if _, scope := s.commanderFor(requestFor(oldCap)); scope != "" {
		t.Fatal("operator survived worker foreground launch")
	}
	if b, _ := other.commanderFor(requestFor(separateCap)); b != operator {
		t.Fatal("independent clone lost its identity")
	}
	if b, _ := linked.commanderFor(requestFor(newCap)); b != worker {
		t.Fatal("worker did not retain own authority")
	}
	call := request(t, s, "POST", "/api/session", s.origin, "", map[string]string{"launch": oldTicket})
	var credentials struct{ Session, Remember, Commander string }
	if err := json.Unmarshal(call.Body.Bytes(), &credentials); err != nil || call.Code != 200 || credentials.Session == "" || credentials.Remember == "" || credentials.Commander != "" {
		t.Fatal("old ticket must remain configuration-only")
	}
	resumed := request(t, s, "POST", "/api/resume", s.origin, "", map[string]string{"remember": credentials.Remember}, newCap)
	if resumed.Code != 200 || strings.Contains(resumed.Body.String(), "commander") {
		t.Fatal("saved browser regained Commander")
	}
	reply := request(t, s, "POST", "/api/reply", s.origin, "session", map[string]string{"answer": "yes"}, oldCap)
	if reply.Code != 403 || !strings.Contains(reply.Body.String(), "commander_reconnect") {
		t.Fatal("worktree with revoked cap did not request reconnection")
	}
}

func TestCommanderCrossWorktreeTabScope(t *testing.T) {
	root := testServer(t).repo.Dir
	linked := filepath.Join(t.TempDir(), "linked")
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "--detach", linked).CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
	lead := commanderBinding{Socket: filepath.Join(t.TempDir(), "service.sock"), Token: randomKey(), Worker: "lead"}
	other := lead
	other.Worker = "worker"
	other.Token = randomKey()
	for _, tc := range []struct {
		name                 string
		caller               commanderBinding
		authenticated, allow bool
	}{{"same", lead, true, true}, {"different", other, true, false}, {"configuration only", lead, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := loadDaemon(filepath.Join(t.TempDir(), "daemon"), instance{Port: 80, Key: randomKey()}, randomKey(), func() {})
			if err != nil {
				t.Fatal(err)
			}
			op := registration(root, "link", "handoff")
			op.Commander = lead
			raw, err := d.register(t.Context(), op)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			s := d.live[u.Hostname()+"/main"].server
			w := request(t, s, "POST", "/api/session", s.origin, "", map[string]string{"launch": launchToken(t, u)})
			var credentials struct{ Commander string }
			if err := json.Unmarshal(w.Body.Bytes(), &credentials); err != nil || credentials.Commander == "" {
				t.Fatal("missing initial scope")
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.authenticated {
				req.Header.Set("X-Agentflow-Commander", credentials.Commander)
			}
			_, scope := s.commanderFor(req)
			id := strings.Repeat("7", 32)
			if !s.tabs.attach(id, s.origin, s.worktreePath, scope) {
				t.Fatal("attach failed")
			}
			release := s.tabs.hold([]string{id})
			defer release()
			op = registration(linked, "open", "handoff")
			op.Commander = tc.caller
			done := make(chan error, 1)
			go func() { _, e := d.register(t.Context(), op); done <- e }()
			{
				event := waitNavigation(t, s.tabs, id)
				dest, err := url.Parse(event.Target)
				if err != nil || dest.Path == u.Path {
					t.Fatal("sibling navigation missing")
				}
				target := d.live[dest.Hostname()+"/"+strings.Trim(dest.Path[2:], "/")].server
				w := request(t, target, "POST", "/api/session", target.origin, "", map[string]string{"launch": launchToken(t, dest)})
				var exchange struct{ Commander, Session string }
				if err := json.Unmarshal(w.Body.Bytes(), &exchange); err != nil || w.Code != 200 || exchange.Session == "" || (exchange.Commander != "") != tc.allow {
					t.Fatal("unexpected scope on reused tab")
				}
				if strings.Contains(w.Body.String(), lead.Token) || strings.Contains(raw, lead.Socket) {
					t.Fatal("credentials disclosed in browser response")
				}
				if ack := request(t, s, "POST", "/api/tabs/ack", s.origin, "session", map[string]any{"id": event.ID, "tab": id, "handled": true}); ack.Code != 200 {
					t.Fatal("could not acknowledge")
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(d.index)
			if err != nil || strings.Contains(string(data), lead.Socket) || strings.Contains(string(data), lead.Token) || strings.Contains(string(data), other.Token) {
				t.Fatal("credential in on-disk registry")
			}
		})
	}
}

func TestCommanderNegotiationWithOlderDaemon(t *testing.T) {
	i := instance{Key: randomKey()}
	aead, err := i.controlCipher()
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		plain, err := aead.Open(nil, nil, data, []byte("agentflow-control/v1"))
		if err != nil {
			t.Error(err)
			return
		}
		var op daemonRequest
		if json.Unmarshal(plain, &op) != nil || op.Action != "status" || op.Dir != "" || op.Commander != (commanderBinding{}) {
			t.Error("old daemon received launch or identity")
		}
		hash := sha256.Sum256(data)
		_, _ = w.Write(aead.Seal(nil, nil, []byte("{}"), hash[:]))
	}))
	defer old.Close()
	i.Port = old.Listener.Addr().(*net.TCPAddr).Port
	op := registration("/private/checkout", "link", "handoff")
	op.Commander = commanderBinding{Socket: "/private/service.sock", Token: randomKey()}
	if _, err := i.control(t.Context(), op); err == nil || !strings.Contains(err.Error(), "agentflow ui --stop") || requests.Load() != 1 {
		t.Fatalf("legacy control negotiation: %v (requests %d)", err, requests.Load())
	}
}
