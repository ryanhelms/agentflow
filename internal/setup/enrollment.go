package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/nscaledev/agentflow/templates"
	"gopkg.in/yaml.v3"
)

// The receipt records names, never file contents or machine-specific paths. Only
// fixed template paths are consulted during removal. Commit it with the project.
const receiptPath = ".agentflow/installation.yml"
const managedHooks = "agentflow.managedhookspath"

type enrollment struct {
	Version        int      `yaml:"version"`
	Existing       []string `yaml:"existing,omitempty"`
	AgentSeparator int      `yaml:"agent_separator,omitempty"`
	ClientOnly     bool     `yaml:"client_only,omitempty"`
	ClientGuidance bool     `yaml:"client_agent_instructions,omitempty"`
	// Only names from a shipped adapter are recorded. Values may contain credentials.
	ClientEntries map[string][]string `yaml:"existing_client_entries,omitempty"`
}

func readEnrollment(root *os.Root) (*enrollment, error) {
	data, err := readProjectFile(root, receiptPath)
	if err != nil || data == nil {
		return nil, err
	}
	var record enrollment
	if err := yaml.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("read installation receipt: %w", err)
	}
	if record.Version != 1 || record.AgentSeparator < 0 || record.AgentSeparator > 2 {
		return nil, errors.New("invalid agentflow installation receipt")
	}
	return &record, nil
}

func (r *enrollment) existing(name string) bool {
	return r != nil && slices.Contains(r.Existing, name)
}

func legacyInstallation(root *os.Root) bool {
	launcher, _ := readProjectFile(root, ".agentflow/bin/agentflow")
	config, _ := root.Stat(".agentflow/config.yml")
	shebang, body, _ := bytes.Cut(launcher, []byte("\n"))
	return config != nil && config.Mode().IsRegular() && string(bytes.TrimSuffix(shebang, []byte("\r"))) == "#!/bin/sh" && bytes.HasPrefix(body, []byte("# agentflow project launcher"))
}

// An empty client path enrolls the whole project. A client-only installation
// owns its registrations and can later be promoted to full project setup.
func recordEnrollment(root *os.Root, clientFile string, writesGuidance bool) error {
	return withClientLock(root, ".agentflow-installation", func() error {
		r, err := readEnrollment(root)
		if err != nil {
			return err
		}
		if r == nil {
			if legacyInstallation(root) {
				return nil
			}
			r = &enrollment{Version: 1, ClientOnly: true, ClientEntries: map[string][]string{}}
			b, err := readProjectFile(root, agentInstructionsFile)
			if err != nil {
				return err
			}
			if len(b) > 0 && !bytes.Contains(b, []byte(blockStart)) {
				switch {
				case bytes.HasSuffix(b, []byte("\n\n")):
				case b[len(b)-1] == '\n':
					r.AgentSeparator = 1
				default:
					r.AgentSeparator = 2
				}
			}
		}
		if clientFile == "" && !r.ClientOnly {
			return nil
		}
		_, tracked := r.ClientEntries[clientFile]
		if clientFile == "" || !tracked {
			if err := r.capture(root, clientFile); err != nil {
				return err
			}
		} else if !writesGuidance || r.ClientGuidance || !r.ClientOnly {
			return nil
		}
		if r.ClientOnly && writesGuidance {
			r.ClientGuidance = true
		}
		data, err := yaml.Marshal(r)
		if err != nil {
			return err
		}
		if err := root.MkdirAll(".agentflow", 0o755); err != nil {
			return err
		}
		return publishAfter(root, receiptPath, data, nil)
	})
}

func (r *enrollment) capture(root *os.Root, clientFile string) error {
	// Other files can appear between a client install and the first project
	// init. Snapshot them at promotion, keeping the original client baselines.
	observed := map[string]bool{}
	if len(r.ClientEntries) > 0 {
		observed[".agentflow"], observed[agentInstructionsFile] = true, true
		for name := range r.ClientEntries {
			observed[name], observed[path.Dir(name)] = true, true
		}
	}
	record := func(name string) error {
		if observed[name] || r.existing(name) {
			return nil
		}
		_, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil {
			r.Existing = append(r.Existing, name)
		}
		return err
	}
	if clientFile == "" {
		err := fs.WalkDir(templates.Repo, "repo", func(p string, _ fs.DirEntry, err error) error {
			if err != nil || p == "repo" {
				return err
			}
			return record(strings.TrimPrefix(p, "repo/"))
		})
		if err != nil {
			return err
		}
		for _, name := range append(slices.Clone(releaseFiles), ".agentflow/releases.yml") {
			if err := record(name); err != nil {
				return err
			}
		}
		r.ClientOnly = false
		return nil
	}
	if r.ClientOnly {
		for _, name := range []string{".agentflow", agentInstructionsFile, path.Dir(clientFile), clientFile} {
			if err := record(name); err != nil {
				return err
			}
		}
	}
	var keys []string
	if r.existing(clientFile) {
		var err error
		keys, err = existingClientEntries(root, clientFile)
		if err != nil {
			return err
		}
	}
	if r.ClientEntries == nil {
		r.ClientEntries = map[string][]string{}
	}
	r.ClientEntries[clientFile] = keys
	return nil
}

func existingClientEntries(root *os.Root, name string) ([]string, error) {
	data, err := readProjectFile(root, name)
	if err != nil {
		return nil, err
	}
	if name == ".codex/config.toml" {
		if bytes.Contains(data, []byte("[mcp_servers.agentflow]")) {
			return []string{"mcp_servers.agentflow"}, nil
		}
		return nil, nil
	}
	doc := registrationJSON(data, &ClientRegistration{})
	if doc == nil {
		return nil, fmt.Errorf("%s: expected a JSON object", name)
	}
	var adapter map[string]any
	if err := json.Unmarshal(mustTemplate(name), &adapter); err != nil {
		return nil, err
	}
	var keys []string
	for key, value := range adapter {
		original, exists := doc[key]
		if !exists {
			continue
		}
		keys = append(keys, key)
		entries, _ := original.(map[string]any)
		wanted, _ := value.(map[string]any)
		for entry := range wanted {
			if _, exists := entries[entry]; exists {
				keys = append(keys, key+"/"+entry)
			}
		}
	}
	slices.Sort(keys)
	return keys, nil
}
