package policy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nscaledev/devflow/internal/config"
)

func gateFor(t *testing.T, home, client string) *Gate {
	t.Helper()
	g, err := NewGate(home, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, client)
	if err != nil {
		t.Fatal(err)
	}
	g.Configure("repo", nil, nil)
	return g
}

func TestApprovalOwnerHandoffAndSnapshot(t *testing.T) {
	home := t.TempDir()
	a, b := gateFor(t, home, "owner"), gateFor(t, home, "worker")
	const payload = "confidential unpublished contents"
	first, err := a.Check(Local, "commit", payload, "", false)
	if err != nil || first.Outcome != NeedsConfirmation || first.Approval == nil {
		t.Fatal(first, err)
	}
	id := first.Approval.OperationID
	second, err := b.Check(Local, "commit", payload, "", false)
	if err != nil || second.Outcome != AwaitingApproval || second.Token != "" || second.Approval.OperationID != id || second.Approval.PromptOwner != first.Approval.PromptOwner {
		t.Fatal(second, err)
	}
	same, err := a.Check(Local, "commit", payload, "", false)
	if err != nil || same.Token != first.Token {
		t.Fatal(same, err)
	}
	if _, err = b.Approve(id, id); !errors.Is(err, ErrToken) {
		t.Fatal("public ID approved itself", err)
	}
	if _, err = b.Check(Local, "commit", payload, id, false); !errors.Is(err, ErrNotApproved) {
		t.Fatal(err)
	}
	other := gateFor(t, home, "other")
	other.Configure("unrelated", nil, nil)
	if _, err = other.Status(id); !errors.Is(err, ErrToken) {
		t.Fatal("cross-repo status", err)
	}
	if _, err = other.Approve(id, first.Token); !errors.Is(err, ErrToken) {
		t.Fatal("cross-repo approval", err)
	}
	if status, err := a.Approve(id, first.Token); err != nil || status.State != approved {
		t.Fatal(status, err)
	}
	if _, err = b.Check(Local, "commit", "changed", id, false); !errors.Is(err, ErrChanged) {
		t.Fatal(err)
	}
	if _, err = b.Check(Remote, "other", payload, id, false); !errors.Is(err, ErrChanged) {
		t.Fatal(err)
	}
	if out, err := b.Check(Local, "commit", payload, id, false); err != nil || out.Outcome != Proceed {
		t.Fatal(out, err)
	}
	restarted := gateFor(t, home, "worker")
	if _, err = restarted.Check(Local, "commit", payload, first.Token, false); !errors.Is(err, ErrConsumed) {
		t.Fatal(err)
	}
	if st, err := a.Status(id); err != nil || st.State != consumed {
		t.Fatal(st, err)
	}
	path := filepath.Join(home, "approvals", "events.jsonl")
	content, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(content), id) || strings.Contains(string(content), payload) || strings.Contains(string(content), first.Token) {
		t.Fatal("unexpected lifecycle audit", err)
	}
}

