package slackhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/nscaledev/agentflow/internal/app"
	"github.com/nscaledev/agentflow/internal/provider/native"
	"github.com/nscaledev/agentflow/internal/provider/slackoauth"
)

const (
	targetChannel = "C0123456789"
	targetTeam    = "T0123456789"
	targetUser    = "U0123456789"
	permalink     = "https://fixture.slack.com/archives/C0123456789/p1790771415332219"
	ack           = `{"message_link":"` + permalink + `","message_context":{"channel_id":"C0123456789","message_ts":"1790771415.332219"},"ignored":"private-provider-data"}`
)

var (
	errNotEnrolled   = errors.New("fixture enrollment absent")
	errLoginRequired = errors.New("fixture login required")
)

type fixture struct {
	postEventStream                   bool
	t                                 *testing.T
	mu                                sync.Mutex // simulates the enrollment worker's cross-process lock
	locked                            bool
	token                             string
	grant                             slackoauth.GrantIdentity
	openErr                           error
	initStatus, sendStatus            int
	lastAuthorization                 string
	catalog                           []json.RawMessage
	catalogCursor                     string
	catalogs, writes, reads, connects int
	replaceAfterIdentity              bool
	replaceAfterList                  bool
	sendErr                           error
	sendResult                        json.RawMessage
	profileResult                     json.RawMessage
	expectClaimDir                    string
}

// All HTTP, including session cleanup, terminates here without a socket.
func newFixture(t *testing.T) (*fixture, *native.SessionWriter) {
	t.Helper()
	f := &fixture{
		t: t, token: "fixture-only-bearer", grant: slackoauth.GrantIdentity{Team: targetTeam, User: targetUser, Generation: "first"},
		sendResult:    json.RawMessage(`{"structuredContent":` + ack + `,"content":[]}`),
		profileResult: json.RawMessage(`{"result":"- **User ID:** U0123456789"}`),
	}
	data, err := os.ReadFile("testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &f.catalog); err != nil {
		t.Fatal(err)
	}
	writer := newWriter(Enrollment{
		BoundIdentity: func(context.Context) (slackoauth.GrantIdentity, error) {
			identity := f.grant
			if f.replaceAfterIdentity {
				f.grant.Generation = "replacement"
			}
			return identity, f.openErr
		},
		WithBoundToken: func(_ context.Context, expected string, dispatch func(string) error) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if expected == "" || f.grant.Generation != expected {
				return errors.New("private-grant-error")
			}
			f.locked = true
			defer func() { f.locked = false }()
			return dispatch(f.token)
		},
		ErrNotEnrolled: errNotEnrolled, ErrLoginRequired: errLoginRequired,
	}, f)
	t.Cleanup(writer.Close)
	return f, writer
}

func (f *fixture) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Helper()
	if r.URL.String() != "https://mcp.slack.com/mcp" || !f.locked || r.Header.Get("Authorization") != "Bearer "+f.token || r.GetBody != nil {
		f.t.Fatal("request escaped endpoint, grant lock or single-use body")
	}
	f.lastAuthorization = r.Header.Get("Authorization")
	rec := httptest.NewRecorder()
	if r.Method == http.MethodDelete {
		rec.WriteHeader(http.StatusNoContent)
		return rec.Result(), nil
	}
	if r.Method != http.MethodPost {
		f.t.Fatalf("unexpected HTTP method %s", r.Method)
	}
	var rpc struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
			Cursor    string         `json:"cursor"`
		} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
		f.t.Fatal(err)
	}
	var result any
	rec.Header().Set("Content-Type", "application/json")
	if rpc.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "fixture-session" {
		f.t.Fatal("request left the initialized MCP session")
	}
	switch rpc.Method {
	case "initialize":
		f.connects++
		if f.initStatus != 0 {
			rec.WriteHeader(f.initStatus)
			return rec.Result(), nil
		}
		rec.Header().Set("Mcp-Session-Id", "fixture-session")
		result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "slack-fixture", "version": "1"}}
	case "notifications/initialized":
		rec.WriteHeader(http.StatusAccepted)
		return rec.Result(), nil
	case "tools/list":
		f.catalogs++
		result = map[string]any{"tools": f.catalog, "nextCursor": f.catalogCursor}
		if f.replaceAfterList {
			f.grant.Generation = "replacement"
		}
	case "tools/call":
		switch rpc.Params.Name {
		case send:
			f.writes++
			if !reflect.DeepEqual(rpc.Params.Arguments, sendCall().Arguments) {
				f.t.Fatal("send payload changed")
			}
			if f.expectClaimDir != "" {
				files, err := filepath.Glob(filepath.Join(f.expectClaimDir, "*.claim"))
				if err != nil || len(files) != 1 {
					f.t.Fatal("mutation reached provider without a durable claim")
				}
			}
			if f.sendErr != nil {
				return nil, f.sendErr
			}
			if f.sendStatus != 0 {
				rec.Header().Set("Location", "https://other.example/never")
				rec.WriteHeader(f.sendStatus)
				return rec.Result(), nil
			}
			result = f.sendResult
		case profile:
			f.reads++
			result = map[string]any{"structuredContent": f.profileResult, "content": []any{}}
		case channels:
			f.reads++
			// A regular workspace token can ignore team_id and omit the team from its result.
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": `{"channels":[{"id":"C0123456789"}]}`}}}
		case thread, channelHistory:
			f.reads++
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "fixture history"}}}
		default:
			f.t.Fatal("unmapped tool reached the wire")
		}
	default:
		f.t.Fatalf("unexpected JSON-RPC method %s", rpc.Method)
	}
	packet, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
	if err != nil {
		f.t.Fatal(err)
	}
	if f.postEventStream && rpc.Params.Name == send {
		rec.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(rec, "event: message\ndata: %s\n\n", packet)
	} else {
		_, _ = rec.Write(packet)
	}
	return rec.Result(), nil
}

