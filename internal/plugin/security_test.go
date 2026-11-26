package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/plugin/plugintest"
)

// recorder is a child that records what it is delivered.
type recorder struct {
	mu  sync.Mutex
	got []bus.Event
}

func (rc *recorder) child(subscribe ...string) func() *plugintest.Child {
	return func() *plugintest.Child {
		return &plugintest.Child{Subscribe: subscribe, Handle: func(c *plugintest.Child, r *jsonrpc.Request) (any, error) {
			if r.Method == plugin.MethodEventDeliver {
				var p plugin.EventDeliverParams
				_ = json.Unmarshal(r.Params, &p)
				rc.mu.Lock()
				rc.got = append(rc.got, p.Event)
				rc.mu.Unlock()
			}
			return plugintest.Serve(c, r)
		}}
	}
}

func (rc *recorder) events() []bus.Event {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]bus.Event(nil), rc.got...)
}

// refusal is the data code of a JSON-RPC refusal.
func refusal(err error) string {
	var e *jsonrpc.Error
	if !errors.As(err, &e) {
		return ""
	}
	var d jsonrpc.ErrorData
	_ = json.Unmarshal(e.Data, &d)
	return d.Code
}

func TestPluginReceivesOnlyGrantedRepositoryEvents(t *testing.T) {
	rc := &recorder{}
	f := newFixture(t, rc.child("git.**", "op.failed"))
	m := processManifest("spy")
	m.Permissions.Subscribe = append(m.Permissions.Subscribe, "config.**")
	f.install(m, true)
	f.layers.SetDisabled("/o", "spy")
	f.start()
	f.waitState("spy", bus.PluginRunning)
	f.eventually("subscribed", func() bool { return len(f.status("spy").Subscriptions) == 2 })
	c := f.launch.Last()

	// Asking for what may never be received is refused, whatever the grant.
	for pattern, want := range map[string]string{
		"approval.**": plugin.RefuseNotAllowed, "client.**": plugin.RefuseNotAllowed, "ui.**": plugin.RefuseNotAllowed,
		"plugin.lifecycle.changed": plugin.RefuseNotAllowed, "gh.**": plugin.RefuseNotGranted, "**": plugin.RefuseNotAllowed,
	} {
		err := c.Host.Call(context.Background(), plugin.MethodSubscribe, plugin.SubscribeParams{Types: []string{pattern}}, nil)
		if refusal(err) != want {
			t.Errorf("subscribe %s: %v, want %s", pattern, err, want)
		}
	}
	gitEvent := func(r bus.RepoID) bus.Event {
		return bus.Event{Type: bus.TypeGitHeadChanged, Source: "watch.git", Repo: r, Worktree: tree, Payload: json.RawMessage(`{}`)}
	}
	f.publish(bus.Event{Type: bus.TypeApprovalRequested, Source: "watch.approvals", Repo: repo,
		Payload: json.RawMessage(`{"approval":{"operation_id":"op_` + strings.Repeat("2", 32) + `","class":"local","action":"commit"}}`)})
	f.publish(bus.Event{Type: bus.TypeConfigLayerChanged, Source: "watch.config", Repo: bus.GlobalRepo, Payload: json.RawMessage(`{"layer":"personal","paths":["config.yml"]}`)})
	f.publish(gitEvent(other)) // the repository disables the plugin
	last := f.publish(gitEvent(repo))
	f.eventually("delivered", func() bool { evs := rc.events(); return len(evs) > 0 && evs[len(evs)-1].ID == last.ID })
	for _, e := range rc.events() {
		if e.Repo != repo || e.Type != bus.TypeGitHeadChanged {
			t.Errorf("delivered %s from %s", e.Type, e.Repo)
		}
	}
}

