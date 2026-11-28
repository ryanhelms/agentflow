package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/adapters"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/templates"
)

// RemoveOptions control Remove.
type RemoveOptions struct {
	Force        bool // also delete template files the team has changed
	DryRun       bool // report only
	KeepReleases bool // keep release-please-config.json and .release-please-manifest.json
}

// AutoInitKey is the per-clone git config that turns automatic init off. Remove sets it
// so the AI-client plugin does not set agentflow up again on the next session.
const AutoInitKey = "agentflow.autoinit"

// Files agentflow owns outright: removed even when edited.
var ownedPaths = []string{
	".agentflow/config.yml",
	".agentflow/releases.yml",
	".agentflow/bin/agentflow",
	".agentflow/bin/agentflow.cmd",
	".agentflow/bin/agentflow.ps1",
	".github/workflows/agentflow.yml",
}

// Files `agentflow release init` generates.
var releaseFiles = []string{"release-please-config.json", ".release-please-manifest.json"}

// JSON client configs that are merged into, not owned: only agentflow's keys are removed.
var mergedJSON = map[string][]string{
	".claude/settings.json": {"template:.claude/settings.json"},
	".cursor/mcp.json":      {"template:.cursor/mcp.json", "adapter:cursor/mcp.json"},
	".vscode/mcp.json":      {"template:.vscode/mcp.json", "adapter:vscode/mcp.json"},
	".gemini/settings.json": {"template:.gemini/settings.json", "adapter:gemini/settings.json"},
	".grok/settings.json":   {"template:.grok/settings.json", "adapter:grok/settings.json"},
}

