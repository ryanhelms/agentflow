package delegate

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/boundary"
)

var (
	home   = boundary.Repo{CommonDir: "/h/agent-commander/.git", Dir: "/h/agent-commander", Slug: "ryanhelms/agent-commander"}
	target = boundary.Repo{CommonDir: "/h/agentflow/.git", Dir: "/h/agentflow", Slug: "ryanhelms/agentflow"}
)

func resolve(dir string) (boundary.Repo, bool) {
	switch {
	case strings.HasPrefix(dir, "/h/agentflow"), strings.HasPrefix(dir, "/w/agentflow/"):
		return target, true
	case strings.HasPrefix(dir, "/h/agent-commander"):
		return home, true
	}
	return boundary.Repo{}, false
}

// fakeTmux answers list-panes with panes and records every other call.
func fakeTmux(t *testing.T, panes string) *[]string {
	t.Helper()
	var calls []string
	oldRun, oldLook := Run, LookPath
	Run = func(_ context.Context, name string, args ...string) (string, error) {
		if name != "tmux" {
			return "", errors.New("unexpected " + name)
		}
		if args[0] == "list-panes" {
			if panes == "" {
				return "", errors.New("no server running")
			}
			return panes, nil
		}
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "new-session" {
			return "%42\n", nil
		}
		return "", nil
	}
	LookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	t.Setenv("TMUX_PANE", "%0")
	t.Cleanup(func() { Run, LookPath = oldRun, oldLook })
	return &calls
}

func TestSendsToRunningSession(t *testing.T) {
	calls := fakeTmux(t, strings.Join([]string{
		"%0\tme\t/h/agentflow\tclaude\t",                               // the caller's own pane
		"%1\tshell\t/h/agentflow\tzsh\t",                               // not an agent
		"%2\twork\t/w/agentflow/issue-3\tnode\t",                       // an agent in a worktree
		"%3\tother\t/h/agentflow\tclaude\t/h/agent-acp/.git",           // agentflow-managed for another repo
		"%4\tmanaged\t/w/agentflow/issue-4\tclaude\t/h/agentflow/.git", // agentflow-managed here
		"%5\tcmdr\t/h/agent-commander\tclaude\t",                       // the home repository
	}, "\n"))
	p, err := Build(t.Context(), home, target, "/h/agentflow", Request{Repo: "agentflow", Task: "merge PR 7\nonce green"}, true, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusSend || p.Pane.ID != "%4" || !p.Pane.Managed {
		t.Fatalf("plan %+v pane %+v", p, p.Pane)
	}
	if p.Message != "[agentflow delegate from ryanhelms/agent-commander] merge PR 7 once green" {
		t.Fatalf("message %q", p.Message)
	}
	if len(*calls) != 0 {
		t.Fatalf("Build ran %v", *calls)
	}
	res, err := Apply(t.Context(), p)
	if err != nil || res.Status != StatusSent {
		t.Fatalf("apply %+v %v", res, err)
	}
	want := []string{"send-keys -t %4 -l " + p.Message, "send-keys -t %4 Enter"}
	if !slices.Equal(*calls, want) {
		t.Fatalf("calls %v want %v", *calls, want)
	}
}

func TestUnmanagedAgentPaneIsUsed(t *testing.T) {
	fakeTmux(t, "%2\twork\t/w/agentflow/issue-3\tcodex\t\n")
	p, err := Build(t.Context(), home, target, "/h/agentflow", Request{Task: "x"}, false, resolve)
	if err != nil || p.Status != StatusSend || p.Pane.ID != "%2" || p.Pane.Managed {
		t.Fatalf("plan %+v %v", p, err)
	}
}

func TestNativeHandOffWithoutPane(t *testing.T) {
	calls := fakeTmux(t, "")
	p, err := Build(t.Context(), home, target, "/h/agentflow", Request{Task: "review PR 7"}, true, resolve)
	if err != nil || p.Status != StatusNative || !strings.Contains(p.Next, "/h/agentflow") {
		t.Fatalf("plan %+v %v", p, err)
	}
	if _, err := Apply(t.Context(), p); err != nil || len(*calls) != 0 {
		t.Fatalf("native plan ran %v %v", *calls, err)
	}
}

func TestStartsNewSession(t *testing.T) {
	for _, native := range []bool{false, true} {
		calls := fakeTmux(t, "")
		req := Request{Task: "fix it", Agent: "codex", Start: native}
		p, err := Build(t.Context(), home, target, "/h/agentflow", req, native, resolve)
		if err != nil || p.Status != StatusStart || p.Agent != "codex" || !strings.HasPrefix(p.Session, "agentflow-agentflow-") {
			t.Fatalf("plan %+v %v", p, err)
		}
		res, err := Apply(t.Context(), p)
		if err != nil || res.Status != StatusStarted || res.Pane.ID != "%42" {
			t.Fatalf("apply %+v %v", res, err)
		}
		if len(*calls) != 2 ||
			!strings.Contains((*calls)[0], "new-session -d -P -F #{pane_id} -s "+p.Session+" -c /h/agentflow -e "+boundary.SessionEnv+"=/h/agentflow/.git codex "+p.Message) ||
			(*calls)[1] != "set-option -p -t %42 "+PaneOption+" /h/agentflow/.git" {
			t.Fatalf("calls %v", *calls)
		}
	}
}

func TestStartNeedsTmux(t *testing.T) {
	fakeTmux(t, "")
	LookPath = func(string) (string, error) { return "", errors.New("not found") }
	if _, err := Build(t.Context(), home, target, "/h/agentflow", Request{Task: "x"}, false, resolve); err == nil || !strings.Contains(err.Error(), "/h/agentflow") {
		t.Fatalf("err %v", err)
	}
	if _, err := Build(t.Context(), home, target, "/h/agentflow", Request{Task: "  "}, false, resolve); err == nil {
		t.Fatal("empty task accepted")
	}
}

func TestDefaultAgentAndRepoName(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	if got := DefaultAgent("", "claude"); got != "claude" {
		t.Errorf("agent %s", got)
	}
	if got := DefaultAgent("codex", "claude"); got != "codex" {
		t.Errorf("agent %s", got)
	}
	for in, want := range map[string]string{"agentflow": "agentflow", "ryanhelms/agentflow": "agentflow", "github.com/o/r.git": "r"} {
		if got := RepoName(in); got != want {
			t.Errorf("RepoName(%q) = %q", in, got)
		}
	}
}