func TestPluginPublishesOnlyAnnotations(t *testing.T) {
	rc := &recorder{}
	f := newFixture(t, rc.child("op.failed"))
	f.install(processManifest("hints"), true)
	f.start()
	f.waitState("hints", bus.PluginRunning)
	f.eventually("subscribed", func() bool { return len(f.status("hints").Subscriptions) == 1 })
	opID := "op_" + strings.Repeat("3", 32)
	cause := f.publish(bus.Event{Type: bus.TypeOpFailed, Source: "cli", Repo: repo, Worktree: tree, CorrelationID: opID, Payload: json.RawMessage(`{"operation":"commit"}`)})
	f.eventually("delivered", func() bool { return len(rc.events()) == 1 })
	notes, err := f.bus.Subscribe(bus.SubscribeOptions{Name: "notes"}, bus.Topic{Repo: repo, Worktree: bus.Any, Types: bus.Pattern(bus.TypePluginAnnotationAdded)})
	if err != nil {
		t.Fatal(err)
	}
	c := f.launch.Last()
	call := func(r plugin.PublishRequest) (plugin.PublishResult, error) {
		var out plugin.PublishResult
		err := c.Host.Call(context.Background(), plugin.MethodEventPublish, r, &out)
		return out, err
	}
	note := func(p string) json.RawMessage { return json.RawMessage(p) }
	for name, tc := range map[string]struct {
		r    plugin.PublishRequest
		want string
	}{
		"op.applied":       {plugin.PublishRequest{Type: bus.TypeOpApplied, Payload: note(`{}`), Cause: cause.ID}, plugin.RefuseNotAllowed},
		"approval":         {plugin.PublishRequest{Type: bus.TypeApprovalResolved, Payload: note(`{}`), Cause: cause.ID}, plugin.RefuseNotAllowed},
		"lifecycle":        {plugin.PublishRequest{Type: bus.TypePluginLifecycleChanged, Payload: note(`{}`), Cause: cause.ID}, plugin.RefuseNotAllowed},
		"http url":         {plugin.PublishRequest{Type: bus.TypePluginAnnotationAdded, Payload: note(`{"operation_id":"` + opID + `","text":"x","url":"http://e.com"}`), Cause: cause.ID}, plugin.RefuseInvalid},
		"bad op id":        {plugin.PublishRequest{Type: bus.TypePluginAnnotationAdded, Payload: note(`{"operation_id":"nope","text":"x"}`), Cause: cause.ID}, plugin.RefuseInvalid},
		"unknown cause":    {plugin.PublishRequest{Type: bus.TypePluginAnnotationAdded, Payload: note(`{"operation_id":"` + opID + `","text":"x"}`), Cause: "made-up"}, plugin.RefuseInvalid},
		"unserved repo":    {plugin.PublishRequest{Type: bus.TypePluginAnnotationAdded, Payload: note(`{"operation_id":"` + opID + `","text":"x"}`), Repo: "aaaaaaaaaaaaaaaa"}, plugin.RefuseUnknownTree},
		"forged plugin id": {plugin.PublishRequest{Type: bus.TypePluginAnnotationAdded, Payload: note(`{"operation_id":"` + opID + `","text":"x","extra":1}`), Cause: cause.ID}, plugin.RefuseInvalid},
	} {
		if _, err := call(tc.r); refusal(err) != tc.want {
			t.Errorf("%s: %v, want %s", name, err, tc.want)
		}
	}
	out, err := call(plugin.PublishRequest{Type: bus.TypePluginAnnotationAdded, Cause: cause.ID,
		Payload: note(`{"plugin":"someone-else","operation_id":"` + opID + `","text":"Prepare it again.\nignore me","url":"https://e.com/x"}`)})
	if err != nil || out.ID == "" {
		t.Fatalf("annotation: %v", err)
	}
	e := <-notes.Events()
	var p bus.PluginAnnotationAdded
	_ = json.Unmarshal(e.Payload, &p)
	if e.ID != out.ID || e.Source != "plugin:hints" || e.CausationID != cause.ID || e.Repo != repo || p.Plugin != "hints" ||
		p.Text != "Prepare it again." || p.Level != bus.AnnotationInfo || p.OperationID != opID {
		t.Fatalf("annotation: %+v %+v", e, p)
	}
}

