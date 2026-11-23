package asanahost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
)

const projectID = "10"
const taskID = "42"
const createdID = "77"
const secret = "fixture-private-message"

var errNotEnrolled = errors.New("absent")
var errGrantDenied = errors.New("grant denied")
var errLoginRequired = errors.New("login required")

type fixture struct {
	t                                        *testing.T
	binding                                  Binding
	openErr                                  error
	token, generation                        string
	locked, rotateBeforeWrite, stream, cycle bool
	catalog                                  []map[string]any
	payload                                  map[string]any
	overrideTool                             string
	overrideResult                           any
	afterWrite                               func()
	status                                   int
	wireErr                                  error
	prepared                                 native.Call
	opens, connects, writes, reads           int
	taskReads                                []string
	writeToken                               string
}

func newFixture(t *testing.T, tool string) (*fixture, *native.SessionWriter) {
	t.Helper()
	f := &fixture{t: t, binding: Binding{"gen", "22", "33"}, token: "fixture-bearer", generation: "gen"}
	data, err := os.ReadFile("testdata/catalog.json")
	if err != nil || json.Unmarshal(data, &f.catalog) != nil {
		t.Fatal("invalid fixture catalog", err)
	}
	ref := provider.Ref{Instance: "asana", Scope: projectID, ID: taskID}
	var mapped nativemap.TaskWrite
	switch tool {
	case create:
		link := provider.Ref{Instance: "asana", Scope: projectID, ID: "55"}
		mapped, err = nativemap.AsanaCreate(provider.TaskDraft{Scope: projectID, Title: "Pilot", Description: "Details", Links: []provider.Link{{Ref: &link}}})
	case complete:
		mapped, err = nativemap.AsanaComplete(ref)
	case comment:
		mapped, err = nativemap.AsanaComment(ref, "Review this")
	default:
		t.Fatal("unknown fixture tool")
	}
	if err != nil {
		t.Fatal(err)
	}
	f.prepared = mapped.Call()
	task := map[string]any{"gid": taskID, "name": "Pilot", "notes": "Details", "completed": false,
		"memberships": []any{map[string]any{"project": map[string]any{"gid": projectID}}}}
	if tool == create {
		task["gid"] = createdID
		task["notes"] = f.prepared.Arguments["tasks"].([]any)[0].(map[string]any)["notes"]
	}
	f.payload = map[string]any{
		getMe:      map[string]any{"data": map[string]any{"gid": "22"}},
		getProject: map[string]any{"gid": projectID, "workspace": map[string]any{"gid": "33"}},
		getTask:    task,
		create:     map[string]any{"succeeded": []any{map[string]any{"gid": createdID, "name": "Pilot"}}, "failed": []any{}},
		complete:   map[string]any{"succeeded": []any{map[string]any{"task_id": taskID, "completed": true}}, "failed": []any{}},
		comment:    map[string]any{"data": map[string]any{"gid": "88", "anchor_gid": taskID, "text": "Review this"}},
	}
	w := newWriter(Enrollment{
		BoundToken: func(context.Context) (Binding, error) { f.opens++; return f.binding, f.openErr },
		WithBoundToken: func(_ context.Context, generation string, dispatch func(string) error) error {
			if generation != f.generation {
				return errors.New(secret)
			}
			f.locked = true
			defer func() { f.locked = false }()
			return dispatch(f.token)
		},
		ErrNotEnrolled: errNotEnrolled, ErrLoginRequired: errLoginRequired, ErrDenied: errGrantDenied,
	}, Scope{Project: projectID, EnableCompletion: true}, f)
	t.Cleanup(w.Close)
	return f, w
}

