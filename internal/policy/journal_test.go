package policy

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
)

func TestListJournalIsReadOnlyAndPublic(t *testing.T) {
	home := t.TempDir()
	a := gateFor(t, home, "claude-code")
	a.Configure("repo-a", nil, nil)
	b := gateFor(t, home, "codex")
	b.Configure("repo-b", nil, nil)

	first, err := a.Check(Remote, "pr.open", "first", "", false)
	if err != nil || first.Approval == nil {
		t.Fatal(first, err)
	}
	approvedDecision, err := a.Check(Remote, "pr.open", "second", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Approve(approvedDecision.Approval.OperationID, approvedDecision.Token); err != nil {
		t.Fatal(err)
	}
	other, err := b.Check(Local, "commit", "elsewhere", "", false)
	if err != nil {
		t.Fatal(err)
	}

	dir := ApprovalsDir(home)
	before := directoryListing(t, dir)
	got, err := ListJournal(dir, ScopeKey("repo-a"), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].OperationID != first.Approval.OperationID || got[1].OperationID != approvedDecision.Approval.OperationID {
		t.Fatalf("records of repo-a = %+v", got)
	}
	if got[0].State != pending || got[1].State != approved {
		t.Fatalf("states = %q, %q", got[0].State, got[1].State)
	}
	if got[0].Action != "pr.open" || got[0].Class != Remote || got[0].IssuedAt.IsZero() || got[0].ExpiresAt.IsZero() || got[0].Source == "" {
		t.Fatalf("record = %+v", got[0])
	}
	if !strings.HasPrefix(got[0].PromptOwner, "claude-code/") {
		t.Fatalf("prompt owner = %q", got[0].PromptOwner)
	}
	// The public view never carries what the record keeps secret.
	raw, err := os.ReadFile(filepath.Join(dir, ScopeKey("repo-a")+"-"+first.Approval.OperationID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var full struct{ Fingerprint string }
	if err := json.Unmarshal(raw, &full); err != nil || full.Fingerprint == "" {
		t.Fatalf("record has no fingerprint: %v", err)
	}
	view, err := json.Marshal(got[0])
	if err != nil || strings.Contains(string(view), full.Fingerprint) || strings.Contains(string(view), "fingerprint") || strings.Contains(string(view), "related") {
		t.Fatalf("view leaks: %s %v", view, err)
	}
	if after := directoryListing(t, dir); after != before {
		t.Fatalf("listing changed the journal:\nbefore %s\nafter  %s", before, after)
	}

	if rest, err := ListJournal(dir, ScopeKey("repo-b"), time.Time{}); err != nil || len(rest) != 1 || rest[0].OperationID != other.Approval.OperationID {
		t.Fatalf("records of repo-b = %+v, %v", rest, err)
	}
	if none, err := ListJournal(dir, ScopeKey("repo-a"), time.Now().Add(time.Hour)); err != nil || len(none) != 0 {
		t.Fatalf("since filter: %+v, %v", none, err)
	}
	if none, err := ListJournal(filepath.Join(home, "missing"), ScopeKey("repo-a"), time.Time{}); err != nil || none != nil {
		t.Fatalf("missing directory: %+v, %v", none, err)
	}
}

func TestListJournalSkipsPreparedAndBrokenRecords(t *testing.T) {
	home := t.TempDir()
	g, err := NewGate(home, config.Autonomy{Local: config.Auto, Remote: config.Auto}, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	g.Configure("repo", nil, nil)
	prepared, err := g.CheckActions(Remote, "pr.open", nil, "payload", "", false, true)
	if err != nil || prepared.Approval == nil {
		t.Fatal(prepared, err)
	}
	dir := ApprovalsDir(home)
	scope := ScopeKey("repo")
	if err := os.WriteFile(filepath.Join(dir, scope+"-op_00000000000000000000000000000001.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "request-abc"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ListJournal(dir, scope, time.Time{})
	if err != nil || len(got) != 0 {
		t.Fatalf("records = %+v, %v", got, err)
	}
}

func TestReadJournalRecordRefusesUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	const scope, id = "0123456789abcdef0123456789abcdef", "op_0123456789abcdef0123456789abcdef"
	name := filepath.Join(dir, scope+"-"+id+".json")
	good := `{"operation_id":"` + id + `","state":"pending","action":"commit","class":"local","issued_at":"2026-11-26T12:00:00Z","expires_at":"2026-11-26T12:05:00Z"}`

	if _, err := ReadJournalRecord(name); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	if err := os.WriteFile(name, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := ReadJournalRecord(name); err != nil || r.OperationID != id || r.State != pending {
		t.Fatalf("good record: %+v, %v", r, err)
	}
	other := filepath.Join(dir, scope+"-op_ffffffffffffffffffffffffffffffff.json")
	if err := os.WriteFile(other, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadJournalRecord(other); err == nil {
		t.Fatal("record accepted under another operation ID")
	}
	if _, err := ReadJournalRecord(filepath.Join(dir, "events.jsonl")); err == nil {
		t.Fatal("record accepted under a foreign name")
	}
	big := filepath.Join(dir, scope+"-op_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee.json")
	if err := os.WriteFile(big, []byte(strings.Repeat(" ", maxJournalRecord+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadJournalRecord(big); err == nil {
		t.Fatal("oversized record accepted")
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(dir, scope+"-op_dddddddddddddddddddddddddddddddd.json")
		if err := os.Symlink(name, link); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadJournalRecord(link); err == nil {
			t.Fatal("symbolic link accepted")
		}
	}
}

func directoryListing(t *testing.T, dir string) string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, e := range list {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		sb.WriteString(e.Name() + ":" + info.ModTime().String() + ";")
	}
	return sb.String()
}
