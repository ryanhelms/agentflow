package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/managedmcp"
	"github.com/nscaledev/devflow/internal/provision"
)

func TestRegisterReviewedCompanion(t *testing.T) {
	dir := t.TempDir()
	const file = ".cursor/mcp.json"
	s := managedmcp.Status{ID: "wiki", Kind: provision.RemoteMCP, Endpoint: "https://fixture.invalid/mcp", AuthEnv: "READ_TOKEN", Ready: true}
	r, _, err := companionOverlay(dir, "cursor", s)
	if err != nil || r.State != "pending" {
		t.Fatalf("initial registration: %+v %v", r, err)
	}
	const collision = `{"mcpServers":{"devflow-wiki":{"url":"https://elsewhere.invalid","headers":{"Authorization":"existing-secret"}}}}`
	setupFile(t, dir, file, collision, 0o600)
	if err := RegisterCompanion(dir, r, s); !errors.Is(err, provision.ErrStalePlan) {
		t.Fatalf("accepted changed entry: %v", err)
	}
	r, _, err = companionOverlay(dir, "cursor", s)
	if err != nil || r.PreviousSHA256 == "" || strings.Contains(r.Detail, "existing-secret") {
		t.Fatalf("invalid replacement: %+v %v", r, err)
	}
	if err := RegisterCompanion(dir, r, s); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil || strings.Contains(string(data), "existing-secret") || !strings.Contains(string(data), "Bearer ${env:READ_TOKEN}") {
		t.Fatal("invalid hosted cursor configuration")
	}
	s.Kind, s.Command, s.Endpoint = provision.StdioMCP, `C:\Users\fixture\native MCP\catalog.exe`, ""
	s.Args = []string{"stdio"}
	r, _, err = companionOverlay(dir, "cursor", s)
	if err != nil || r.State != "pending" {
		t.Fatal(err)
	}
	if err := RegisterCompanion(dir, r, s); err != nil {
		t.Fatal(err)
	}
	r, data, err = companionOverlay(dir, "cursor", s)
	if err != nil || r.State != "configured" || data != nil {
		t.Fatalf("native path did not round trip: %+v %v", r, err)
	}
}

func TestCompanionProjectClientFormats(t *testing.T) {
	for client, shape := range map[string]struct{ path, urlKey, ref string }{
		"claude": {".mcp.json", "url", "${MCP_TOKEN}"},
		"gemini": {".gemini/settings.json", "httpUrl", "${MCP_TOKEN}"},
		"cursor": {".cursor/mcp.json", "url", "${env:MCP_TOKEN}"},
		"vscode": {".vscode/mcp.json", "url", "${env:MCP_TOKEN}"},
	} {
		t.Run(client, func(t *testing.T) {
			root := t.TempDir()
			s := managedmcp.Status{ID: "catalog", Kind: provision.RemoteMCP, Endpoint: "https://fixture.invalid/mcp", AuthEnv: "MCP_TOKEN", Ready: true}
			r, _, err := companionOverlay(root, client, s)
			if err != nil || r.State != "pending" {
				t.Fatalf("plan %+v %v", r, err)
			}
			if err = RegisterCompanion(root, r, s); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(root, shape.path))
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range []string{shape.urlKey, shape.ref, "devflow-catalog", s.Endpoint} {
				if !strings.Contains(string(data), part) {
					t.Errorf("missing %s", part)
				}
			}
		})
	}
}
