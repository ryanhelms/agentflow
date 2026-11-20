package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/nscaledev/devflow/adapters"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/templates"
)

// RemoveOptions control Remove.
type RemoveOptions struct {
	Force        bool // also delete template files the team has changed
	DryRun       bool // report only
	KeepReleases bool // keep release-please-config.json and .release-please-manifest.json
}

// AutoInitKey is the per-clone git config that turns automatic init off. Remove sets it
// so the AI-client plugin does not set devflow up again on the next session.
const AutoInitKey = "devflow.autoinit"

// Files devflow owns outright: removed even when edited.
var ownedPaths = []string{".devflow", ".github/workflows/devflow.yml"}

// Files `devflow release init` generates.
var releaseFiles = []string{"release-please-config.json", ".release-please-manifest.json"}

// JSON client configs that are merged into, not owned: only devflow's keys are removed.
var mergedJSON = map[string][]string{
	".claude/settings.json": {"template:.claude/settings.json"},
	".cursor/mcp.json":      {"template:.cursor/mcp.json", "adapter:cursor/mcp.json"},
	".vscode/mcp.json":      {"template:.vscode/mcp.json", "adapter:vscode/mcp.json"},
	".gemini/settings.json": {"template:.gemini/settings.json", "adapter:gemini/settings.json"},
	".grok/settings.json":   {"template:.grok/settings.json", "adapter:grok/settings.json"},
}

// Remove undoes Init and Install in the repository at repoDir: it deletes the files
// devflow owns, strips devflow's entries from shared files (AGENTS.md, client configs),
// unsets core.hooksPath when .githooks is gone and turns automatic init off for this
// clone. Nothing is committed; the developer reviews and commits the deletions.
func Remove(ctx context.Context, repoDir string, opt RemoveOptions) (*Result, error) {
	res := &Result{}
	del := func(rel string) error {
		res.Removed = append(res.Removed, rel)
		if opt.DryRun {
			return nil
		}
		return os.RemoveAll(filepath.Join(repoDir, filepath.FromSlash(rel)))
	}

	for _, rel := range ownedPaths {
		if exists(repoDir, rel) {
			if err := del(rel); err != nil {
				return res, err
			}
		}
	}
	if !opt.KeepReleases {
		for _, rel := range releaseFiles {
			if exists(repoDir, rel) {
				if err := del(rel); err != nil {
					return res, err
				}
			}
		}
	}

	// Other template files: delete when unchanged (or forced), otherwise keep.
	err := fs.WalkDir(templates.Repo, "repo", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, "repo/")
		if rel == "AGENTS.md" || rel == ".codex/config.toml" || mergedJSON[rel] != nil || isOwned(rel) {
			return nil
		}
		cur, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(rel)))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		want, _ := templates.Repo.ReadFile(p)
		if !bytes.Equal(cur, want) && !opt.Force {
			res.Skipped = append(res.Skipped, rel+" (changed since devflow init; --force to delete)")
			return nil
		}
		return del(rel)
	})
	if err != nil {
		return res, err
	}

	rels := make([]string, 0, len(mergedJSON))
	for rel := range mergedJSON {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if err := unmergeJSON(res, repoDir, rel, opt.DryRun); err != nil {
			return res, fmt.Errorf("%s: %w", rel, err)
		}
	}
	if err := removeCodex(res, repoDir, opt.DryRun); err != nil {
		return res, err
	}
	if err := removeBlock(res, repoDir, "AGENTS.md", opt.DryRun); err != nil {
		return res, err
	}
	if !opt.DryRun {
		for _, dir := range []string{".githooks", ".jira", "docs", ".github/workflows", ".github", ".claude", ".cursor", ".vscode", ".gemini", ".grok", ".codex"} {
			removeIfEmpty(filepath.Join(repoDir, filepath.FromSlash(dir)))
		}
	}

	if repo, err := gitx.Open(ctx, repoDir); err == nil && !opt.DryRun {
		hp, _ := repo.Git(ctx, "config", "--local", "core.hooksPath")
		if strings.TrimSpace(hp) == ".githooks" && !exists(repoDir, ".githooks") {
			if _, err := repo.Git(ctx, "config", "--local", "--unset", "core.hooksPath"); err == nil {
				res.Notes = append(res.Notes, "unset core.hooksPath for this clone")
			}
		}
		if _, err := repo.Git(ctx, "config", "--local", AutoInitKey, "false"); err == nil {
			res.Notes = append(res.Notes, "automatic init is off for this clone (git config "+AutoInitKey+" false); `devflow init` turns it back on")
		}
	}
	res.Notes = append(res.Notes,
		"commit the deletions on a branch and open a PR; other clones keep core.hooksPath until they pull",
		"repo variables and secrets (JIRA_*, DEVFLOW_*) and ~/.codex/prompts/devflow-*.md are not touched")
	return res, nil
}

