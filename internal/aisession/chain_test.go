package aisession

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/config"
)

// chainRig is a worktree, a gate in a temporary folder and a program in a
// temporary folder, for the chain tests. No test here starts a process.
type chainRig struct {
	wt, home, bin string
}

func newChainRig(t *testing.T) chainRig {
	t.Helper()
	real := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	c := chainRig{wt: real(t.TempDir()), home: real(t.TempDir()), bin: real(t.TempDir())}
	for _, rel := range []string{"README.md", "src/a.go", ".env", ".claude/settings.json"} {
		p := filepath.Join(c.wt, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"cat", "make", "sh", "agentflow", "sudo"} {
		if err := os.WriteFile(filepath.Join(c.bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func (c chainRig) chain(t *testing.T, user *config.User, repo *config.Repo, ceiling string) []PermissionHandler {
	t.Helper()
	if user == nil {
		user = &config.User{}
	}
	if repo == nil {
		repo = &config.Repo{}
	}
	// As the host builds it.
	g, err := newAuditGate(t.TempDir(), user, repo, c.wt)
	if err != nil {
		t.Fatal(err)
	}
	return DefaultChain(ChainConfig{
		Session: "ais_00000000000000000000000000000000", Worktree: c.wt, Ceiling: ceiling,
		User: user, Repo: repo, Gate: g,
		// As the host sets them: the home, the store, ~/.agentflow-ui.
		Protect: []string{c.home, filepath.Join(c.home, "ai"), filepath.Join(filepath.Dir(c.home), ".agentflow-ui")},
		Expand:  map[string][]string{"AGENTFLOW_HOME": {c.home}, "HOME": {filepath.Dir(c.home)}},
	})
}

func (c chainRig) read(rel string) *Request {
	p, _ := mustWorktree(c.wt).resolve(filepath.Join(c.wt, filepath.FromSlash(rel)))
	return &Request{Kind: KindRead, Paths: []Path{p}}
}

func (c chainRig) write(rel string) *Request {
	r := c.read(rel)
	r.Kind = KindWrite
	return r
}

func (c chainRig) term(program string, args ...string) *Request {
	cwd := Path{Abs: c.wt, Real: c.wt, Exists: true}
	return &Request{Kind: KindTerminal, Command: &Command{
		Program: program, Resolved: filepath.Join(c.bin, program), Args: args, Cwd: cwd, Line: quoteLine(program, args),
	}}
}

func mustWorktree(root string) worktree {
	w, err := newWorktree(root)
	if err != nil {
		panic(err)
	}
	return w
}

func userWith(rules map[string]config.AIRule, a config.Autonomy) *config.User {
	u := &config.User{Autonomy: a}
	u.AI.Permissions.Rules = rules
	return u
}

// forward is a link that always passes the request on.
type forward struct{}

func (forward) Decide(ctx context.Context, r *Request, next Next) Verdict { return next(ctx, r) }

// says is a link that answers v.
type says struct{ v Verdict }

func (s says) Decide(context.Context, *Request, Next) Verdict { return s.v }

func TestChainDeniesByDefault(t *testing.T) {
	ctx := context.Background()
	c := newChainRig(t)
	r := c.read("README.md")
	want := Verdict{Decision: Deny, Handler: HandlerDefault, Code: VerdictDefault}

	// No link, or links that only pass it on: deny.
	for name, chain := range map[string][]PermissionHandler{
		"empty":   nil,
		"forward": {forward{}, forward{}},
	} {
		if v := Run(ctx, chain, r); v != want {
			t.Errorf("%s chain = %+v", name, v)
		}
	}
	// Only the rule and gate links may allow; the never-list may also ask.
	for _, v := range []Verdict{
		{Decision: Allow, Handler: HandlerNever},
		{Decision: Allow, Handler: HandlerBoundary},
		{Decision: Allow, Handler: HandlerDefault},
		{Decision: Allow, Handler: HandlerPerson},
		{Decision: Ask, Handler: HandlerBoundary},
		{Decision: "maybe", Handler: HandlerGate},
		{},
	} {
		if got := Run(ctx, []PermissionHandler{says{v}}, r); got != want {
			t.Errorf("a link answering %+v gives %+v", v, got)
		}
	}
	// The default chain with no gate: a person's allow rule alone allows
	// nothing.
	u := userWith(map[string]config.AIRule{"all": {Kind: config.AIRuleKindFSRead, Match: "**", Decision: config.AIDecisionAllow}}, config.Autonomy{})
	noGate := DefaultChain(ChainConfig{Worktree: c.wt, User: u})
	if v := Run(ctx, noGate, r); v != want {
		t.Errorf("no gate = %+v", v)
	}
	// An unknown request kind is denied by the never-list.
	if v := Run(ctx, c.chain(t, nil, nil, ""), &Request{Kind: "fs_delete"}); v.Decision != Deny || v.Handler != HandlerNever {
		t.Errorf("unknown kind = %+v", v)
	}
	// An earlier request_permission authorizes nothing: the same chain
	// decides the write that follows on its own.
	chain := c.chain(t, nil, nil, "")
	perm := &Request{Kind: KindPermission, ToolKind: acp.ToolEdit, Paths: c.write("src/a.go").Paths,
		Options: []acp.PermissionOption{{OptionID: "a", Kind: acp.AllowOnce}}}
	_ = Run(ctx, chain, perm)
	if v := Run(ctx, chain, c.write("src/a.go")); v.Decision == Allow {
		t.Errorf("a write after a permission request = %+v", v)
	}
}

func TestChainPerRequestKind(t *testing.T) {
	ctx := context.Background()
	c := newChainRig(t)
	allowSrc := map[string]config.AIRule{"src": {Kind: config.AIRuleKindFSWrite, Match: "src/**", Decision: config.AIDecisionAllow}}
	allowAll := map[string]config.AIRule{
		"all-write": {Kind: config.AIRuleKindFSWrite, Match: "**", Decision: config.AIDecisionAllow},
		"all-read":  {Kind: config.AIRuleKindFSRead, Match: "**", Decision: config.AIDecisionAllow},
	}
	allowCat := map[string]config.AIRule{"cat": {Kind: config.AIRuleKindTerminal, Match: "cat", Decision: config.AIDecisionAllow}}
	allowSh := map[string]config.AIRule{"sh": {Kind: config.AIRuleKindTerminal, Match: "sh", Decision: config.AIDecisionAllow}}
	autoAll := config.Autonomy{Local: config.Auto, Remote: config.Auto, Actions: map[string]config.Level{
		ActionTerminal: config.Auto, ActionFSWrite: config.Auto, ActionPermission: config.Auto,
	}}
	repoDeny := &config.Repo{}
	repoDeny.AI.Permissions.Deny = []string{"src/**"}
	repoAsk := &config.Repo{}
	repoAsk.AI.Permissions.Ask = []string{"README.md"}

	tests := []struct {
		name    string
		user    *config.User
		repo    *config.Repo
		ceiling string
		r       *Request
		want    Decision
		handler string
		code    string
	}{
		{name: "read in the worktree", r: c.read("README.md"), want: Allow, handler: HandlerGate},
		{name: "read a secret file asks whatever a rule says", user: userWith(allowAll, autoAll), r: c.read(".env"), want: Ask, handler: HandlerNever, code: ReasonSecretFile},
		{name: "person rule allows a write", user: userWith(allowSrc, config.Autonomy{}), r: c.write("src/a.go"), want: Allow, handler: HandlerRule, code: "rule:src"},
		{name: "the ceiling overrides the rule", user: userWith(allowSrc, config.Autonomy{}), ceiling: CeilingConfirm, r: c.write("src/a.go"), want: Ask, handler: HandlerGate, code: ReasonCeiling},
		{name: "the ceiling leaves reads to policy", ceiling: CeilingConfirm, r: c.read("README.md"), want: Allow, handler: HandlerGate},
		{name: "a control-file write asks whatever a rule says", user: userWith(allowAll, autoAll), r: c.write(".claude/settings.json"), want: Ask, handler: HandlerNever, code: ReasonControlFile},
		{name: "a control-file write in any case", user: userWith(allowAll, autoAll), r: c.write(".CLAUDE/x.json"), want: Ask, handler: HandlerNever, code: ReasonControlFile},
		{name: "a repository deny beats a person's allow", user: userWith(allowSrc, config.Autonomy{}), repo: repoDeny, r: c.write("src/a.go"), want: Deny, handler: HandlerRule, code: "rule:repo"},
		{name: "a repository ask beats the gate", repo: repoAsk, r: c.read("README.md"), want: Ask, handler: HandlerRule, code: ReasonRule},
		{name: "a mandatory terminal asks at auto", user: userWith(nil, autoAll), r: c.term("make", "test"), want: Ask, handler: HandlerGate, code: ReasonTerminal},
		{name: "a person rule allows the program", user: userWith(allowCat, config.Autonomy{}), r: c.term("cat", "README.md"), want: Allow, handler: HandlerRule, code: "rule:cat"},
		{name: "a shell asks whatever a rule says", user: userWith(allowSh, autoAll), r: c.term("sh", "-c", "make"), want: Ask, handler: HandlerNever, code: ReasonShell},
		{name: "a permission of an unknown kind asks", user: userWith(nil, autoAll), r: &Request{Kind: KindPermission, ToolKind: acp.ToolOther}, want: Ask, handler: HandlerNever, code: ReasonKind},
		{name: "an edit permission without locations asks", user: userWith(nil, autoAll), r: &Request{Kind: KindPermission, ToolKind: acp.ToolEdit}, want: Ask, handler: HandlerGate, code: ReasonNoLocations},
		{name: "an execute permission asks", user: userWith(nil, autoAll), r: &Request{Kind: KindPermission, ToolKind: acp.ToolExecute}, want: Ask, handler: HandlerNever, code: ReasonTerminal},
		{name: "a gate at suggest denies a write the rule allows", user: userWith(allowAll, config.Autonomy{Actions: map[string]config.Level{ActionFSWrite: config.Suggest}}), r: c.write("src/a.go"), want: Deny, handler: HandlerGate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Run(ctx, c.chain(t, tt.user, tt.repo, tt.ceiling), tt.r)
			if v.Decision != tt.want || v.Handler != tt.handler || tt.code != "" && v.Code != tt.code {
				t.Errorf("verdict = %+v, want %s by %s (%s)", v, tt.want, tt.handler, tt.code)
			}
		})
	}
}

func TestChainNeverListsTerminals(t *testing.T) {
	ctx := context.Background()
	c := newChainRig(t)
	key := filepath.Join(c.home, "ai", "sessions", "ais_1", "answer.key")
	// A person's rule allows every program here: the never-list still wins.
	rules := map[string]config.AIRule{"any": {Kind: config.AIRuleKindTerminal, Match: "*", Decision: config.AIDecisionAllow}}
	u := userWith(rules, config.Autonomy{})
	homeName := filepath.Base(c.home)

	tests := []struct {
		name string
		r    *Request
		env  []acp.EnvVariable
		code string
	}{
		{name: "cat answer.key", r: c.term("cat", key), code: VerdictAgentflowHome},
		{name: "$AGENTFLOW_HOME spelling", r: c.term("cat", "$AGENTFLOW_HOME/ai/sessions/ais_1/answer.key"), code: VerdictAgentflowHome},
		{name: "${AGENTFLOW_HOME} spelling", r: c.term("cat", "${AGENTFLOW_HOME}/config.yml"), code: VerdictAgentflowHome},
		{name: "~ spelling", r: c.term("cat", "~/"+homeName+"/config.yml"), code: VerdictAgentflowHome},
		{name: "$HOME spelling", r: c.term("cat", "$HOME/"+homeName+"/config.yml"), code: VerdictAgentflowHome},
		{name: "the console registry", r: c.term("cat", "~/.agentflow-ui/registry.json"), code: VerdictAgentflowHome},
		{name: "a flag value", r: c.term("cat", "--file="+key), code: VerdictAgentflowHome},
		{name: "relative to the cwd", r: func() *Request {
			r := c.term("cat", "ai/sessions/ais_1/answer.key")
			r.Command.Cwd = Path{Abs: c.home, Real: c.home, Exists: true}
			return r
		}(), code: VerdictAgentflowHome},
		{name: "an env value", r: c.term("cat", "README.md"), env: []acp.EnvVariable{{Name: "F", Value: key}}, code: VerdictAgentflowHome},
		{name: "agentflow ai run", r: c.term("agentflow", "ai", "run", "--harness", "demo", "x"), code: VerdictAgentflowInSession},
		{name: "agentflow ai respond --allow", r: c.term("agentflow", "ai", "respond", "ais_1", "ask_1", "--allow"), code: VerdictAgentflowInSession},
		{name: "agentflow inside a shell line", r: c.term("sh", "-c", "agentflow ai run x"), code: VerdictAgentflowInSession},
		{name: "a privileged program", r: c.term("sudo", "make"), code: VerdictPrivilege},
		{name: "a refused env name", r: c.term("cat", "README.md"), env: []acp.EnvVariable{{Name: "LD_PRELOAD", Value: "/x.so"}}, code: VerdictEnv},
		{name: "an unresolved program", r: func() *Request {
			r := c.term("cat", "README.md")
			r.Command.Resolved = ""
			return r
		}(), code: VerdictProgram},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Run(withTerminalEnv(ctx, tt.env), c.chain(t, u, nil, ""), tt.r)
			if v.Decision != Deny || v.Handler != HandlerNever || v.Code != tt.code {
				t.Errorf("verdict = %+v, want deny by never (%s)", v, tt.code)
			}
		})
	}
	// A read-only agentflow command is not refused by the never-list.
	if v := Run(ctx, c.chain(t, u, nil, ""), c.term("agentflow", "version")); v.Decision != Allow {
		t.Errorf("agentflow version = %+v", v)
	}
	// The same cat outside the protected folders follows the rule.
	if v := Run(ctx, c.chain(t, u, nil, ""), c.term("cat", "README.md")); v.Decision != Allow || v.Code != "rule:any" {
		t.Errorf("cat README.md = %+v", v)
	}
}
