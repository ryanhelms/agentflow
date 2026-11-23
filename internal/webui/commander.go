package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nscaledev/devflow/internal/policy"
)

type commanderRun struct {
	ID, Title string
	Workers   []struct{ ID, Name string }
	Requests  []struct {
		ID, Question, Status string
		WorkerID             string `json:"worker_id"`
	}
}

var errCommanderDenied = errors.New("request denied by Agent Commander for the current identity")

func commanderSocket() string {
	socket := os.Getenv("AGENT_COMMANDER_SOCKET")
	if socket == "" {
		state := os.Getenv("AGENT_COMMANDER_HOME")
		if state == "" {
			base, err := os.UserConfigDir()
			if err != nil {
				return ""
			}
			state = filepath.Join(base, "Agent Commander")
		}
		socket = filepath.Join(state, "service.sock")
	}
	absolute, _ := filepath.Abs(socket)
	return absolute
}

func commanderRequest(ctx context.Context, method, path string, input any, readResponse func(*json.Decoder) error) error {
	socket := commanderSocket()
	if !filepath.IsAbs(socket) {
		return errors.New("expected an absolute Agent Commander socket path")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://commander"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := os.Getenv("AGENT_COMMANDER_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect to Agent Commander: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w (HTTP %d)", errCommanderDenied, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected Agent Commander response: HTTP %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(resp.Body)
	if err := readResponse(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("unexpected trailing data in Agent Commander response")
	}
	return nil
}

// Agent Commander includes historical transcripts for every worktree in the
// run list. Decode one run at a time and only allocate worker and question
// records for a matching worktree. The order of fields within a run can vary.
type commanderInbox struct {
	root  os.FileInfo
	items []item
}

func (inbox *commanderInbox) UnmarshalJSON(data []byte) error {
	var scope struct{ Path string }
	if err := json.Unmarshal(data, &scope); err != nil {
		return err
	}
	if !filepath.IsAbs(scope.Path) {
		return nil
	}
	ri, err := os.Stat(scope.Path)
	if err != nil || !os.SameFile(inbox.root, ri) {
		return nil
	}
	var run commanderRun
	if err := json.Unmarshal(data, &run); err != nil {
		return err
	}
	for _, q := range run.Requests {
		if q.Status != "pending" {
			continue
		}
		name := run.Title
		for _, w := range run.Workers {
			if w.ID == q.WorkerID {
				name = w.Name
				break
			}
		}
		inbox.items = append(inbox.items, item{Title: "Waiting for your reply · " + name, Detail: q.Question, Run: run.ID, Request: q.ID})
	}
	return nil
}

func (inbox *commanderInbox) read(decoder *json.Decoder) error {
	// Stream the runs array and tolerate additional envelope metadata.
	expect := func(want json.Delim) error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if token != want {
			return errors.New("unexpected Agent Commander run-list format")
		}
		return nil
	}
	if err := expect('{'); err != nil {
		return err
	}
	found := false
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		if key != "runs" {
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return err
			}
			continue
		}
		if found {
			return errors.New("duplicate Agent Commander run list")
		}
		found = true
		if err := expect('['); err != nil {
			return err
		}
		for decoder.More() {
			if err := decoder.Decode(inbox); err != nil {
				return err
			}
		}
		if err := expect(']'); err != nil {
			return err
		}
	}
	if !found {
		return errors.New("missing Agent Commander run list")
	}
	return expect('}')
}

func commanderActivity(ctx context.Context, root string) ([]item, bool, error) {
	active, err := os.Stat(root)
	if err != nil {
		return nil, false, err
	}
	inbox := commanderInbox{root: active, items: []item{}}
	err = commanderRequest(ctx, http.MethodGet, "/v1/runs", nil, inbox.read)
	if errors.Is(err, os.ErrNotExist) {
		return []item{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return inbox.items, true, nil
}

func (s *server) replyToWorker(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Run      string `json:"run_id"`
		Request  string `json:"request_id"`
		Answer   string `json:"answer"`
		Approval string `json:"approval,omitempty"`
	}
	if !read(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Answer) == "" || len(in.Answer) > 65536 {
		problem(w, 422, "enter an answer of up to 64 KB")
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	items, _, err := commanderActivity(r.Context(), s.repo.Dir)
	if errors.Is(err, errCommanderDenied) {
		problem(w, 403, err.Error())
		return
	}
	if err != nil {
		problem(w, 503, "Agent Commander is unavailable")
		return
	}
	var selected *item
	for i := range items {
		if items[i].Request != "" && items[i].Request == in.Request && items[i].Run == in.Run {
			selected = &items[i]
			break
		}
	}
	if selected == nil {
		problem(w, 409, "this request is no longer pending in the current worktree")
		return
	}
	payload := struct{ Root, Socket, Run, Request, Question, Answer string }{s.repo.Dir, commanderSocket(), in.Run, in.Request, selected.Detail, in.Answer}
	d, err := s.checkGate(r.Context(), policy.Local, "console.reply", payload, in.Approval)
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	if d.Outcome == policy.SuggestOnly {
		problem(w, 403, d.Reason)
		return
	}
	if d.Outcome != policy.Proceed {
		reply(w, 200, map[string]any{"approval": d.Token, "reason": d.Reason, "policy": d.Policy, "request": d.Approval})
		return
	}
	var result struct{ ID, Status, Answer string }
	err = commanderRequest(r.Context(), http.MethodPost, "/v1/requests/"+url.PathEscape(in.Request)+"/reply", map[string]string{"answer": in.Answer}, func(decoder *json.Decoder) error {
		return decoder.Decode(&result)
	})
	if errors.Is(err, errCommanderDenied) {
		s.gate.Audit(policy.Local, "console.reply", payload, "denied")
		problem(w, 403, err.Error())
		return
	}
	if err != nil || result.ID != in.Request || result.Status != "answered" || result.Answer != in.Answer {
		s.gate.Audit(policy.Local, "console.reply", payload, "unverified")
		problem(w, 409, "reply outcome is unverified; refresh activity before retrying")
		return
	}
	s.gate.Audit(policy.Local, "console.reply", payload, "answered")
	reply(w, 200, map[string]string{"state": "answered"})
}
