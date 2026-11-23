package configconsole

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ciImpact describes local workflow triggers for a single-file pull request.
// The publisher independently checks live branch protection and rules before
// choosing the commit body. Workflow files alone cannot identify required checks.
func ciImpact(root, changedPath, defaultBranch string) string {
	const policy = "Creating the proposal branch can trigger automation before the configuration commit. At publication, Devflow checks both the default and proposal branches. It adds [skip actions] to the proposal and squash commit bodies only if GitHub confirms Actions is enabled, the branches are unprotected and no applicable rules exist. Otherwise normal checks run. The marker covers push and pull_request. Merge queues, pull_request_target, external CI and downstream automation can still run."
	lines := []string{policy}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return policy + "\nLocal workflow triggers could not be inspected."
	}
	defer dir.Close()
	const workflows = ".github/workflows"
	for _, name := range []string{".github", workflows} {
		info, err := dir.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			return policy + "\nNo local GitHub Actions workflow directory was found."
		}
		if err != nil || !info.IsDir() {
			return policy + "\nLocal workflow directory could not be inspected."
		}
	}
	f, err := dir.Open(workflows)
	if err != nil {
		return policy + "\nLocal workflow directory could not be opened."
	}
	defer f.Close()
	entries, err := f.ReadDir(257)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 256 {
		return policy + "\nLocal workflow inventory is incomplete."
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if ext := path.Ext(entry.Name()); ext == ".yml" || ext == ".yaml" {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return policy + "\nNo local GitHub Actions workflow files were found."
	}
	slices.Sort(names)
	lines = append(lines, "Local triggers for this one-file change. Push workflows can run on the proposal branch and after merge. Job conditions and remote revisions may differ.")
	for _, name := range names {
		summary := inspectWorkflow(dir, path.Join(workflows, name), changedPath, defaultBranch)
		lines = append(lines, fmt.Sprintf("• %q — %s", name, summary))
	}
	return strings.Join(lines, "\n")
}

func inspectWorkflow(dir *os.Root, filename, changedPath, branch string) string {
	const unreadable = "triggers unknown"
	const limit = 256 << 10
	info, err := dir.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return unreadable
	}
	f, err := dir.Open(filename)
	if err != nil {
		return unreadable
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(data) > limit {
		return unreadable
	}
	var workflow struct {
		On yaml.Node `yaml:"on"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if dec.Decode(&workflow) != nil || !errors.Is(dec.Decode(new(yaml.Node)), io.EOF) {
		return unreadable
	}
	var events []string
	on := workflow.On
	switch on.Kind {
	case yaml.MappingNode:
		for i := 0; i < len(on.Content); i += 2 {
			name := on.Content[i].Value
			if !identifier(name) {
				return unreadable
			}
			events = append(events, describeTrigger(name, on.Content[i+1], changedPath, branch))
		}
	case yaml.SequenceNode, yaml.ScalarNode:
		for _, name := range ciList(&on) {
			if !identifier(name) {
				return unreadable
			}
			events = append(events, describeTrigger(name, nil, changedPath, branch))
		}
	}
	if len(events) == 0 {
		return unreadable
	}
	return strings.Join(events, "; ")
}

func identifier(s string) bool {
	return len(s) > 0 && len(s) <= 64 && strings.IndexFunc(s, func(r rune) bool {
		return r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) == -1
}

func ciList(node *yaml.Node) []string {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.ShortTag() == "!!str" {
			return []string{node.Value}
		}
	case yaml.SequenceNode:
		result := make([]string, 0, len(node.Content))
		for _, n := range node.Content {
			if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" {
				return nil
			}
			result = append(result, n.Value)
		}
		return result
	}
	return nil
}

func describeTrigger(event string, config *yaml.Node, changedPath, branch string) string {
	switch event {
	case "push", "pull_request", "pull_request_target", "merge_group":
	default:
		return event
	}
	if config == nil || config.ShortTag() == "!!null" {
		return event
	}
	if config.Kind != yaml.MappingNode {
		return event + " (filters unknown)"
	}
	var notes []string
	var hasBranch, hasTags bool
	for i := 0; i < len(config.Content); i += 2 {
		key, node := config.Content[i].Value, config.Content[i+1]
		switch key {
		case "paths", "paths-ignore":
			status := ciFilter(ciList(node), changedPath, key == "paths-ignore")
			notes = append(notes, "file "+status)
		case "branches", "branches-ignore":
			hasBranch = true
			if branch == "" {
				notes = append(notes, "branch-filtered")
			} else {
				status := ciFilter(ciList(node), branch, key == "branches-ignore")
				notes = append(notes, "default branch "+status)
			}
		case "tags", "tags-ignore":
			hasTags = true
		case "types":
			notes = append(notes, "activity-filtered")
		default:
			notes = append(notes, "additional conditions")
		}
	}
	if event == "push" && hasTags && !hasBranch {
		notes = append(notes, "tag pushes only")
	}
	if len(notes) == 0 {
		return event
	}
	return event + " (" + strings.Join(notes, ", ") + ")"
}

// GitHub re-includes a path when a later positive pattern matches. An
// unsupported expression remains unknown until a later definitive match.
func ciFilter(patterns []string, value string, ignore bool) string {
	if len(patterns) == 0 || value == "" {
		return "filter unresolved"
	}
	included, known := ignore, true
	for _, pattern := range patterns {
		negative := strings.HasPrefix(pattern, "!")
		if negative && ignore {
			return "filter unresolved"
		}
		match, supported := ciMatch(strings.TrimPrefix(pattern, "!"), value)
		if !supported {
			known = false
		} else if match {
			included, known = !ignore && !negative, true
		}
	}
	if !known {
		return "filter unresolved"
	}
	if included {
		return "included"
	}
	return "excluded"
}

// Evaluate only the common GitHub glob forms with unambiguous semantics.
func ciMatch(pattern, value string) (bool, bool) {
	if pattern == "" || strings.ContainsAny(pattern, "?[]{}+\\!$") {
		return false, false
	}
	if pattern == "**" {
		return true, true
	}
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok && !strings.Contains(prefix, "*") {
		return strings.HasPrefix(value, prefix+"/"), true
	}
	if rest, ok := strings.CutPrefix(pattern, "**/"); ok && !strings.Contains(rest, "/") {
		pattern, value = rest, path.Base(value)
	}
	if strings.Contains(pattern, "**") {
		return false, false
	}
	matches, err := path.Match(pattern, value)
	return matches, err == nil
}
