package command_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
	"github.com/ryanhelms/agentflow/internal/goldentest"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/provider/native"
)

// trace is what one scenario observably did: output, event types, which
// events share a correlation ID, and audit lines.
type trace struct {
	out, err string
	types    []bus.Type
	groups   []int // index of the first event with the same CorrelationID
	audits   int
}

func record(t *testing.T, em *commandtest.Emitter, au *commandtest.Auditor, out string, err error) trace {
	t.Helper()
	tr := trace{out: out, types: em.Types(), audits: len(au.Lines())}
	if err != nil {
		tr.err = err.Error()
	}
	evs := em.Events()
	for _, ev := range evs {
		tr.groups = append(tr.groups, slices.IndexFunc(evs, func(e bus.Event) bool { return e.CorrelationID == ev.CorrelationID }))
	}
	return tr
}

// TestNewInvokerMatchesReference runs the contract's scenarios through the
// reference Invoker and NewInvoker: both must give the same output, events,
// correlation and audit lines.
func TestNewInvokerMatchesReference(t *testing.T) {
	native2 := func(audit bool) command.Spec[fakeIn, fakeOut] {
		return command.Spec[fakeIn, fakeOut]{
			Name: "transition", NativeReads: true,
			Policy: command.Policy{Class: policy.Remote, Action: "transition", AuditByInvoker: audit},
			Run: func(_ context.Context, _ *app.App, in fakeIn) (any, error) {
				if in.Message == "" {
					return nil, &native.Request{Status: "native_read_required", ID: "r1"}
				}
				return fakeOut{Status: "moved"}, nil
			},
		}
	}
	scenarios := map[string]func(inv command.Invoker) (string, error){
		"confirm flow": func(inv command.Invoker) (string, error) {
			var runs []string
			env := &commandtest.Env{Answer: true}
			err := fakeCommit(&runs).RunCLI(context.Background(), env, inv, []string{"-m", "feat: x"})
			return env.Out.String(), err
		},
		"declined": func(inv command.Invoker) (string, error) {
			var runs []string
			env := &commandtest.Env{}
			err := fakeCommit(&runs).RunCLI(context.Background(), env, inv, []string{"-m", "feat: x"})
			return env.Out.String(), err
		},
		"refusal": func(inv command.Invoker) (string, error) {
			var runs []string
			env := &commandtest.Env{}
			err := fakeCommit(&runs).RunCLI(context.Background(), env, inv, []string{"-m", "invalid", "--yes"})
			return env.Out.String(), err
		},
		"dry run": func(inv command.Invoker) (string, error) {
			var runs []string
			env := &commandtest.Env{}
			err := fakeCommit(&runs).RunCLI(context.Background(), env, inv, []string{"-m", "x", "--dry-run", "--confirm-token", approvalID})
			return env.Out.String(), err
		},
		"validation": func(inv command.Invoker) (string, error) {
			var runs []string
			env := &commandtest.Env{}
			err := fakeCommit(&runs).RunCLI(context.Background(), env, inv, nil)
			return env.Out.String(), err
		},
		"error": func(inv command.Invoker) (string, error) {
			s := command.Spec[fakeIn, fakeOut]{Name: "push", Policy: command.Policy{Class: policy.Remote, Action: "push", AuditByInvoker: true},
				Run: func(context.Context, *app.App, fakeIn) (any, error) { return nil, policy.ErrChanged }}
			_, err := command.Invoke(context.Background(), inv, nil, s, fakeIn{}, command.Origin{Source: "cli", Client: "cli"})
			return "", err
		},
		// A write that publishes no events is still audited.
		"scope none write": func(inv command.Invoker) (string, error) {
			s := command.Spec[fakeIn, fakeOut]{Name: "plugin.add", Events: command.ScopeNone,
				Policy: command.Policy{Class: policy.Local, Action: "plugin.install", AuditByInvoker: true},
				Run:    func(context.Context, *app.App, fakeIn) (any, error) { return fakeOut{Status: "added"}, nil }}
			_, err := command.Invoke(context.Background(), inv, nil, s, fakeIn{}, command.Origin{Source: "cli", Client: "cli"})
			return "", err
		},
		"native read": func(inv command.Invoker) (string, error) {
			origin := command.Origin{Source: "mcp:claude", Client: "claude"}
			_, err := command.Invoke(context.Background(), inv, nil, native2(true), fakeIn{}, origin)
			var nr *native.Request
			if !errors.As(err, &nr) {
				return "", errors.New("native request changed")
			}
			_, err = command.Invoke(context.Background(), inv, nil, native2(true), fakeIn{Message: "results"}, origin)
			return "", err
		},
	}
	for name, run := range scenarios {
		t.Run(name, func(t *testing.T) {
			var got [2]trace
			for i, mk := range []func(command.Options) command.Invoker{
				func(o command.Options) command.Invoker { return commandtest.Invoker{Options: o} },
				command.NewInvoker,
			} {
				em, au := &commandtest.Emitter{}, &commandtest.Auditor{}
				out, err := run(mk(command.Options{Emitters: em.Source(), Auditor: au}))
				got[i] = record(t, em, au, out, err)
			}
			if !slices.Equal(got[0].types, got[1].types) || !slices.Equal(got[0].groups, got[1].groups) ||
				got[0].out != got[1].out || got[0].err != got[1].err || got[0].audits != got[1].audits {
				t.Fatalf("reference %+v\nNewInvoker %+v", got[0], got[1])
			}
		})
	}
}

