package asanahost

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/provider/native"
)

func TestMappedWrites(t *testing.T) {
	for _, tool := range []string{create, comment, complete} {
		t.Run(tool, func(t *testing.T) {
			f, w := newFixture(t, tool)
			if tool == complete {
				task := f.payload[getTask].(map[string]any)
				task["memberships"] = append(task["memberships"].([]any), map[string]any{"project": map[string]any{"gid": "99"}})
			}
			f.stream = true
			b := f.prepare(w)
			id := taskID
			if tool != create {
				if _, err := w.ReadBound(t.Context(), b, readCall(id)); err != nil {
					t.Fatal(err)
				}
			} else {
				id = createdID
			}
			f.token = "refreshed"
			if tool == comment {
				body, err := json.Marshal(f.payload[comment])
				if err != nil {
					t.Fatal(err)
				}
				f.overrideTool = tool
				f.overrideResult = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(body)}}}
			}
			raw, err := w.Write(t.Context(), b, f.prepared)
			if err != nil || !json.Valid(raw) || f.writes != 1 || f.connects != 1 || f.writeToken != "refreshed" {
				t.Fatalf("write: %s %v", raw, err)
			}
			if tool != comment {
				if _, err := w.ReadBound(t.Context(), b, readCall(id)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := w.Write(t.Context(), b, f.prepared); !errors.Is(err, native.ErrWriteNotDispatched) || f.writes != 1 {
				t.Fatal("replayed binding", err)
			}
		})
	}
}

func TestRejectWiderOperations(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		mutate     func(*native.Call)
	}{
		{"server", comment, func(c *native.Call) { c.Server = "alias" }},
		{"add project", create, func(c *native.Call) { c.Arguments["default_project"] = "99" }},
		{"move item", create, func(c *native.Call) { c.Arguments["tasks"].([]any)[0].(map[string]any)["project_id"] = "99" }},
		{"assignee", create, func(c *native.Call) { c.Arguments["tasks"].([]any)[0].(map[string]any)["assignee"] = "22" }},
		{"batch", complete, func(c *native.Call) {
			items := c.Arguments["tasks"].([]any)
			c.Arguments["tasks"] = append(items, items[0])
		}},
		{"rename", complete, func(c *native.Call) { c.Arguments["tasks"].([]any)[0].(map[string]any)["name"] = "renamed" }},
		{"reopen", complete, func(c *native.Call) { c.Arguments["tasks"].([]any)[0].(map[string]any)["completed"] = false }},
		{"pinned", comment, func(c *native.Call) { c.Arguments["is_pinned"] = true }},
		{"html", comment, func(c *native.Call) { c.Arguments["html_text"] = "<b>more</b>" }},
		{"blank", comment, func(c *native.Call) { c.Arguments["text"] = " " }},
		{"another tool", create, func(c *native.Call) { c.Tool = "create_project" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w := newFixture(t, tc.tool)
			tc.mutate(&f.prepared)
			if _, err := w.Prepare(t.Context(), f.prepared); !errors.Is(err, native.ErrDenied) || f.opens != 0 {
				t.Fatalf("unsafe preview: %v", err)
			}
		})
	}
	f, _ := newFixture(t, complete)
	if _, err := prepareOperation(Scope{Project: projectID}, f.prepared); !errors.Is(err, native.ErrDenied) {
		t.Fatal("global completion without warning path")
	}
}

