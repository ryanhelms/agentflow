package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
)

// ClientRegistration describes only the devflow-owned project entry. Host
// trust, plugin activation, and native vendor tools remain client operations.
type ClientRegistration struct {
	Client string `json:"client"`
	Path   string `json:"path"`
	State  string `json:"state"` // configured, pending, or blocked
	Detail string `json:"detail"`
}

func inspectRegistration(dir, client string) (*ClientRegistration, error) {
	r, _, err := clientOverlay(dir, client)
	return r, err
}

func clientOverlay(dir, client string) (*ClientRegistration, []byte, error) {
	file := map[string]string{
		"claude": "settings.json", "codex": "config.toml", "cursor": "mcp.json",
		"vscode": "mcp.json", "grok": "settings.json", "gemini": "settings.json",
	}[client]
	if file == "" {
		return nil, nil, fmt.Errorf("unknown client %q", client)
	}
	r := &ClientRegistration{Client: client, Path: filepath.Join("."+client, file), State: "blocked"}
	dest := filepath.Join(dir, r.Path)
	parent, err := os.Lstat(filepath.Dir(dest))
	if err == nil && !parent.IsDir() {
		r.Detail = "use an ordinary project directory for " + filepath.Dir(r.Path)
		return r, nil, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	info, err := os.Lstat(dest)
	var current []byte
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			r.Detail = "use a regular project file for " + r.Path
			return r, nil, nil
		}
		current, err = os.ReadFile(dest)
		if err != nil {
			return nil, nil, err
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, nil, err
	}
	var next []byte
	if client == "codex" {
		adapter := mustAdapter("codex/config.toml")
		_, table, _ := bytes.Cut(adapter, []byte("[mcp_servers.devflow]"))
		block := append([]byte("[mcp_servers.devflow]"), table...)
		switch {
		case bytes.Equal(bytes.TrimSpace(current), bytes.TrimSpace(adapter)), bytes.Equal(bytes.TrimSpace(current), bytes.TrimSpace(block)):
		case len(bytes.TrimSpace(current)) == 0:
			next = adapter
		default:
			r.Detail = "custom Codex TOML requires manual verification: compare the devflow table with adapters/codex/config.toml, trust the project and check devflow_setup_plan in Codex"
			return r, nil, nil
		}
	} else {
		var overlay []byte
		if client == "claude" {
			overlay = mustTemplate(".claude/settings.json")
		} else {
			overlay = mustAdapter(client + "/" + file)
		}
		var wanted map[string]any
		if err := json.Unmarshal(overlay, &wanted); err != nil {
			return nil, nil, err
		}
		// Install also manages these general preferences. Guided registration
		// only installs the devflow endpoint or Claude plugin declaration.
		delete(wanted, "includeCoAuthoredBy")
		delete(wanted, "contextFileName")
		cur := map[string]any{}
		if info != nil {
			decoder := json.NewDecoder(bytes.NewReader(current))
			decoder.UseNumber()
			if !json.Valid(current) || decoder.Decode(&cur) != nil || cur == nil {
				r.Detail = "make " + r.Path + " a valid JSON object before merging the devflow adapter"
				return r, nil, nil
			}
		}
		missing := false
		for _, key := range slices.Sorted(maps.Keys(wanted)) {
			entries := wanted[key].(map[string]any)
			if _, exists := cur[key]; !exists {
				missing = true
				continue
			}
			actual, ok := cur[key].(map[string]any)
			if !ok {
				r.Detail = "inspect the conflicting " + key + " entry in " + r.Path
				return r, nil, nil
			}
			for _, name := range slices.Sorted(maps.Keys(entries)) {
				val := entries[name]
				existing, exists := actual[name]
				if exists && !reflect.DeepEqual(existing, val) {
					r.Detail = "inspect the existing " + key + "." + name + " entry in " + r.Path + " before adopting the project adapter"
					if endpoint, ok := existing.(map[string]any); ok && endpoint["url"] != nil {
						r.Detail = "the existing remote MCP entry in " + r.Path + " requires a provider-declared endpoint, its authentication method and a read-only MCP handshake. This adapter verifies local stdio only"
					}
					return r, nil, nil
				}
				missing = missing || !exists
			}
		}
		if missing {
			deepMerge(cur, wanted)
			next, err = json.MarshalIndent(cur, "", "  ")
			if err != nil {
				return nil, nil, err
			}
			next = append(next, '\n')
		}
	}
	if next == nil {
		r.State, r.Detail = "configured", "project adapter present. Reload the client, trust the project and check that devflow_setup_plan appears"
	} else {
		r.State, r.Detail = "pending", "run devflow setup apply --client "+client+" to register the project adapter, then reload the client"
	}
	return r, next, nil
}