func sendCall() native.Call {
	return native.Call{Provider: "slack", Server: "slack", Tool: send, Scope: targetTeam, Arguments: map[string]any{"channel_id": targetChannel, "message": "Rollout"}}
}

func prepare(t *testing.T, w *native.SessionWriter) native.WriteBinding {
	t.Helper()
	b, err := w.Prepare(t.Context(), sendCall())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBoundCalls(t *testing.T) {
	f, w := newFixture(t)
	b := prepare(t, w)
	if b.Owner != "agentflow" || b.Origin != origin || b.Connection == "" || b.Payload == "" {
		t.Fatal("incomplete enrollment binding")
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{profile, nil},
		{channels, map[string]any{"team_id": targetTeam, "types": "public_channel,private_channel", "exclude_archived": true, "format": "ids_only", "cursor": "page2"}},
		{channelHistory, map[string]any{"channel_id": targetChannel}},
		{thread, map[string]any{"channel_id": targetChannel, "message_ts": "1790771415.332219", "limit": 20, "response_format": "detailed"}},
	} {
		c := sendCall()
		c.Tool, c.Arguments = tc.name, tc.args
		if _, err := w.ReadBound(t.Context(), b, c); err != nil {
			t.Fatal(err)
		}
	}
	f.token = "fixture-rotated-bearer"
	f.postEventStream = true
	raw, err := w.Write(t.Context(), b, sendCall())
	if err != nil || strings.Contains(string(raw), "private-provider-data") || !strings.Contains(string(raw), permalink) ||
		f.lastAuthorization != "Bearer fixture-rotated-bearer" || f.reads != 4 || f.connects != 1 || f.writes != 1 {
		t.Fatalf("receipt or grant mismatch: %s, %v, reads=%d writes=%d", raw, err, f.reads, f.writes)
	}
	if _, err := w.Write(t.Context(), b, sendCall()); !errors.Is(err, native.ErrWriteNotDispatched) || f.writes != 1 {
		t.Fatal("consumed binding was reused")
	}
}

func TestGrantChangedBeforeInitialize(t *testing.T) {
	f, w := newFixture(t)
	f.replaceAfterIdentity = true
	_, err := w.Prepare(t.Context(), sendCall())
	if !errors.Is(err, native.ErrDenied) || errors.Is(err, native.ErrUnavailable) || f.connects != 0 {
		t.Fatalf("grant changed before initialize: %v, connections=%d", err, f.connects)
	}
}

func TestGrantChangedAtDispatch(t *testing.T) {
	f, w := newFixture(t)
	b := prepare(t, w)
	f.replaceAfterList = true // After the final schema check, before the mutation POST.
	_, err := w.Write(t.Context(), b, sendCall())
	if !errors.Is(err, native.ErrWriteNotDispatched) || !errors.Is(err, native.ErrDenied) || errors.Is(err, native.ErrUnavailable) ||
		strings.Contains(err.Error(), "private-grant-error") || f.writes != 0 {
		t.Fatalf("grant swap: %v, writes=%d", err, f.writes)
	}
}

func TestNoFallbackAfterProviderDeny(t *testing.T) {
	for _, tc := range []struct {
		name    string
		openErr error
		status  int
		want    error
	}{
		{"absent", errNotEnrolled, 0, native.ErrSignIn}, {"invalid", errLoginRequired, 0, native.ErrSignIn},
		{"keychain", errors.New("private-grant-error"), 0, native.ErrDenied}, {"unauthorized", nil, 401, native.ErrSignIn}, {"forbidden", nil, 403, native.ErrDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w := newFixture(t)
			f.openErr, f.initStatus = tc.openErr, tc.status
			_, err := w.Prepare(t.Context(), sendCall())
			if !errors.Is(err, tc.want) || !errors.Is(tc.want, native.ErrUnavailable) && errors.Is(err, native.ErrUnavailable) ||
				strings.Contains(err.Error(), "private-grant-error") || f.connects > 1 || f.writes != 0 {
				t.Fatalf("selection: %v, connections=%d writes=%d", err, f.connects, f.writes)
			}
		})
	}
}