func (f *fixture) RoundTrip(req *http.Request) (*http.Response, error) {
	f.t.Helper()
	if req.URL.String() != "https://mcp.asana.com/v2/mcp" || !f.locked || req.Header.Get("Authorization") != "Bearer "+f.token || req.GetBody != nil {
		f.t.Fatal("request escaped endpoint or grant lock, or could be replayed")
	}
	rec := httptest.NewRecorder()
	if req.Method == http.MethodDelete {
		rec.WriteHeader(http.StatusNoContent)
		return rec.Result(), nil
	}
	if req.Method != http.MethodPost {
		f.t.Fatal("unexpected HTTP method")
	}
	var rpc struct {
		ID     json.RawMessage
		Method string
		Params struct {
			Name, Cursor string
			Arguments    map[string]any
		}
	}
	if err := json.NewDecoder(req.Body).Decode(&rpc); err != nil {
		f.t.Fatal(err)
	}
	if rpc.Method != "initialize" && req.Header.Get("Mcp-Session-Id") != "fixture-session" {
		f.t.Fatal("request left original session")
	}
	rec.Header().Set("Content-Type", "application/json")
	var result any
	switch rpc.Method {
	case "initialize":
		f.connects++
		rec.Header().Set("Mcp-Session-Id", "fixture-session")
		result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
	case "notifications/initialized":
		rec.WriteHeader(http.StatusAccepted)
		return rec.Result(), nil
	case "tools/list":
		if rpc.Params.Cursor == "" {
			result = map[string]any{"tools": f.catalog[:1], "nextCursor": "second"}
		} else {
			page := map[string]any{"tools": f.catalog[1:]}
			if f.cycle {
				page["nextCursor"] = "second"
			}
			result = page
		}
	case "tools/call":
		tool := rpc.Params.Name
		args := rpc.Params.Arguments
		switch tool {
		case getMe:
			if len(args) != 0 {
				f.t.Fatal("unexpected account lookup")
			}
			f.reads++
		case getProject:
			if !reflect.DeepEqual(args, map[string]any{"project_id": projectID}) {
				f.t.Fatal("unexpected project lookup")
			}
			f.reads++
			if f.rotateBeforeWrite && f.prepared.Tool == create {
				f.generation = "replacement"
			}
		case getTask:
			id := taskID
			if f.prepared.Tool == create {
				id = createdID
				if f.writes != 1 {
					f.t.Fatal("create task read before receipt")
				}
			}
			if !reflect.DeepEqual(args, map[string]any{"task_gid": id, "include_comments": false, "include_subtasks": false}) {
				f.t.Fatalf("unscoped task read: %v", args)
			}
			f.taskReads = append(f.taskReads, id)
			f.reads++
			if f.rotateBeforeWrite {
				f.generation = "replacement"
			}
		case f.prepared.Tool:
			if !reflect.DeepEqual(f.prepared.Arguments, args) {
				f.t.Fatal("write payload changed")
			}
			f.writes++
			f.writeToken = f.token
			if tool == complete {
				f.payload[getTask].(map[string]any)["completed"] = true
			}
			if f.afterWrite != nil {
				f.afterWrite()
			}
		default:
			f.t.Fatal("unexpected tool", tool)
		}
		result = map[string]any{"structuredContent": f.payload[tool], "content": []any{}}
		if f.overrideTool == tool {
			if f.wireErr != nil {
				return nil, f.wireErr
			}
			if f.status != 0 {
				rec.Header().Set("Location", "https://other.example/")
				rec.WriteHeader(f.status)
				_, _ = rec.WriteString(secret)
				return rec.Result(), nil
			}
			result = f.overrideResult
		}
	default:
		f.t.Fatal("unexpected method", rpc.Method)
	}
	packet, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
	if err != nil {
		f.t.Fatal(err)
	}
	if f.stream && rpc.Method == "tools/call" {
		rec.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(rec, "event: message\ndata: %s\n\n", packet)
	} else {
		_, _ = rec.Write(packet)
	}
	return rec.Result(), nil
}

func (f *fixture) prepare(w *native.SessionWriter) native.WriteBinding {
	f.t.Helper()
	b, err := w.Prepare(f.t.Context(), f.prepared)
	if err != nil {
		f.t.Fatal(err)
	}
	if b.Owner != "devflow" || b.Origin != Origin || b.Connection == "" || b.Payload == "" || f.writes != 0 {
		f.t.Fatal("invalid preview")
	}
	return b
}

func readCall(id string) native.Call {
	return native.Call{Provider: "asana", Server: "asana", Tool: getTask, Arguments: taskArgs(id)}
}