// Remove undoes Init and Install in the repository at repoDir: it deletes the files
// agentflow owns, strips agentflow's entries from shared files (AGENTS.md, client configs),
// unsets core.hooksPath when .githooks is gone and turns automatic init off for this
// clone. Nothing is committed; the developer reviews and commits the deletions.
func Remove(ctx context.Context, repoDir string, opt RemoveOptions) (*Result, error) {
	res := &Result{}
	root, err := os.OpenRoot(repoDir)
	if err != nil {
		return res, err
	}
	defer root.Close()
	receipt, err := readEnrollment(root)
	if err != nil {
		return res, err
	}
	fullInstall := receipt != nil && !receipt.ClientOnly || receipt == nil && legacyInstallation(root)
	guidance, _ := readProjectFile(root, agentInstructionsFile)
	legacyClients := receipt == nil && (fullInstall || bytes.Contains(guidance, []byte(blockStart)) && bytes.Contains(guidance, []byte(blockEnd)))
	if receipt == nil && !fullInstall {
		if repo, openErr := gitx.Open(ctx, repoDir); openErr == nil {
			optedOut, _ := repo.Git(ctx, "config", "--local", "--type=bool", AutoInitKey)
			if strings.TrimSpace(optedOut) == "false" && !bytes.Contains(guidance, []byte(blockStart)) && !hasCompanionReceipts(repoDir) {
				res.Notes = append(res.Notes, "Agentflow was already removed from this clone")
				return res, nil
			}
		}
	}
	del := func(rel string) error {
		if receipt.existing(rel) {
			res.Notes = append(res.Notes, "kept "+rel+" (present before installation)")
			return nil
		}
		info, err := root.Lstat(filepath.FromSlash(rel))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			res.Skipped = append(res.Skipped, rel+" (expected a regular project file; review manually)")
			return nil
		}
		res.Removed = append(res.Removed, rel)
		if opt.DryRun {
			return nil
		}
		return root.Remove(filepath.FromSlash(rel))
	}

	for _, rel := range ownedPaths {
		if fullInstall && exists(repoDir, rel) {
			if err := del(rel); err != nil {
				return res, err
			}
		}
	}
	if fullInstall && !opt.KeepReleases {
		for _, rel := range releaseFiles {
			if exists(repoDir, rel) {
				if err := del(rel); err != nil {
					return res, err
				}
			}
		}
	}

	// Other template files: delete when unchanged (or forced), otherwise keep.
	err = fs.WalkDir(templates.Repo, "repo", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !fullInstall {
			return err
		}
		rel := strings.TrimPrefix(p, "repo/")
		if rel == "AGENTS.md" || rel == ".codex/config.toml" || mergedJSON[rel] != nil || isOwned(rel) {
			return nil
		}
		cur, err := readProjectFile(root, filepath.FromSlash(rel))
		if errors.Is(err, errNotRegular) {
			res.Skipped = append(res.Skipped, rel+" (expected a regular project file; review manually)")
			return nil
		}
		if err == nil && cur == nil {
			return nil
		}
		if err != nil {
			return err
		}
		want, _ := templates.Repo.ReadFile(p)
		if !bytes.Equal(cur, want) && !opt.Force {
			res.Skipped = append(res.Skipped, rel+" (changed since agentflow init; --force to delete)")
			return nil
		}
		return del(rel)
	})
	if err != nil {
		return res, err
	}

	groups := map[string]string{}
	for rel := range mergedJSON {
		groups[rel] = ""
	}
	for _, format := range companionClients {
		groups[format.path] = format.group
	}
	for _, rel := range slices.Sorted(maps.Keys(groups)) {
		cleanup := legacyClients
		var protected []string
		if !fullInstall && receipt == nil {
			protected = []string{"includeCoAuthoredBy", "contextFileName"}
		}
		if receipt != nil {
			var claimed bool
			protected, claimed = receipt.ClientEntries[rel]
			cleanup = claimed || fullInstall && !receipt.existing(rel)
		}
		if err := unmergeJSON(res, root, rel, groups[rel], cleanup, protected, receipt.existing(rel), opt.DryRun); err != nil {
			return res, fmt.Errorf("%s: %w", rel, err)
		}
	}
	cleanCodex := legacyClients
	if receipt != nil {
		protected, claimed := receipt.ClientEntries[".codex/config.toml"]
		cleanCodex = claimed && !slices.Contains(protected, "mcp_servers.agentflow") || fullInstall && !receipt.existing(".codex/config.toml")
	}
	if cleanCodex {
		if err := removeCodex(res, root, receipt.existing(".codex/config.toml"), opt.DryRun); err != nil {
			return res, err
		}
	}
	if receipt == nil || fullInstall || receipt.ClientGuidance {
		if err := removeBlock(res, root, receipt, opt.DryRun); err != nil {
			return res, err
		}
	}
	if receipt != nil {
		if err := del(receiptPath); err != nil {
			return res, err
		}
	}
	if (fullInstall || receipt != nil) && !opt.DryRun {
		directories := []string{".agentflow/bin", ".agentflow", ".githooks", ".jira", "docs", ".github/workflows", ".github", ".claude", ".cursor", ".vscode", ".gemini", ".grok", ".codex"}
		if !fullInstall {
			directories = []string{".agentflow"}
			for name := range receipt.ClientEntries {
				if _, known := mergedJSON[name]; known || name == ".codex/config.toml" {
					directories = append(directories, filepath.Dir(name))
				}
			}
		}
		for _, dir := range directories {
			if !receipt.existing(dir) {
				removeIfEmpty(root, filepath.FromSlash(dir))
			}
		}
	}

	if repo, err := gitx.Open(ctx, repoDir); err == nil && !opt.DryRun {
		hp, _ := repo.Git(ctx, "config", "--local", "core.hooksPath")
		managed, _ := repo.Git(ctx, "config", "--local", managedHooks)
		if strings.TrimSpace(hp) == ".githooks" && (strings.TrimSpace(managed) == "true" || (fullInstall && receipt == nil && !exists(repoDir, ".githooks"))) {
			if _, err := repo.Git(ctx, "config", "--local", "--unset", "core.hooksPath"); err == nil {
				res.Notes = append(res.Notes, "unset core.hooksPath for this clone")
			}
		}
		if strings.TrimSpace(managed) == "true" {
			_, _ = repo.Git(ctx, "config", "--local", "--unset", managedHooks)
		}
		if _, err := repo.Git(ctx, "config", "--local", AutoInitKey, "false"); err == nil {
			res.Notes = append(res.Notes, "automatic init is off for this clone (git config "+AutoInitKey+" false); `agentflow init` turns it back on")
		}
	}
	res.Notes = append(res.Notes,
		"commit the deletions on a branch and open a PR; other clones keep core.hooksPath until they pull",
		"repo variables and secrets (JIRA_*, AGENTFLOW_*) and ~/.codex/prompts/agentflow-*.md are not touched")
	return res, nil
}

