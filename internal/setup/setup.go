// Package setup implements `devflow init` (repo bootstrap) and `devflow install`
// (per-client configuration).
package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/nscaledev/devflow/adapters"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/version"
	"github.com/nscaledev/devflow/templates"
)

// Result lists what a setup step did.
type Result struct {
	Written []string `json:"written,omitempty"`
	Merged  []string `json:"merged,omitempty"`
	Removed []string `json:"removed,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

const (
	blockStart = "<!-- devflow:start -->"
	blockEnd   = "<!-- devflow:end -->"
)

var executables = map[string]bool{".devflow/bin/devflow": true, ".githooks/commit-msg": true}

// Init writes templates/repo into the repository root. Existing files are left
// alone unless force is set; AGENTS.md gets its devflow block inserted or refreshed.
func Init(ctx context.Context, repoDir string, force bool) (*Result, error) {
	res := &Result{}
	root, err := os.OpenRoot(repoDir)
	if err != nil {
		return res, err
	}
	defer root.Close()
	if err := recordEnrollment(root, "", true); err != nil {
		return res, err
	}
	if err := writeAgentInstructions(res, repoDir); err != nil {
		return res, err
	}
	err = fs.WalkDir(templates.Repo, "repo", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, "repo/")
		if rel == agentInstructionsFile {
			return nil
		}
		data, err := templates.Repo.ReadFile(p)
		if err != nil {
			return err
		}
		info, statErr := root.Lstat(rel)
		if statErr == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("%s: expected a regular project file", rel)
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
		if info != nil && !force {
			res.Skipped = append(res.Skipped, rel)
			return nil
		}
		if rel == CallerWorkflow {
			data = PinWorkflow(data, version.Commit, version.Version)
		}
		mode := os.FileMode(0o644)
		if executables[rel] {
			mode = 0o755
		}
		if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
			return err
		}
		if err := publishAfter(root, rel, data, nil); err != nil {
			return err
		}
		if err := root.Chmod(rel, mode); err != nil {
			return err
		}
		res.Written = append(res.Written, rel)
		return nil
	})
	if err != nil {
		return res, err
	}
	if repo, err := gitx.Open(ctx, repoDir); err == nil {
		hooks, _ := repo.Git(ctx, "config", "--get", "core.hooksPath")
		hooks = strings.TrimSpace(hooks)
		switch {
		case hooks == "" && hasDefaultHooks(ctx, repo):
			res.Notes = append(res.Notes, "kept the existing Git hooks; chain .githooks/commit-msg from your commit-msg hook to enable Devflow lint")
		case hooks == "":
			if _, err := repo.Git(ctx, "config", "--local", managedHooks, "true"); err != nil {
				return res, err
			}
			if _, err := repo.Git(ctx, "config", "--local", "core.hooksPath", ".githooks"); err != nil {
				return res, err
			}
			res.Notes = append(res.Notes, "set core.hooksPath=.githooks for this clone")
		case hooks != ".githooks":
			res.Notes = append(res.Notes, "kept the existing core.hooksPath; chain .githooks/commit-msg from your hook to enable Devflow lint")
		}
		// An explicit init undoes the opt-out `devflow remove` leaves behind.
		_, _ = repo.Git(ctx, "config", "--local", "--unset", AutoInitKey)
	}
	res.Notes = append(res.Notes,
		"review .devflow/config.yml and run `devflow setup providers --explain` to see the selected providers",
		"review and commit the generated project files")
	return res, nil
}

// Git's default hooks can live in the shared Git directory of a linked worktree.
func hasDefaultHooks(ctx context.Context, repo *gitx.Repo) bool {
	location, err := repo.Git(ctx, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return true
	}
	hooks := strings.TrimSpace(location)
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(repo.Dir, hooks)
	}
	entries, err := os.ReadDir(hooks)
	if err != nil {
		return !os.IsNotExist(err)
	}
	for _, e := range entries {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".sample") {
			return true
		}
	}
	return false
}

func writeFile(dest string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, data, mode); err != nil {
		return err
	}
	return os.Chmod(dest, mode)
}

// Clients supported by Install.
var Clients = []string{"claude", "codex", "grok", "cursor", "vscode", "gemini"}

var clientBinaries = map[string][]string{
	"claude": {"claude"}, "codex": {"codex"}, "grok": {"grok"},
	"cursor": {"cursor", "cursor-agent"}, "vscode": {"code"}, "gemini": {"gemini"},
}

// Detect returns clients whose CLI is on PATH.
func Detect() []string {
	var found []string
	for _, c := range Clients {
		for _, bin := range clientBinaries[c] {
			if _, err := exec.LookPath(bin); err == nil {
				found = append(found, c)
				break
			}
		}
	}
	return found
}

var clientSettings = map[string]string{
	"claude": ".claude/settings.json", "codex": ".codex/config.toml", "cursor": ".cursor/mcp.json",
	"vscode": ".vscode/mcp.json", "grok": ".grok/settings.json", "gemini": ".gemini/settings.json",
}

// Install configures clients for the repo at repoDir.
func Install(repoDir string, clients []string) (*Result, error) {
	res := &Result{}
	root, err := os.OpenRoot(repoDir)
	if err != nil {
		return res, err
	}
	defer root.Close()
	for _, client := range clients {
		rel, ok := clientSettings[client]
		if !ok {
			return res, fmt.Errorf("unknown client %q (supported: %s)", client, strings.Join(Clients, ", "))
		}
		if err := recordEnrollment(root, rel, true); err != nil {
			return res, err
		}
	}
	if err := writeAgentInstructions(res, repoDir); err != nil {
		return res, err
	}
	for _, client := range clients {
		rel := clientSettings[client]
		if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			return res, err
		}
		if client == "codex" {
			err = installCodex(res, root)
		} else {
			src := mustTemplate(rel)
			if client != "claude" {
				src = mustAdapter(client + "/" + filepath.Base(rel))
			}
			err = mergeJSON(res, root, rel, src)
		}
		if err != nil {
			return res, fmt.Errorf("%s: %w", client, err)
		}
		if client == "gemini" {
			res.Notes = append(res.Notes, `Gemini CLI: set "contextFileName": "AGENTS.md" in .gemini/settings.json if you don't use GEMINI.md`)
		}
	}
	return res, nil
}