func TestApprovalWindowAndPrepare(t *testing.T) {
	home := t.TempDir()
	g := gateFor(t, home, "client")
	now := time.Now()
	g.now = func() time.Time { return now }
	window := 20 * time.Minute
	u := config.User{Autonomy: config.Autonomy{Remote: config.Confirm, Actions: map[string]config.Level{"pr_open_draft": config.Auto, "push": config.Confirm}}, ApprovalWindow: &window}
	g.Configure("repo", &u, nil)
	const payload = "pr head and remote"
	d, err := g.CheckActions(Remote, "pr_open_draft", []string{"push"}, payload, "", false, true)
	if err != nil || d.Outcome != NeedsConfirmation || d.Policy.Action != "push" || !d.Approval.ExpiresAt.Equal(now.Add(window)) {
		t.Fatal(d, err)
	}
	u.Autonomy.Actions["push"] = config.Auto
	now = now.Add(9 * time.Minute)
	window = time.Minute
	if _, err = g.CheckActions(Remote, "pr_open_draft", []string{"push"}, payload, d.Token, false, false); !errors.Is(err, ErrExpired) {
		t.Fatal("window cap", err)
	}
	fresh, err := g.CheckActions(Remote, "pr_open_draft", []string{"push"}, payload, "", false, true)
	if err != nil || fresh.Outcome != Prepared || fresh.Token == "" || fresh.Approval.OperationID == d.Approval.OperationID {
		t.Fatal(fresh, err)
	}
	if _, err = g.CheckActions(Remote, "pr_open_draft", []string{"push"}, payload, fresh.Approval.OperationID, false, false); !errors.Is(err, ErrNotApproved) {
		t.Fatal(err)
	}
	if out, err := g.CheckActions(Remote, "pr_open_draft", []string{"push"}, "changed", fresh.Token, false, false); !errors.Is(err, ErrChanged) {
		t.Fatal(out, err)
	}
	u.Autonomy.Actions["push"] = config.Confirm
	if _, err = g.CheckActions(Remote, "pr_open_draft", []string{"push"}, payload, fresh.Token, false, false); !errors.Is(err, ErrChanged) {
		t.Fatal("tightened auto", err)
	}
	approvedPreview, err := g.CheckActions(Remote, "pr_open_draft", []string{"push"}, payload, "", false, true)
	if err != nil || approvedPreview.Outcome != NeedsConfirmation || approvedPreview.Approval.OperationID == fresh.Approval.OperationID {
		t.Fatal(approvedPreview, err)
	}
	if err = g.Cancel(approvedPreview.Approval.OperationID, approvedPreview.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = g.CheckActions(Remote, "pr_open_draft", []string{"push"}, payload, approvedPreview.Token, false, false); !errors.Is(err, ErrCancelled) {
		t.Fatal(err)
	}
	g.PreviewOnly = true
	info, err := g.Check(Remote, "ticket_create", "draft", "", true)
	if err != nil || info.Token != "" || info.Approval != nil || info.Outcome != Prepared {
		t.Fatal(info, err)
	}
	g.PreviewOnly = false
	next, err := g.Check(Remote, "ticket_create", "draft", "", true)
	if err != nil || next.Outcome != NeedsConfirmation || next.Token == "" {
		t.Fatal(next, err)
	}
}

func TestApprovalAcrossProcesses(t *testing.T) {
	const helper = "DEVFLOW_POLICY_TEST_HELPER"
	if home := os.Getenv(helper); home != "" {
		g := gateFor(t, home, fmt.Sprintf("worker-%d", os.Getpid()))
		d, err := g.Check(Local, "commit", "staged-tree", os.Getenv("DEVFLOW_POLICY_TEST_RECEIPT"), false)
		fmt.Printf("POLICY_RESULT %s %s %s %s\n", d.Outcome, d.Token, func() string {
			if d.Approval != nil {
				return d.Approval.OperationID
			}
			return ""
		}(), ErrorCode(err))
		return
	}
	const workers = 5
	home := t.TempDir()
	run := func(receipt string) map[string]int {
		t.Helper()
		results := make(chan string, workers)
		for range workers {
			go func() {
				cmd := exec.Command(os.Args[0], "-test.run=^TestApprovalAcrossProcesses$")
				cmd.Env = append(os.Environ(), helper+"="+home, "DEVFLOW_POLICY_TEST_RECEIPT="+receipt)
				data, err := cmd.CombinedOutput()
				if err != nil {
					results <- "error: " + err.Error()
					return
				}
				for _, line := range strings.Split(string(data), "\n") {
					if strings.HasPrefix(line, "POLICY_RESULT ") {
						results <- strings.TrimPrefix(line, "POLICY_RESULT ")
						return
					}
				}
				results <- "missing child result"
			}()
		}
		counts := map[string]int{}
		for range workers {
			counts[<-results]++
		}
		return counts
	}
	counts := run("")
	var token, id string
	for key := range counts {
		parts := strings.Split(key, " ")
		if len(parts) == 4 && parts[0] == string(NeedsConfirmation) {
			token, id = parts[1], parts[2]
		}
	}
	if token == "" || counts[string(NeedsConfirmation)+" "+token+" "+id+" "] != 1 || counts[string(AwaitingApproval)+"  "+id+" "] != workers-1 {
		t.Fatal("expected exactly one prompt owner", counts)
	}
	g := gateFor(t, home, "coordinator")
	if _, err := g.Approve(id, token); err != nil {
		t.Fatal(err)
	}
	counts = run(id)
	var accepted, rejected int
	for key, n := range counts {
		parts := strings.Split(key, " ")
		if len(parts) != 4 {
			t.Fatal(key)
		}
		if parts[0] == string(Proceed) && parts[3] == "" {
			accepted += n
		} else if parts[3] == "approval_consumed" {
			rejected += n
		}
	}
	if accepted != 1 || rejected != workers-1 {
		t.Fatal("unexpected redemption counts", counts)
	}
	public, err := g.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(public)
	if strings.Contains(string(value), "cf_") {
		t.Fatal("status disclosed credential")
	}
}

func TestProviderBoundExpiry(t *testing.T) {
	g := gateFor(t, t.TempDir(), "agent")
	now := time.Now()
	g.now = func() time.Time { return now }
	lease := now.Add(10 * time.Second)
	d, err := g.CheckActionsUntil(Remote, "comment", nil, "bound", "", true, false, lease)
	if err != nil || d.Approval == nil || !d.Approval.ExpiresAt.Equal(lease) {
		t.Fatal(d, err)
	}
	now = lease
	if _, err := g.Check(Remote, "comment", "bound", d.Token, true); !errors.Is(err, ErrExpired) {
		t.Fatal("accepted expired native lease", err)
	}
}

func TestInterruptedForegroundPrompt(t *testing.T) {
	const variable = "DEVFLOW_TEST_FOREGROUND_PROMPT"
	if home := os.Getenv(variable); home != "" {
		g := gateFor(t, home, "foreground")
		d, err := g.Check(Local, "commit", "snapshot", "", false)
		if err != nil {
			t.Fatal(err)
		}
		release, err := g.OpenPrompt(d.Approval.OperationID, d.Token)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		fmt.Println(d.Approval.OperationID)
		<-time.After(time.Minute)
		return
	}
	home := t.TempDir()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestInterruptedForegroundPrompt$")
	cmd.Env = append(os.Environ(), variable+"="+home)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	id, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	id = strings.TrimSpace(id)
	worker := gateFor(t, home, "background")
	d, err := worker.Check(Local, "commit", "snapshot", "", false)
	if err != nil || d.Outcome != AwaitingApproval || d.Approval.OperationID != id {
		t.Fatal("live prompt was not held", err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if status, err := worker.Status(id); err != nil || status.State != cancelled {
		t.Fatal("killed CLI retained prompt", status, err)
	}
	d, err = worker.Check(Local, "commit", "snapshot", "", false)
	if err != nil || d.Outcome != NeedsConfirmation || d.Approval.OperationID == id {
		t.Fatal("new worker could not recover prompt", err)
	}
}
