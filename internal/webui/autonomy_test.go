package webui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/consolecfg"
)

func TestPersonalAutonomySave(t *testing.T) {
	s := testServer(t)
	path := filepath.Join(os.Getenv("DEVFLOW_HOME"), "config.yml")
	initial := []byte("# keep this comment\ncustom_field: private-user-data\n")
	if err := os.WriteFile(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	if w := request(t, s, "GET", "/api/autonomy", "", "", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request(t, s, "GET", "/api/autonomy", "", "session", nil)
	var v consolecfg.ApprovalView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || len(v.Actions) == 0 || bytes.Contains(w.Body.Bytes(), []byte("private-user-data")) {
		t.Fatal(w.Code, w.Body.String())
	}
	in := autonomyEdit{Scope: "personal", UserRevision: v.UserRevision, Settings: &consolecfg.ApprovalSettings{Actions: map[string]config.Level{"console.reply": config.Suggest, "commit": config.Auto}, Window: "15m"}}
	w = request(t, s, "POST", "/api/autonomy", s.origin, "session", in)
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("private-user-data")) {
		t.Fatal(w.Code, w.Body.String())
	}
	saved, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(saved, initial) || !bytes.Contains(saved, []byte("console.reply: suggest")) {
		t.Fatalf("%s, %v", saved, err)
	}
	user, err := config.LoadUser()
	if err != nil || user.Autonomy.Actions["commit"] != config.Auto || user.ApprovalWindow.Minutes() != 15 {
		t.Fatal(user, err)
	}
	if w = request(t, s, "POST", "/api/autonomy", s.origin, "session", in); w.Code != 409 {
		t.Fatalf("stale: %s", w.Body.String())
	}
	in.UserRevision, in.Settings.Window = hash(saved), "2h"
	if w = request(t, s, "POST", "/api/autonomy", s.origin, "session", in); w.Code != 422 {
		t.Fatalf("invalid: %s", w.Body.String())
	}
}

func TestRepositoryAutonomyStagesAgainstReviewedRevision(t *testing.T) {
	s := testServer(t)
	path := filepath.Join(s.repo.Dir, approvalConfigFile)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	in := autonomyEdit{Scope: "repository", Base: hash(src), Content: string(src) + "# existing staged change\n", Settings: &consolecfg.ApprovalSettings{Actions: map[string]config.Level{"push": config.Confirm}, Window: "10m"}}
	w := request(t, s, "POST", "/api/autonomy", s.origin, "session", in)
	var result struct {
		Content  string
		Autonomy consolecfg.ApprovalView
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !strings.Contains(result.Content, "existing staged change") || result.Autonomy.Repository.Actions["push"] != config.Confirm {
		t.Fatal(w.Code, w.Body.String())
	}
	onDisk, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(onDisk, src) {
		t.Fatal("repository changed during staging", err)
	}
	in.Base = "stale"
	if w = request(t, s, "POST", "/api/autonomy", s.origin, "session", in); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}