func TestReadScope(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		after      bool
		read       native.Call
	}{
		{"no task before create", create, false, readCall(createdID)}, {"different created task", create, true, readCall(taskID)},
		{"arbitrary task", comment, false, readCall("999")}, {"different completion target", complete, true, readCall("999")},
		{"account", comment, false, native.Call{Provider: "asana", Server: "asana", Tool: "get_user", Arguments: map[string]any{"user_id": "22"}}},
		{"project", create, false, native.Call{Provider: "asana", Server: "asana", Tool: getProject, Arguments: map[string]any{"project_id": "99"}}},
		{"include comments", comment, false, native.Call{Provider: "asana", Server: "asana", Tool: getTask, Arguments: map[string]any{"task_gid": taskID, "include_comments": true, "include_subtasks": false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w := newFixture(t, tc.tool)
			b := f.prepare(w)
			if tc.after {
				if _, err := w.Write(t.Context(), b, f.prepared); err != nil {
					t.Fatal(err)
				}
			}
			reads := f.reads
			if _, err := w.ReadBound(t.Context(), b, tc.read); !errors.Is(err, native.ErrDenied) || f.reads != reads {
				t.Fatal("unscoped read", err)
			}
			if _, err := w.Write(t.Context(), b, f.prepared); !errors.Is(err, native.ErrWriteNotDispatched) {
				t.Fatal("lease survived denial", err)
			}
		})
	}
	f, w := newFixture(t, comment)
	b := f.prepare(w)
	changed := f.prepared
	changed.Arguments = map[string]any{"task_id": taskID, "text": "changed"}
	if _, err := w.Rebind(t.Context(), b, changed); !errors.Is(err, native.ErrDenied) {
		t.Fatal("payload changed", err)
	}
	if _, err := w.ReadForWrite(t.Context(), readCall(taskID)); !errors.Is(err, native.ErrDenied) {
		t.Fatal("unbound task read", err)
	}
}