func TestSchemaAndArgumentDenials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture)
	}{
		{"unadvertised message", func(f *fixture) {
			f.catalog[0] = json.RawMessage(`{"name":"slack_send_message","inputSchema":{"type":"object","properties":{"channel_id":{"type":"string"}}}}`)
		}},
		{"untyped field", func(f *fixture) {
			f.catalog[0] = json.RawMessage(strings.Replace(string(f.catalog[0]), `"message": {"type": "string"}`, `"message": {}`, 1))
		}},
		{"changed scope verification", func(f *fixture) {
			f.catalog[2] = json.RawMessage(`{"name":"slack_list_user_channels","inputSchema":{"type":"object"}}`)
		}},
		{"absent tool", func(f *fixture) { f.catalog = f.catalog[1:] }},
		{"duplicate tool", func(f *fixture) { f.catalog = append(f.catalog, f.catalog[0]) }},
		{"incompatible schema", func(f *fixture) {
			f.catalog[0] = json.RawMessage(strings.Replace(string(f.catalog[0]), `"message": {"type": "string"}`, `"message": {"type": "string","maxLength":2}`, 1))
		}},
		{"incomplete catalogue", func(f *fixture) { f.catalog = nil; f.catalogCursor = "cycle" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w := newFixture(t)
			b := prepare(t, w)
			tc.change(f)
			if _, err := w.Write(t.Context(), b, sendCall()); !errors.Is(err, native.ErrWriteNotDispatched) || !errors.Is(err, native.ErrDenied) || errors.Is(err, native.ErrUnavailable) || f.writes != 0 {
				t.Fatalf("%v, writes=%d", err, f.writes)
			}
		})
	}
	f, w := newFixture(t)
	for _, change := range []func(*native.Call){
		func(c *native.Call) { c.Tool = "slack_schedule_message" }, func(c *native.Call) { c.Server = "slack-copy" },
		func(c *native.Call) { c.Arguments["thread_ts"] = "1234.123456" }, func(c *native.Call) { c.Arguments["channel_id"] = "#release" },
		func(c *native.Call) { c.Scope = "" },
	} {
		c := sendCall()
		change(&c)
		if _, err := w.Prepare(t.Context(), c); !errors.Is(err, native.ErrDenied) || f.connects != 0 {
			t.Fatal("invalid mutation opened an enrollment")
		}
	}
	b := prepare(t, w)
	c := sendCall()
	c.Arguments["message"] = "changed"
	if _, err := w.Rebind(t.Context(), b, c); !errors.Is(err, native.ErrDenied) {
		t.Fatal("changed payload rebound")
	}
	c = sendCall()
	c.Scope += "/" + targetUser
	if _, err := w.Rebind(t.Context(), b, c); !errors.Is(err, native.ErrDenied) {
		t.Fatal("changed scope rebound")
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{profile, map[string]any{"user_id": "U0123456789"}},
		{channels, map[string]any{"team_id": targetTeam}},
		{channels, map[string]any{"team_id": "T1123456789", "types": "public_channel,private_channel", "exclude_archived": true, "format": "ids_only", "cursor": "page2"}},
		{channelHistory, map[string]any{"channel_id": "C1123456789"}},
		{"slack_search_public", map[string]any{}},
	} {
		b = prepare(t, w)
		c := sendCall()
		c.Tool, c.Arguments = tc.name, tc.args
		if _, err := w.ReadBound(t.Context(), b, c); !errors.Is(err, native.ErrDenied) || f.reads != 0 {
			t.Fatal("unsafe read reached provider")
		}
	}
}

