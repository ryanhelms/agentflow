package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// errStageDenied is what the registered test stage returns.
var errStageDenied = errors.New("denied by the registered stage")

func init() {
	// Acts only on its own operation, so other tests are unaffected.
	command.RegisterStage(command.StageFunc(func(ctx context.Context, c *command.Call, next command.Handler) (any, error) {
		if c.Meta.Name == "stage-denied" {
			return nil, errStageDenied
		}
		return next(ctx, c)
	}))
}

func TestRegisteredStagesRunInEveryInvoker(t *testing.T) {
	var ran bool
	s := command.Spec[fakeIn, fakeOut]{Name: "stage-denied", Policy: command.Policy{Class: policy.Local, Action: "commit"},
		Run: func(context.Context, *app.App, fakeIn) (any, error) { ran = true; return fakeOut{}, nil }}
	var order []string
	own := command.StageFunc(func(ctx context.Context, c *command.Call, next command.Handler) (any, error) {
		order = append(order, "own")
		return next(ctx, c)
	})
	em := &commandtest.Emitter{}
	inv := command.NewInvoker(command.Options{Emitters: em.Source(), Stages: []command.Stage{own}})
	_, err := command.Invoke(context.Background(), inv, nil, s, fakeIn{Message: "x"}, command.Origin{Source: "cli", Client: "cli"})
	if !errors.Is(err, errStageDenied) || ran {
		t.Fatalf("registered stage did not stop the call: err=%v ran=%v", err, ran)
	}
	if len(order) != 1 {
		t.Fatalf("Options.Stages must run before the registered ones: %v", order)
	}
	if len(em.Events()) != 0 {
		t.Fatalf("a stopped call published %v", em.Types())
	}
	if len(command.RegisteredStages()) == 0 {
		t.Fatal("RegisteredStages is empty")
	}
}

func TestInvokeJSONDecodesStrictly(t *testing.T) {
	var got fakeIn
	s := command.Spec[fakeIn, fakeOut]{Name: "jsontest", Policy: command.Policy{Class: policy.Local, Action: "commit"},
		Token: func(in *fakeIn) *string { return &in.ConfirmToken },
		Run: func(_ context.Context, _ *app.App, in fakeIn) (any, error) {
			got = in
			return fakeOut{Status: "ok"}, nil
		}}
	var jc command.JSONCommand = s
	em := &commandtest.Emitter{}
	inv := command.NewInvoker(command.Options{Emitters: em.Source()})
	o := command.Origin{Source: "plugin:x", Client: "plugin:x", Cause: "cause-1"}
	ctx := context.Background()

	r := jc.InvokeJSON(ctx, inv, nil, json.RawMessage(`{"message":"hi"}`), "", o)
	if r.Err != nil || r.Outcome.Kind != command.OutcomeApplied || got.Message != "hi" || r.OperationID == "" {
		t.Fatalf("valid input: %+v got=%+v", r, got)
	}
	if evs := em.Events(); len(evs) != 2 || evs[0].CausationID != "cause-1" || evs[0].Source != "plugin:x" {
		t.Fatalf("events: %+v", evs)
	}

	for name, input := range map[string]string{
		"unknown field":      `{"message":"hi","extra":1}`,
		"confirm token":      `{"message":"hi","confirm_token":"cf_x"}`,
		"confirm token case": `{"Confirm_Token":"cf_x"}`,
		"not an object":      `["message"]`,
		"wrong type":         `{"message":3}`,
		"trailing data":      `{"message":"hi"} {}`,
	} {
		got = fakeIn{}
		before := len(em.Events())
		r := jc.InvokeJSON(ctx, inv, nil, json.RawMessage(input), "", o)
		if r.Err == nil || r.Outcome.Kind != command.OutcomeFailed || got.Message != "" || len(em.Events()) != before {
			t.Errorf("%s: accepted: %+v", name, r)
		}
		if name == "confirm token" && !errors.Is(r.Err, command.ErrConfirmTokenInput) {
			t.Errorf("%s: %v", name, r.Err)
		}
	}

	// resume is the only way to pass a token, and it lands in the Spec's
	// token field.
	r = jc.InvokeJSON(ctx, inv, nil, nil, approvalID, o)
	if r.Err != nil || got.ConfirmToken != approvalID || r.OperationID != approvalID {
		t.Fatalf("resume: %+v got=%+v", r, got)
	}
	read := command.Spec[fakeIn, fakeOut]{Name: "jsonread", Policy: command.Policy{Class: policy.Read},
		Run: func(context.Context, *app.App, fakeIn) (any, error) { return fakeOut{}, nil }}
	if r := read.InvokeJSON(ctx, inv, nil, nil, approvalID, o); !errors.Is(r.Err, command.ErrNoResume) {
		t.Fatalf("resume of a command without a token: %+v", r)
	}
}