func TestGrantAndPreflight(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture)
		want   error
	}{
		{"no enrollment", func(f *fixture) { f.openErr = errNotEnrolled }, native.ErrUnavailable},
		{"expired", func(f *fixture) { f.openErr = errLoginRequired }, native.ErrSignIn},
		{"denied", func(f *fixture) { f.openErr = errors.New(secret) }, native.ErrDenied},
		{"denial takes precedence over absence", func(f *fixture) { f.openErr = errors.Join(errNotEnrolled, errGrantDenied) }, native.ErrDenied},
		{"missing principal", func(f *fixture) { f.binding.User = "" }, native.ErrDenied},
		{"wrong principal", func(f *fixture) { f.binding.User = "23" }, native.ErrDenied},
		{"wrong workspace", func(f *fixture) { f.binding.Workspace = "34" }, native.ErrDenied},
		{"different project", func(f *fixture) { f.payload[getProject].(map[string]any)["gid"] = "11" }, native.ErrDenied},
		{"wrong task", func(f *fixture) { f.payload[getTask].(map[string]any)["gid"] = "43" }, native.ErrDenied},
		{"wrong task workspace", func(f *fixture) { f.payload[getTask].(map[string]any)["workspace"] = map[string]any{"gid": "34"} }, native.ErrDenied},
		{"no membership", func(f *fixture) { f.payload[getTask].(map[string]any)["memberships"] = []any{} }, native.ErrDenied},
		{"missing notes", func(f *fixture) { delete(f.payload[getTask].(map[string]any), "notes") }, native.ErrDenied},
		{"ambiguous project", func(f *fixture) { f.payload[getProject].(map[string]any)["data"] = f.payload[getMe] }, native.ErrDenied},
		{"401", func(f *fixture) { f.overrideTool = getProject; f.status = 401 }, native.ErrDenied},
		{"403", func(f *fixture) { f.overrideTool = getTask; f.status = 403 }, native.ErrDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w := newFixture(t, comment)
			tc.change(f)
			_, err := w.Prepare(t.Context(), f.prepared)
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), secret) || f.writes != 0 ||
				errors.Is(err, native.ErrUnavailable) != errors.Is(tc.want, native.ErrUnavailable) {
				t.Fatalf("preview: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*fixture)
	}{
		{"revoked between final read and write", func(f *fixture) { f.rotateBeforeWrite = true }},
		{"changed generation", func(f *fixture) { f.generation = "other" }},
		{"changed task", func(f *fixture) { f.payload[getTask].(map[string]any)["notes"] = "changed" }},
		{"changed workspace", func(f *fixture) { f.payload[getProject].(map[string]any)["workspace"] = map[string]any{"gid": "34"} }},
		{"already completed", func(f *fixture) { f.payload[getTask].(map[string]any)["completed"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w := newFixture(t, complete)
			b := f.prepare(w)
			tc.change(f)
			_, err := w.Write(t.Context(), b, f.prepared)
			if !errors.Is(err, native.ErrWriteNotDispatched) || errors.Is(err, native.ErrWriteUncertain) || errors.Is(err, native.ErrUnavailable) || strings.Contains(err.Error(), secret) || f.writes != 0 {
				t.Fatalf("changed precondition: %v", err)
			}
			if _, err = w.Write(t.Context(), b, f.prepared); !errors.Is(err, native.ErrDenied) || f.writes != 0 {
				t.Fatal("denial replayed")
			}
		})
	}
}

func TestSchemaGates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture)
	}{
		{"missing verification", func(f *fixture) { f.catalog = f.catalog[1:] }},
		{"duplicate", func(f *fixture) { f.catalog = append(f.catalog, f.catalog[3]) }},
		{"cursor cycle", func(f *fixture) { f.cycle = true }},
		{"malformed output", func(f *fixture) { f.catalog[3]["outputSchema"] = 42 }},
		{"untyped input", func(f *fixture) { f.catalog[3]["inputSchema"] = map[string]any{"type": "object"} }},
		{"open batch items", func(f *fixture) {
			f.catalog[3]["inputSchema"].(map[string]any)["properties"].(map[string]any)["tasks"] = map[string]any{"type": "array", "items": map[string]any{"type": "object"}}
		}},
		{"undeclared nested project", func(f *fixture) {
			item := f.catalog[3]["inputSchema"].(map[string]any)["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
			delete(item["properties"].(map[string]any), "project_id")
		}},
		{"incompatible readback", func(f *fixture) { f.catalog[2]["inputSchema"].(map[string]any)["required"] = []string{"workspace_id"} }},
		{"extra required write field", func(f *fixture) { f.catalog[3]["inputSchema"].(map[string]any)["required"] = []string{"parent"} }},
		{"invalid reference", func(f *fixture) {
			f.catalog[3]["outputSchema"] = map[string]any{"type": "object", "$ref": "https://invalid.example/schema"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w := newFixture(t, create)
			tc.change(f)
			if _, err := w.Prepare(t.Context(), f.prepared); !errors.Is(err, native.ErrDenied) || f.writes != 0 {
				t.Fatal("accepted incompatible catalog", err)
			}
		})
	}
	f, w := newFixture(t, create)
	b := f.prepare(w)
	f.catalog[2]["outputSchema"] = map[string]any{"type": "object"}
	if _, err := w.Write(t.Context(), b, f.prepared); !errors.Is(err, native.ErrWriteNotDispatched) || f.writes != 0 {
		t.Fatal("accepted schema drift", err)
	}
}

func TestUncertainDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		change     func(*fixture)
	}{
		{"forbidden", comment, func(f *fixture) { f.overrideTool = comment; f.status = 403 }},
		{"redirect", create, func(f *fixture) { f.overrideTool = create; f.status = 307 }},
		{"throttled", complete, func(f *fixture) { f.overrideTool = complete; f.status = 429 }},
		{"lost reply", comment, func(f *fixture) { f.overrideTool = comment; f.wireErr = errors.New(secret) }},
		{"tool error", create, func(f *fixture) {
			f.overrideTool = create
			f.overrideResult = map[string]any{"isError": true, "structuredContent": f.payload[create], "content": []any{}}
		}},
		{"follow-up", comment, func(f *fixture) {
			f.overrideTool = comment
			f.overrideResult = map[string]any{"resultType": "input_required", "content": []any{}, "structuredContent": f.payload[comment]}
		}},
		{"contradictory comment", comment, func(f *fixture) {
			f.payload[comment].(map[string]any)["data"].(map[string]any)["target"] = map[string]any{"gid": "43"}
		}},
		{"text despite declared output", comment, func(f *fixture) {
			f.catalog[4]["outputSchema"] = map[string]any{"type": "object"}
			f.overrideTool = comment
			f.overrideResult = map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"gid":"88","anchor_gid":"42"}`}}}
		}},
		{"invalid structured output", create, func(f *fixture) {
			f.catalog[3]["outputSchema"] = map[string]any{"type": "object", "required": []string{"provider_receipt_id"}}
		}},
		{"missing failure count", create, func(f *fixture) { delete(f.payload[create].(map[string]any), "failed") }},
		{"partial success", create, func(f *fixture) { f.payload[create].(map[string]any)["failed"] = []any{secret} }},
		{"extra success", create, func(f *fixture) {
			f.payload[create].(map[string]any)["succeeded"] = []any{map[string]any{"gid": "77"}, map[string]any{"gid": "78"}}
		}},
		{"uncertain target", create, func(f *fixture) {
			f.payload[create].(map[string]any)["succeeded"] = []any{map[string]any{"gid": "77", "task_id": "78"}}
		}},
		{"error in success", create, func(f *fixture) { f.payload[create].(map[string]any)["errors"] = []any{secret} }},
		{"wrong create details", create, func(f *fixture) { f.payload[getTask].(map[string]any)["notes"] = "missing metadata" }},
		{"revoked before readback", create, func(f *fixture) { f.afterWrite = func() { f.generation = "replacement" } }},
		{"completion readback unchanged", complete, func(f *fixture) { f.afterWrite = func() { f.payload[getTask].(map[string]any)["completed"] = false } }},
		{"readback denied", create, func(f *fixture) { f.overrideTool = getTask; f.status = 403 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w := newFixture(t, tc.tool)
			tc.change(f)
			b := f.prepare(w)
			raw, err := w.Write(t.Context(), b, f.prepared)
			if raw != nil || !errors.Is(err, native.ErrWriteUncertain) || errors.Is(err, native.ErrWriteNotDispatched) || errors.Is(err, native.ErrUnavailable) || strings.Contains(err.Error(), secret) || f.writes != 1 {
				t.Fatalf("result: %s %v writes=%d", raw, err, f.writes)
			}
			if tc.tool == create && !strings.Contains(tc.name, "readback") && tc.name != "wrong create details" && len(f.taskReads) != 0 {
				t.Fatal("unverified receipt authorized task read")
			}
			if _, err = w.ReadBound(t.Context(), b, readCall(createdID)); !errors.Is(err, native.ErrDenied) {
				t.Fatal("uncertain receipt authorized readback")
			}
			if _, err = w.Write(t.Context(), b, f.prepared); !errors.Is(err, native.ErrWriteNotDispatched) || f.writes != 1 {
				t.Fatal("uncertain write replayed")
			}
		})
	}
}

func TestEndpointPin(t *testing.T) {
	for _, tc := range []struct{ method, url, host string }{
		{"POST", "http://mcp.asana.com/v2/mcp", ""}, {"POST", Endpoint + "?extra", ""}, {"POST", Endpoint + "/", ""}, {"POST", Endpoint + "#fragment", ""},
		{"POST", "https://mcp.asana.com/v2/command/mcp", ""}, {"POST", "https://user@mcp.asana.com/v2/mcp", ""}, {"POST", Endpoint, "other.example"}, {"GET", Endpoint, ""},
	} {
		req, err := http.NewRequestWithContext(t.Context(), tc.method, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = tc.host
		resp, err := new(boundTransport).RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, native.ErrDenied) || !errors.Is(err, native.ErrWriteNotDispatched) {
			t.Fatal(tc, err)
		}
	}
}

func TestReadAfterGrantChange(t *testing.T) {
	f, w := newFixture(t, create)
	b := f.prepare(w)
	if _, err := w.Write(t.Context(), b, f.prepared); err != nil {
		t.Fatal(err)
	}
	f.generation = "new"
	if _, err := w.ReadBound(t.Context(), b, readCall(createdID)); !errors.Is(err, native.ErrDenied) || !reflect.DeepEqual(f.taskReads, []string{createdID}) {
		t.Fatal("grant changed during readback", err)
	}
}