func TestPinnedEndpoint(t *testing.T) {
	for _, url := range []string{origin + "/mcp/", origin + "/mcp?", origin + "/mcp#fragment", origin + "/other", origin + ".example/mcp", "http://mcp.slack.com/mcp", "https://user@mcp.slack.com/mcp"} {
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		checkPin(t, r)
	}
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "wrong.example"
	checkPin(t, r)
}

func checkPin(t *testing.T, r *http.Request) {
	t.Helper()
	resp, err := new(boundTransport).RoundTrip(r)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, native.ErrDenied) {
		t.Fatalf("request left pin: %v", err)
	}
}

func openApp(t *testing.T, actor string) *app.App {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("notifications: {provider: slack, settings: {native: {workspace_id: %s, channel_id: %s, actor_id: %q}}}\n", targetTeam, targetChannel, actor)
	if err := os.WriteFile(filepath.Join(dir, ".agentflow", "config.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yml"), []byte("autonomy: {remote: confirm}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(t.Context(), dir, "claude-desktop")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestNotifyWireReceiptAndReplay(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		sendErr error
		result  string
		want    app.NotificationStatus
	}{
		{"structured", 0, nil, `{"structuredContent":` + ack + `,"content":[]}`, app.NotificationDelivered},
		{"json text", 0, nil, `{"content":[{"type":"text","text":` + quoted(ack) + `}]}`, app.NotificationDelivered},
		{"provider error", 0, nil, `{"isError":true,"content":[],"structuredContent":` + ack + `}`, app.NotificationUnconfirmed},
		{"wrong channel", 0, nil, strings.ReplaceAll(`{"content":[],"structuredContent":`+ack+`}`, targetChannel, "C1123456789"), app.NotificationUnconfirmed},
		{"follow-up requested", 0, nil, `{"resultType":"input_required","content":[],"structuredContent":` + ack + `}`, app.NotificationUnconfirmed},
		{"bare prose", 0, nil, `{"content":[{"type":"text","text":"message sent"}]}`, app.NotificationUnconfirmed},
		{"rejected", 403, nil, "", app.NotificationUnconfirmed}, {"expired", 401, nil, "", app.NotificationUnconfirmed},
		{"redirect", 307, nil, "", app.NotificationUnconfirmed}, {"throttled", 429, nil, "", app.NotificationUnconfirmed},
		{"response lost", 0, io.ErrUnexpectedEOF, "", app.NotificationUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w := newFixture(t)
			a := openApp(t, "")
			f.expectClaimDir = filepath.Join(a.Home, "notification-receipts")
			f.sendStatus, f.sendErr, f.sendResult = tc.status, tc.sendErr, json.RawMessage(tc.result)
			in := app.NotifyInput{Signal: "delivery", OperationID: "submitted/42", Body: "Rollout"}
			ctx := native.WithWriteHost(t.Context(), w)
			for i := range 2 {
				_, pending, err := a.Notify(ctx, in, "")
				if err != nil || pending == nil || f.writes != i {
					t.Fatalf("preview %d: %v, pending=%v, writes=%d", i, err, pending != nil, f.writes)
				}
				summary, _ := json.Marshal(pending.Summary)
				if !strings.Contains(string(summary), "U0123456789") || strings.Contains(string(summary), f.token) {
					t.Fatal("preview omitted actor or exposed bearer")
				}
				res, pending, err := a.Notify(ctx, in, pending.ConfirmToken)
				want := tc.want
				if i == 1 && want == app.NotificationDelivered {
					want = app.NotificationAlreadyDelivered
				}
				if err != nil || pending != nil || len(res.Deliveries) != 1 || res.Deliveries[0].Status != want ||
					res.Deliveries[0].ReceiptID == "" || want == app.NotificationDelivered && res.Deliveries[0].URL != permalink || f.writes != 1 {
					t.Fatalf("attempt %d: %+v, %v, writes=%d", i, res, err, f.writes)
				}
			}
		})
	}
}