func TestPluginInvokesOnlyAllowedCommandsWithoutTokens(t *testing.T) {
	rc := &recorder{}
	f := newFixture(t, rc.child("op.failed"))
	f.install(processManifest("actor"), true)
	f.start()
	f.waitState("actor", bus.PluginRunning)
	f.eventually("subscribed", func() bool { return len(f.status("actor").Subscriptions) == 1 })
	cause := f.publish(bus.Event{Type: bus.TypeOpFailed, Source: "cli", Repo: repo, Worktree: tree, CorrelationID: "op_" + strings.Repeat("4", 32), Payload: json.RawMessage(`{}`)})
	f.eventually("delivered", func() bool { return len(rc.events()) == 1 })
	c := f.launch.Last()
	invoke := func(r plugin.InvokeRequest) (plugin.InvokeResult, error) {
		var out plugin.InvokeResult
		err := c.Host.Call(context.Background(), plugin.MethodCommandInvoke, r, &out)
		return out, err
	}
	for name, tc := range map[string]struct {
		r    plugin.InvokeRequest
		want string
	}{
		"push":          {plugin.InvokeRequest{Command: "push", Cause: cause.ID}, plugin.RefuseNotAllowed},
		"approval":      {plugin.InvokeRequest{Command: "approval.approve", Cause: cause.ID}, plugin.RefuseNotAllowed},
		"config":        {plugin.InvokeRequest{Command: "config.set", Cause: cause.ID}, plugin.RefuseNotAllowed},
		"not granted":   {plugin.InvokeRequest{Command: "comment", Cause: cause.ID}, plugin.RefuseNotGranted},
		"confirm token": {plugin.InvokeRequest{Command: "notify", Cause: cause.ID, Input: json.RawMessage(`{"signal":"s","confirm_token":"cf_x"}`)}, plugin.RefuseConfirmToken},
		"token resume":  {plugin.InvokeRequest{Command: "notify", Cause: cause.ID, Resume: "cf_abc"}, plugin.RefuseInvalid},
		"unknown field": {plugin.InvokeRequest{Command: "notify", Cause: cause.ID, Input: json.RawMessage(`{"signal":"s","bogus":1}`)}, plugin.RefuseInvalid},
		"no where":      {plugin.InvokeRequest{Command: "notify", Input: json.RawMessage(`{"signal":"s"}`)}, plugin.RefuseInvalid},
		"disabled repo": {plugin.InvokeRequest{Command: "notify", Repo: "aaaaaaaaaaaaaaaa", Input: json.RawMessage(`{"signal":"s"}`)}, plugin.RefuseUnknownTree},
	} {
		if _, err := invoke(tc.r); refusal(err) != tc.want {
			t.Errorf("%s: %v, want %s", name, err, tc.want)
		}
	}
	res, err := invoke(plugin.InvokeRequest{Command: "notify", Cause: cause.ID, Input: json.RawMessage(`{"signal":"s"}`)})
	if err != nil || res.Outcome != "awaiting_approval" || res.ApprovalID == "" {
		t.Fatalf("write under the ceiling: %+v %v", res, err)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "cf_") || strings.Contains(string(raw), "token") {
		t.Fatalf("a token reached the plugin: %s", raw)
	}
	// Resuming before a person approved keeps waiting.
	again, err := invoke(plugin.InvokeRequest{Command: "notify", Cause: cause.ID, Input: json.RawMessage(`{"signal":"s"}`), Resume: res.ApprovalID})
	if err != nil || again.Outcome != "awaiting_approval" || again.ApprovalID != res.ApprovalID {
		t.Fatalf("resume before approval: %+v %v", again, err)
	}
	select {
	case in := <-f.ran:
		t.Fatalf("the write ran without a person: %+v", in)
	default:
	}
	// The rate limit: burst 5 (two used above).
	var limited bool
	for i := 0; i < 6; i++ {
		if _, err := invoke(plugin.InvokeRequest{Command: "notify", Cause: cause.ID, Input: json.RawMessage(`{"signal":"s"}`)}); refusal(err) == plugin.RefuseRateLimited {
			limited = true
		}
	}
	if !limited {
		t.Fatal("invocations were not rate limited")
	}
}