func TestNewInvokerStages(t *testing.T) {
	var runs []string
	em := &commandtest.Emitter{}
	var seen int
	pass := command.StageFunc(func(ctx context.Context, c *command.Call, next command.Handler) (any, error) {
		seen = len(em.Events()) // a stage runs before op.requested
		return next(ctx, c)
	})
	inv := command.NewInvoker(command.Options{Emitters: em.Source(), Stages: []command.Stage{pass}})
	if _, err := command.Invoke(context.Background(), inv, nil, fakeCommit(&runs), fakeIn{Message: "m", ConfirmToken: approvalID}, command.Origin{Source: "cli"}); err != nil {
		t.Fatal(err)
	}
	if seen != 0 || !slices.Equal(em.Types(), []bus.Type{bus.TypeOpRequested, bus.TypeOpApplied}) {
		t.Fatalf("stage saw %d events; events %v", seen, em.Types())
	}

	// A stage that refuses stops the chain: nothing runs and nothing is
	// published, like a validation error.
	em = &commandtest.Emitter{}
	refused := errors.New("not allowed")
	stop := command.StageFunc(func(context.Context, *command.Call, command.Handler) (any, error) { return nil, refused })
	inv = command.NewInvoker(command.Options{Emitters: em.Source(), Stages: []command.Stage{stop}})
	runs = nil
	if _, err := command.Invoke(context.Background(), inv, nil, fakeCommit(&runs), fakeIn{Message: "m"}, command.Origin{Source: "cli"}); !errors.Is(err, refused) {
		t.Fatalf("err %v", err)
	}
	if len(runs) != 0 || len(em.Events()) != 0 {
		t.Fatalf("refused call ran %v, published %v", runs, em.Types())
	}
}

// TestNewInvokerGatesByInvoker gates a GateByInvoker operation through
// (*app.App).GateOperation with a real App: confirm autonomy asks first and
// runs only with the token, and the Invoker audits each call once through
// policy.Gate.AuditOperation.
func TestNewInvokerGatesByInvoker(t *testing.T) {
	goldentest.Isolate(t)
	home := t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.yml"), []byte("autonomy: {local: confirm, remote: confirm}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	a, err := app.Open(context.Background(), dir, "cli")
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	s := command.Spec[fakeIn, fakeOut]{
		Name: "widget.make", Policy: command.Policy{Class: policy.Local, Action: "commit", Gate: command.GateByInvoker, AuditByInvoker: true},
		Token: func(in *fakeIn) *string { return &in.ConfirmToken },
		Plan: func(_ context.Context, _ *app.App, in fakeIn) (command.Plan, error) {
			return command.Plan{Summary: map[string]string{"message": in.Message}}, nil
		},
		Run: func(context.Context, *app.App, fakeIn) (any, error) {
			ran++
			return fakeOut{Status: "made", SHA: "abc"}, nil
		},
	}
	em := &commandtest.Emitter{}
	inv := command.NewInvoker(command.Options{Emitters: em.Source(), Auditor: command.AuditLog{}})
	origin := command.Origin{Source: "cli", Client: "cli"}
	v, err := command.Invoke(context.Background(), inv, a, s, fakeIn{Message: "m"}, origin)
	p, ok := v.(*app.Pending)
	if err != nil || !ok || p.Status != "needs_confirmation" || ran != 0 {
		t.Fatalf("first call: %#v, %v, ran %d", v, err, ran)
	}
	v, err = command.Invoke(context.Background(), inv, a, s, fakeIn{Message: "m", ConfirmToken: p.ConfirmToken}, origin)
	if err != nil || ran != 1 {
		t.Fatalf("approved call: %#v, %v, ran %d", v, err, ran)
	}
	if out, ok := v.(fakeOut); !ok || out.SHA != "abc" {
		t.Fatalf("result changed: %#v", v)
	}
	want := []bus.Type{bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpApplied}
	if !slices.Equal(em.Types(), want) {
		t.Fatalf("events %v", em.Types())
	}
	raw, err := os.ReadFile(filepath.Join(a.Home, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"result":"awaiting_approval"`) || !strings.Contains(lines[1], `"result":"applied"`) ||
		!strings.Contains(lines[1], `"operation":"widget.make"`) || !strings.Contains(lines[1], `"operation_id":"`+p.Approval.OperationID+`"`) {
		t.Fatalf("audit lines:\n%s", raw)
	}
	if strings.Contains(string(raw), p.ConfirmToken) {
		t.Fatal("audit line carries the token")
	}
}
