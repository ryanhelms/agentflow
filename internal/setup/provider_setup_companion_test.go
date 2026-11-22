package setup

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	// A user-edited entry survives even a forced removal.
	edited := `{"mcpServers":{"devflow-wiki":{"command":"team","env":{"TOKEN":"private"}}}}`
	setupFile(t, dir, file, edited, 0o600)
	res, err := Remove(t.Context(), dir, RemoveOptions{Force: true})
	notes := strings.Join(res.Notes, " ")
	if err != nil || !strings.Contains(notes, "changed since registration") || strings.Contains(notes, "private") {
		t.Fatalf("expected a redacted manual cleanup notice: %+v, %v", res, err)
	}
	data, err = os.ReadFile(filepath.Join(dir, file))
	if err != nil || string(data) != edited {
		t.Fatal("forced removal changed the edited entry")
	}
}

func TestCompanionProjectClientFormats(t *testing.T) {
	for client, shape := range map[string]struct{ path, urlKey, ref string }{
		"claude":         {".mcp.json", "url", "${MCP_TOKEN}"},
		"claude-desktop": {".mcp.json", "url", "${MCP_TOKEN}"},
		"gemini":         {".gemini/settings.json", "httpUrl", "${MCP_TOKEN}"},
		"cursor":         {".cursor/mcp.json", "url", "${env:MCP_TOKEN}"},
		"vscode":         {".vscode/mcp.json", "url", "${env:MCP_TOKEN}"},
	} {
		t.Run(client, func(t *testing.T) {
			root := t.TempDir()
			if client == claudeDesktop {
				// Both registrations share a receipt. Remove must find both keys.
				if err := RegisterProviderClient(root, client); err != nil {
					t.Fatal(err)
				}
			}
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
			res, err := Remove(t.Context(), root, RemoveOptions{})
			if err != nil || !slices.Contains(res.Removed, shape.path) {
				t.Fatalf("remove did not report %s: %+v, %v", shape.path, res, err)
			}
			for _, name := range []string{shape.path, companionReceiptName(shape.path)} {
				if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Errorf("%s after remove: %v", name, err)
				}
			}
		})
	}
}

func TestRemoveLeavesMatchingUntrackedCompanion(t *testing.T) {
	root := t.TempDir()
	s := managedmcp.Status{ID: "wiki", Kind: provision.RemoteMCP, Endpoint: "https://fixture.invalid/mcp", Ready: true}
	r, data, err := companionOverlay(root, "cursor", s)
	if err != nil {
		t.Fatal(err)
	}
	// An earlier release (or a user) wrote exactly today's desired entry.
	setupFile(t, root, r.Path, string(data), 0o600)
	if err := RegisterCompanion(root, r, s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, companionReceiptName(r.Path))); !os.IsNotExist(err) {
		t.Fatalf("no-op registration claimed an existing entry: %v", err)
	}
	res, err := Remove(t.Context(), root, RemoveOptions{Force: true})
	if err != nil || !strings.Contains(strings.Join(res.Notes, " "), "untracked") {
		t.Fatalf("expected manual cleanup guidance: %+v, %v", res, err)
	}
	after, err := os.ReadFile(filepath.Join(root, r.Path))
	if err != nil || string(after) != string(data) {
		t.Fatalf("untracked config changed: %v", err)
	}
}

func TestCompanionReceiptConflictPreservesConfig(t *testing.T) {
	root := t.TempDir()
	s := managedmcp.Status{ID: "docs", Kind: provision.RemoteMCP, Endpoint: "https://fixture.invalid/mcp", Ready: true}
	const existing = `{"mcpServers":{"devflow-docs":{"command":"team"}}}`
	setupFile(t, root, ".mcp.json", existing, 0o600)
	r, _, err := companionOverlay(root, "claude", s)
	if err != nil {
		t.Fatal(err)
	}
	setupFile(t, root, companionReceiptName(r.Path), `{"team":"settings"}`, 0o600)
	if err := RegisterCompanion(root, r, s); err == nil {
		t.Fatal("overwrote a conflicting receipt")
	}
	data, err := os.ReadFile(filepath.Join(root, r.Path))
	if err != nil || string(data) != existing {
		t.Fatal("failed registration changed the existing config")
	}
}