func hasCompanionReceipts(repoDir string) bool {
	for _, format := range companionClients {
		if exists(repoDir, companionReceiptName(format.path)) {
			return true
		}
	}
	return false
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

func removeIfEmpty(root *os.Root, dir string) {
	if info, err := root.Lstat(dir); err == nil && info.IsDir() {
		_ = root.Remove(dir)
	}
}

// unmergeJSON removes the keys agentflow merged into a shared JSON file, deleting the
// file when nothing else is left in it.
func unmergeJSON(res *Result, root *os.Root, rel, group string, cleanup bool, protected []string, keepFile, dryRun bool) error {
	parent, name := filepath.Split(filepath.FromSlash(rel))
	parent = filepath.Clean(parent)
	info, err := root.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		res.Notes = append(res.Notes, "kept "+rel+" (inspect the project directory by hand)")
		return nil
	}
	dir, err := root.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	if dryRun {
		return removeClientEntries(res, dir, rel, name, group, cleanup, protected, keepFile, true)
	}
	// Only take the project writer lock for an actual change. Re-read both
	// files under it so a concurrent setup cannot invalidate the preview.
	var preview Result
	if err := removeClientEntries(&preview, dir, rel, name, group, cleanup, protected, keepFile, true); err != nil {
		return err
	}
	if len(preview.Merged)+len(preview.Removed) == 0 {
		res.Notes = append(res.Notes, preview.Notes...)
		res.Skipped = append(res.Skipped, preview.Skipped...)
		return nil
	}
	return withClientLock(dir, name, func() error {
		return removeClientEntries(res, dir, rel, name, group, cleanup, protected, keepFile, false)
	})
}

func removeClientEntries(res *Result, dir *os.Root, rel, name, group string, cleanup bool, protected []string, keepFile, dryRun bool) error {
	var records map[string]string
	if group != "" {
		var err error
		records, err = readCompanionRecords(dir, name)
		if err != nil {
			res.Notes = append(res.Notes, "kept "+companionReceiptName(rel)+" (could not read companion ownership; review manually)")
		}
	}
	current, err := readProjectFile(dir, name)
	if errors.Is(err, errNotRegular) {
		res.Notes = append(res.Notes, "kept "+rel+" (inspect the file and remove agentflow's entries by hand)")
		return nil
	}
	if err != nil {
		return err
	}
	doc := registrationJSON(current, &ClientRegistration{Path: rel})
	if doc == nil {
		res.Skipped = append(res.Skipped, rel+" (not valid JSON; remove agentflow's entries by hand)")
		return nil
	}
	changed, err := removeCompanionEntries(res, doc, rel, group, records)
	if err != nil {
		return err
	}
	var sources []string
	if cleanup {
		sources = mergedJSON[rel]
	}
	for _, src := range sources {
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
		if removeAdapterEntries(doc, sub, protected) {
			changed = true
		}
	}
	if cleanup && removeAdapterEntries(doc, legacyOverlay(rel), protected) {
		changed = true
	}
	if changed {
		if err := saveClientRemoval(res, dir, rel, name, doc, keepFile, dryRun); err != nil {
			return err
		}
	}
	if records == nil {
		return nil
	}
	res.Removed = append(res.Removed, companionReceiptName(rel))
	if dryRun {
		return nil
	}
	return dir.Remove(companionReceiptName(name))
}