// RegisterProviderClient merges a single project adapter. A fresh provider
// plan must have passed before calling. Re-read the overlay under a writer
// lock so two setup processes cannot overwrite one another's changes.
func RegisterProviderClient(dir, client string) error {
	r, err := inspectRegistration(dir, client)
	if err != nil {
		return err
	}
	if r.State == "blocked" {
		return errors.New(r.Detail)
	}
	if r.State == "configured" {
		return nil
	}
	project, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer project.Close()
	parent := filepath.Dir(r.Path)
	if err := project.Mkdir(parent, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	clientDir, err := project.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer clientDir.Close()
	name := filepath.Base(r.Path)
	stage := name + ".devflow-tmp"
	f, err := clientDir.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("another registration is pending: retry or remove %s after confirming the previous setup has stopped", filepath.Join(parent, stage))
	}
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = clientDir.Remove(stage)
		}
	}()
	var next []byte
	r, next, err = clientOverlay(dir, client)
	if err == nil && r.State == "blocked" {
		err = errors.New(r.Detail)
	}
	if err == nil && next != nil {
		if info, statErr := clientDir.Stat(name); statErr == nil {
			err = f.Chmod(info.Mode().Perm())
		} else if !errors.Is(statErr, os.ErrNotExist) {
			err = statErr
		}
		if err == nil {
			_, err = f.Write(next)
		}
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil || next == nil {
		return err
	}
	err = clientDir.Rename(stage, name)
	published = err == nil
	return err
}

func (p *ProviderSetupPlan) checkLauncher(ctx context.Context, client string, hasRepo bool) {
	check := func(ok bool, detail string) { p.check("client", client, ok, detail) }
	if !hasRepo {
		check(false, "open the Git working tree where the client will run devflow")
		return
	}
	if runtime.GOOS == "windows" {
		check(false, "the bundled project adapter requires a POSIX launcher environment. Configure the client to run the verified Windows devflow.exe mcp --client "+client+" in this workspace")
		return
	}
	if _, err := exec.LookPath("sh"); err != nil {
		check(false, "make sh available on PATH in the client environment for the project adapter")
		return
	}
	if client != "claude" && !executable(filepath.Join(p.Root, ".devflow", "bin", "devflow")) {
		check(false, "provide the executable project launcher .devflow/bin/devflow generated by devflow init")
		return
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			check(false, "set HOME or XDG_CACHE_HOME for the client launcher")
			return
		}
		cache = filepath.Join(home, ".cache")
	}
	if !executable(filepath.Join(cache, "devflow", "current", "devflow")) {
		switch {
		case !probeCommand(ctx, p.Root, "gh", "attestation", "verify", "--help"):
			check(false, "first run requires gh on PATH with gh attestation verify support")
		case !probeCommand(ctx, p.Root, "gh", "auth", "status", "--hostname", "github.com"):
			check(false, "first run requires authenticated gh access to nscaledev/devflow on github.com. Run gh auth login --hostname github.com")
		default:
			check(false, "run the existing devflow launcher in the client environment to install the release after checksum and signer verification")
		}
		return
	}
	check(true, "launcher prerequisites present in the environment running setup")
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