func quoted(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestNotifyChecksGrantBeforeConnecting(t *testing.T) {
	for _, tc := range []struct {
		name, team, user, configuredActor string
		match                             bool
	}{
		{"wrong workspace without configured actor", "T1123456789", targetUser, "", false},
		{"wrong enrolled user", targetTeam, "U1123456789", targetUser, false},
		{"configured actor matches", targetTeam, targetUser, targetUser, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w := newFixture(t)
			f.grant.Team, f.grant.User = tc.team, tc.user
			a := openApp(t, tc.configuredActor)
			ctx := native.WithWriteHost(t.Context(), w)
			res, pending, err := a.Notify(ctx, app.NotifyInput{Signal: "delivery", OperationID: "submitted/42", Body: "Rollout"}, "")
			if err != nil || f.writes != 0 {
				t.Fatalf("preview: %v, writes=%d", err, f.writes)
			}
			if tc.match {
				if pending == nil || f.connects != 1 || f.reads != 2 {
					t.Fatalf("expected preview after profile and membership checks, connects=%d reads=%d", f.connects, f.reads)
				}
			} else if pending != nil || len(res.Deliveries) != 1 || res.Deliveries[0].Status != app.NotificationFailed ||
				f.connects != 0 || f.catalogs != 0 || f.reads != 0 {
				t.Fatalf("grant mismatch: %+v, connects=%d catalogs=%d reads=%d", res, f.connects, f.catalogs, f.reads)
			}
		})
	}
}

func TestNotifyValidatesEnrolledActor(t *testing.T) {
	f, w := newFixture(t)
	f.profileResult = json.RawMessage(`{"result":"profile unavailable"}`)
	a := openApp(t, "")
	ctx := native.WithWriteHost(t.Context(), w)
	res, pending, err := a.Notify(ctx, app.NotifyInput{Signal: "delivery", OperationID: "submitted/42", Body: "Rollout"}, "")
	if err != nil || pending != nil || len(res.Deliveries) != 1 || res.Deliveries[0].Status != app.NotificationFailed || f.writes != 0 {
		t.Fatalf("unverified actor: %+v, %v, writes=%d", res, err, f.writes)
	}
}

func TestDeclaredOutputSchemaIsEnforced(t *testing.T) {
	f, w := newFixture(t)
	var tool map[string]any
	if err := json.Unmarshal(f.catalog[0], &tool); err != nil {
		t.Fatal(err)
	}
	tool["outputSchema"] = map[string]any{"type": "object", "required": []string{"delivery_id"}}
	var err error
	f.catalog[0], err = json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	b := prepare(t, w)
	_, err = w.Write(t.Context(), b, sendCall())
	if !errors.Is(err, native.ErrWriteUncertain) || errors.Is(err, native.ErrWriteNotDispatched) || f.writes != 1 {
		t.Fatalf("incomplete provider result: %v, writes=%d", err, f.writes)
	}
}

func TestNotifyPredispatchFailureReleasesOnlyForFreshApproval(t *testing.T) {
	f, w := newFixture(t)
	a := openApp(t, "")
	ctx := native.WithWriteHost(t.Context(), w)
	in := app.NotifyInput{Signal: "delivery", OperationID: "submitted/42", Body: "Rollout"}
	_, pending, err := a.Notify(ctx, in, "")
	if err != nil || pending == nil {
		t.Fatalf("preview: %v", err)
	}
	f.replaceAfterList = true
	res, _, err := a.Notify(ctx, in, pending.ConfirmToken)
	if err != nil || len(res.Deliveries) != 1 || res.Deliveries[0].Status != app.NotificationFailed || f.writes != 0 {
		t.Fatalf("replacement: %+v, %v, writes=%d", res, err, f.writes)
	}
	f.replaceAfterList = false
	_, fresh, err := a.Notify(ctx, in, "")
	if err != nil || fresh == nil || fresh.ConfirmToken == pending.ConfirmToken || f.writes != 0 {
		t.Fatal("missing fresh approval")
	}
	res, _, err = a.Notify(ctx, in, fresh.ConfirmToken)
	if err != nil || len(res.Deliveries) != 1 || res.Deliveries[0].Status != app.NotificationDelivered || f.writes != 1 {
		t.Fatalf("fresh approval: %+v, %v, writes=%d", res, err, f.writes)
	}
}