func isOwned(rel string) bool {
	for _, o := range ownedPaths {
		if rel == o || strings.HasPrefix(rel, o+"/") {
			return true
		}
	}
	return false
}

func exists(repoDir, rel string) bool {
	_, err := os.Lstat(filepath.Join(repoDir, filepath.FromSlash(rel)))
	return err == nil
}

func removeIfEmpty(dir string) {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
}

// unmergeJSON removes the keys devflow merged into a shared JSON file, deleting the
// file when nothing else is left in it.
func unmergeJSON(res *Result, repoDir, rel string, dryRun bool) error {
	dest := filepath.Join(repoDir, filepath.FromSlash(rel))
	b, err := os.ReadFile(dest)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cur := map[string]any{}
	if err := json.Unmarshal(b, &cur); err != nil {
		res.Skipped = append(res.Skipped, rel+" (not valid JSON; remove devflow's entries by hand)")
		return nil
	}
	changed := false
	for _, src := range mergedJSON[rel] {
		var data []byte
		if p, ok := strings.CutPrefix(src, "template:"); ok {
			data = mustTemplate(p)
		} else {
			data, err = adapters.FS.ReadFile(strings.TrimPrefix(src, "adapter:"))
			if err != nil {
				return err
			}
		}
		var sub map[string]any
		if err := json.Unmarshal(data, &sub); err != nil {
			return err
		}
		if deepRemove(cur, sub) {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if len(cur) == 0 {
		res.Removed = append(res.Removed, rel)
		if dryRun {
			return nil
		}
		return os.Remove(dest)
	}
	res.Merged = append(res.Merged, rel)
	if dryRun {
		return nil
	}
	out, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dest, append(out, '\n'), 0o644)
}

// deepRemove deletes from dst every key in src whose value matches, recursing into
// objects and dropping objects left empty. It reports whether dst changed.
func deepRemove(dst, src map[string]any) bool {
	changed := false
	for k, v := range src {
		dv, ok := dst[k]
		if !ok {
			continue
		}
		sm, sok := v.(map[string]any)
		dm, dok := dv.(map[string]any)
		switch {
		case sok && dok:
			if deepRemove(dm, sm) {
				changed = true
			}
			if len(dm) == 0 {
				delete(dst, k)
			}
		case reflect.DeepEqual(dv, v):
			delete(dst, k)
			changed = true
		}
	}
	return changed
}

// removeCodex drops the [mcp_servers.devflow] tables from .codex/config.toml.
func removeCodex(res *Result, repoDir string, dryRun bool) error {
	const rel = ".codex/config.toml"
	dest := filepath.Join(repoDir, filepath.FromSlash(rel))
	b, err := os.ReadFile(dest)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var out []string
	skipping, changed := false, false
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			name := strings.Trim(t, "[] ")
			skipping = name == "mcp_servers.devflow" || strings.HasPrefix(name, "mcp_servers.devflow.")
			if skipping {
				changed = true
				// drop the comment lines that introduce the table
				for len(out) > 0 && strings.HasPrefix(strings.TrimSpace(out[len(out)-1]), "#") {
					out = out[:len(out)-1]
				}
			}
		}
		if !skipping {
			out = append(out, line)
		}
	}
	if !changed {
		return nil
	}
	rest := strings.TrimSpace(strings.Join(out, "\n"))
	if rest == "" {
		res.Removed = append(res.Removed, rel)
		if dryRun {
			return nil
		}
		return os.Remove(dest)
	}
	res.Merged = append(res.Merged, rel)
	if dryRun {
		return nil
	}
	return os.WriteFile(dest, []byte(rest+"\n"), 0o644)
}

// removeBlock deletes the devflow marker block from a markdown file, and the file when
// only the block was in it.
func removeBlock(res *Result, repoDir, rel string, dryRun bool) error {
	dest := filepath.Join(repoDir, rel)
	b, err := os.ReadFile(dest)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	s := string(b)
	i := strings.Index(s, blockStart)
	j := strings.Index(s, blockEnd)
	if i < 0 || j < i {
		return nil
	}
	rest := strings.TrimRight(s[:i], "\n ") + "\n" + strings.TrimLeft(s[j+len(blockEnd):], "\n")
	if strings.TrimSpace(rest) == "" {
		res.Removed = append(res.Removed, rel)
		if dryRun {
			return nil
		}
		return os.Remove(dest)
	}
	res.Merged = append(res.Merged, rel)
	if dryRun {
		return nil
	}
	return os.WriteFile(dest, []byte(strings.TrimRight(rest, "\n")+"\n"), 0o644)
}