func saveClientRemoval(res *Result, dir *os.Root, rel, name string, doc map[string]any, keepFile, dryRun bool) error {
	if len(doc) == 0 && !keepFile {
		res.Removed = append(res.Removed, rel)
		if dryRun {
			return nil
		}
		return dir.Remove(name)
	}
	res.Merged = append(res.Merged, rel)
	if dryRun {
		return nil
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return publishAfter(dir, name, append(out, '\n'), nil)
}

func removeCompanionEntries(res *Result, doc map[string]any, rel, group string, records map[string]string) (bool, error) {
	if group == "" {
		return false, nil
	}
	entries, _ := doc[group].(map[string]any)
	changed := false
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		if !receiptedKey(rel, key) {
			continue
		}
		id := fmt.Sprintf("%s %q", rel, key)
		expected, tracked := records[key]
		if !tracked {
			res.Notes = append(res.Notes, "kept "+id+" (untracked entry; review manually)")
			continue
		}
		digest, err := entryDigest(entries[key])
		if err != nil {
			return false, err
		}
		if digest != expected {
			res.Notes = append(res.Notes, "kept "+id+" (changed since registration; review manually)")
			continue
		}
		delete(entries, key)
		if key == "agentflow" {
			res.Notes = append(res.Notes, "remove project launcher "+id)
		} else {
			res.Notes = append(res.Notes, "remove companion "+id)
		}
		changed = true
	}
	if changed && len(entries) == 0 {
		delete(doc, group)
	}
	return changed, nil
}

// A server or marketplace declaration is one registration. Removing only its
// command from an edited declaration would leave a broken user-owned entry.
func removeAdapterEntries(doc, adapter map[string]any, protected []string) bool {
	changed := false
	for key, expected := range adapter {
		current, ok := doc[key]
		if !ok {
			continue
		}
		wanted, group := expected.(map[string]any)
		if !group {
			if !slices.Contains(protected, key) && reflect.DeepEqual(current, expected) {
				delete(doc, key)
				changed = true
			}
			continue
		}
		entries, ok := current.(map[string]any)
		if !ok {
			continue
		}
		removed := false
		for name, entry := range wanted {
			if !slices.Contains(protected, key+"/"+name) && reflect.DeepEqual(entries[name], entry) {
				delete(entries, name)
				removed = true
			}
		}
		if removed {
			changed = true
			if len(entries) == 0 && !slices.Contains(protected, key) {
				delete(doc, key)
			}
		}
	}
	return changed
}

// Remove only the shipped Codex block, keeping surrounding comments and the
// entire table if a user added settings to it.
func removeCodex(res *Result, root *os.Root, keepFile, dryRun bool) error {
	const rel = ".codex/config.toml"
	data, err := readProjectFile(root, rel)
	if err != nil {
		return err
	}
	block := mustAdapter("codex/config.toml")
	start := bytes.Index(data, block)
	if start < 0 {
		return nil
	}
	tail := data[start+len(block):]
	next := bytes.TrimSpace(tail)
	if len(next) > 0 && (!bytes.HasPrefix(next, []byte("[")) || bytes.HasPrefix(next, []byte("[mcp_servers.agentflow."))) {
		return nil
	}
	if start > 0 && data[start-1] != '\n' {
		return nil
	}
	if keepFile && start > 0 {
		start--
	} // installCodex added this separator.
	remaining := append(bytes.Clone(data[:start]), tail...)
	if len(remaining) == 0 && !keepFile {
		res.Removed = append(res.Removed, rel)
		if dryRun {
			return nil
		}
		return root.Remove(rel)
	}
	res.Merged = append(res.Merged, rel)
	if dryRun {
		return nil
	}
	return publishAfter(root, rel, remaining, nil)
}

// removeBlock deletes the agentflow marker block from a markdown file, and the file when
// only the block was in it.
func removeBlock(res *Result, root *os.Root, receipt *enrollment, dryRun bool) error {
	const rel = agentInstructionsFile
	b, err := readProjectFile(root, rel)
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
	if receipt != nil {
		prefix := strings.TrimSuffix(s[:i], strings.Repeat("\n", receipt.AgentSeparator))
		rest = prefix + strings.TrimPrefix(s[j+len(blockEnd):], "\n")
	} else if strings.TrimSpace(rest) != "" {
		rest = strings.TrimRight(rest, "\n") + "\n"
	}
	if strings.TrimSpace(rest) == "" && !receipt.existing(rel) {
		res.Removed = append(res.Removed, rel)
		if dryRun {
			return nil
		}
		return root.Remove(rel)
	}
	res.Merged = append(res.Merged, rel)
	if dryRun {
		return nil
	}
	return publishAfter(root, rel, []byte(rest), nil)
}
