// Package setup implements `devflow init` (repo bootstrap) and `devflow install`
// (per-client configuration).
package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/nscaledev/devflow/adapters"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/templates"
)

// Result lists what a setup step did.
type Result struct {
	Written []string `json:"written,omitempty"`
	Merged  []string `json:"merged,omitempty"`
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
	err := fs.WalkDir(templates.Repo, "repo", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, "repo/")
		data, err := templates.Repo.ReadFile(p)
		if err != nil {
			return err
		}
		dest := filepath.Join(repoDir, filepath.FromSlash(rel))
		if rel == "AGENTS.md" {
			merged, err := mergeBlock(dest, string(data))
			if err != nil {
				return err
			}
			res.Merged = append(res.Merged, merged...)
			return nil
		}
		if _, err := os.Stat(dest); err == nil && !force {
			res.Skipped = append(res.Skipped, rel)
			return nil
		}
		mode := os.FileMode(0o644)
		if executables[rel] {
			mode = 0o755
		}
		if err := writeFile(dest, data, mode); err != nil {
			return err
		}
		res.Written = append(res.Written, rel)
		return nil
	})
	if err != nil {
		return res, err
	}
	if repo, err := gitx.Open(ctx, repoDir); err == nil {
		if _, err := repo.Git(ctx, "config", "core.hooksPath", ".githooks"); err == nil {
			res.Notes = append(res.Notes, "set core.hooksPath=.githooks for this clone (each developer runs `devflow doctor --fix` or the same git config once)")
		}
	}
	res.Notes = append(res.Notes,
		"edit .devflow/config.yml: jira.site and jira.projects",
		"add your project's block to flow-map.yml in nscaledev/devflow (or point flow_map at a local file)",
		"commit the new files; enabling the Claude Code plugin happens through .claude/settings.json")
	return res, nil
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

// mergeBlock inserts or replaces the devflow marker block in a markdown file.
func mergeBlock(dest, src string) ([]string, error) {
	block := extractBlock(src)
	if block == "" {
		block = strings.TrimSpace(src)
	}
	existing, err := os.ReadFile(dest)
	if errors.Is(err, os.ErrNotExist) {
		return []string{filepath.Base(dest)}, writeFile(dest, []byte(block+"\n"), 0o644)
	}
	if err != nil {
		return nil, err
	}
	s := string(existing)
	if i := strings.Index(s, blockStart); i >= 0 {
		if j := strings.Index(s[i:], blockEnd); j >= 0 {
			s = s[:i] + block + s[i+j+len(blockEnd):]
			return []string{filepath.Base(dest)}, os.WriteFile(dest, []byte(s), 0o644)
		}
	}
	s = strings.TrimRight(s, "\n") + "\n\n" + block + "\n"
	return []string{filepath.Base(dest)}, os.WriteFile(dest, []byte(s), 0o644)
}

func extractBlock(s string) string {
	i := strings.Index(s, blockStart)
	j := strings.Index(s, blockEnd)
	if i < 0 || j < i {
		return ""
	}
	return s[i : j+len(blockEnd)]
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

// Install configures clients for the repo at repoDir.
func Install(repoDir string, clients []string) (*Result, error) {
	res := &Result{}
	merged, err := mergeBlockFromAdapter(repoDir)
	if err != nil {
		return res, err
	}
	res.Merged = append(res.Merged, merged...)
	for _, c := range clients {
		var err error
		switch c {
		case "claude":
			err = mergeJSON(res, filepath.Join(repoDir, ".claude", "settings.json"), mustTemplate(".claude/settings.json"))
		case "codex":
			err = installCodex(res, repoDir)
		case "grok":
			err = mergeJSON(res, filepath.Join(repoDir, ".grok", "settings.json"), mustAdapter("grok/settings.json"))
		case "cursor":
			err = mergeJSON(res, filepath.Join(repoDir, ".cursor", "mcp.json"), mustAdapter("cursor/mcp.json"))
		case "vscode":
			err = mergeJSON(res, filepath.Join(repoDir, ".vscode", "mcp.json"), mustAdapter("vscode/mcp.json"))
		case "gemini":
			err = mergeJSON(res, filepath.Join(repoDir, ".gemini", "settings.json"), mustAdapter("gemini/settings.json"))
			res.Notes = append(res.Notes, `Gemini CLI: set "contextFileName": "AGENTS.md" in .gemini/settings.json if you don't use GEMINI.md`)
		default:
			err = fmt.Errorf("unknown client %q (supported: %s)", c, strings.Join(Clients, ", "))
		}
		if err != nil {
			return res, fmt.Errorf("%s: %w", c, err)
		}
	}
	return res, nil
}

func mergeBlockFromAdapter(repoDir string) ([]string, error) {
	src, err := adapters.FS.ReadFile("agents-md/AGENTS.md")
	if err != nil {
		return nil, err
	}
	return mergeBlock(filepath.Join(repoDir, "AGENTS.md"), string(src))
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

// mergeJSON deep-merges src into the JSON file at dest, keeping the developer's own keys.
func mergeJSON(res *Result, dest string, src []byte) error {
	var add map[string]any
	if err := json.Unmarshal(src, &add); err != nil {
		return err
	}
	cur := map[string]any{}
	if b, err := os.ReadFile(dest); err == nil {
		if err := json.Unmarshal(b, &cur); err != nil {
			return fmt.Errorf("%s is not valid JSON; fix it or remove it first: %w", dest, err)
		}
		res.Merged = append(res.Merged, dest)
	} else {
		res.Written = append(res.Written, dest)
	}
	deepMerge(cur, add)
	out, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(dest, append(out, '\n'), 0o644)
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

func installCodex(res *Result, repoDir string) error {
	block := mustAdapter("codex/config.toml")
	dest := filepath.Join(repoDir, ".codex", "config.toml")
	if b, err := os.ReadFile(dest); err == nil {
		if strings.Contains(string(b), "[mcp_servers.devflow]") {
			res.Skipped = append(res.Skipped, dest)
		} else {
			if err := os.WriteFile(dest, append(append(b, '\n'), block...), 0o644); err != nil {
				return err
			}
			res.Merged = append(res.Merged, dest)
		}
	} else {
		if err := writeFile(dest, block, 0o644); err != nil {
			return err
		}
		res.Written = append(res.Written, dest)
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