func mustAdapter(p string) []byte {
	b, err := adapters.FS.ReadFile(p)
	if err != nil {
		panic(err)
	}
	return b
}

func mustTemplate(p string) []byte {
	b, err := templates.Repo.ReadFile(path.Join("repo", p))
	if err != nil {
		panic(err)
	}
	return b
}

// Merge only missing declarations. A project can deliberately use different
// values for client preferences or an existing server with the same name.
func mergeJSON(res *Result, root *os.Root, rel string, src []byte) error {
	var adapter map[string]any
	if err := json.Unmarshal(src, &adapter); err != nil {
		return err
	}
	return withClientLock(root, rel, func() error {
		current, err := readProjectFile(root, rel)
		if err != nil {
			return err
		}
		doc := registrationJSON(current, &ClientRegistration{})
		if doc == nil {
			return fmt.Errorf("%s is not a valid JSON object", rel)
		}
		changed := false
		for key, expected := range adapter {
			existing, found := doc[key]
			if !found {
				doc[key] = expected
				changed = true
				continue
			}
			if reflect.DeepEqual(existing, expected) {
				continue
			}
			entries, ok := existing.(map[string]any)
			wanted, group := expected.(map[string]any)
			if !ok || !group {
				res.Notes = append(res.Notes, "kept existing setting "+rel+" "+key)
				continue
			}
			for name, value := range wanted {
				if _, found := entries[name]; found {
					continue
				}
				entries[name] = value
				changed = true
			}
		}
		if !changed {
			res.Skipped = append(res.Skipped, rel)
			return nil
		}
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		if err := publishAfter(root, rel, append(out, '\n'), nil); err != nil {
			return err
		}
		if current == nil {
			res.Written = append(res.Written, rel)
		} else {
			res.Merged = append(res.Merged, rel)
		}
		return nil
	})
}

func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMerge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

func installCodex(res *Result, root *os.Root) error {
	block := mustAdapter("codex/config.toml")
	const rel = ".codex/config.toml"
	if err := withClientLock(root, rel, func() error {
		b, err := readProjectFile(root, rel)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "[mcp_servers.devflow]") {
			res.Skipped = append(res.Skipped, rel)
			return nil
		}
		next := block
		if b != nil {
			next = append(append(b, '\n'), block...)
		}
		if err := publishAfter(root, rel, next, nil); err != nil {
			return err
		}
		if b == nil {
			res.Written = append(res.Written, rel)
		} else {
			res.Merged = append(res.Merged, rel)
		}
		return nil
	}); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	promptDir := filepath.Join(home, ".codex", "prompts")
	entries, err := adapters.FS.ReadDir("codex/prompts")
	if err != nil {
		return err
	}
	for _, e := range entries {
		data, _ := adapters.FS.ReadFile("codex/prompts/" + e.Name())
		if err := writeFile(filepath.Join(promptDir, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	res.Written = append(res.Written, promptDir+"/devflow-*.md")
	res.Notes = append(res.Notes, "Codex reads .codex/config.toml only for trusted projects; if devflow tools don't appear, copy the [mcp_servers.devflow] block into ~/.codex/config.toml")
	return nil
}
