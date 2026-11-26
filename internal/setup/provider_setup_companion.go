package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/ryanhelms/agentflow/internal/managedmcp"
	"github.com/ryanhelms/agentflow/internal/provision"
)

// RegisterCompanion changes just the reserved key reviewed by the user.
// The hash binds consent to the previous entry even when it contains inline
// credentials. Neither its contents nor other settings enter the preview.
func RegisterCompanion(dir string, r *ClientRegistration, s managedmcp.Status) error {
	return registerOverlay(dir, companionEntry(r.Client, s), func() (*ClientRegistration, []byte, error) {
		now, data, err := companionOverlay(dir, r.Client, s)
		if err == nil && now.State != "configured" && *now != *r {
			return nil, nil, provision.ErrStalePlan
		}
		return now, data, err
	})
}

// Project MCP file formats and credential interpolation differ by host.
var companionClients = map[string]struct {
	path, group, urlKey, envPrefix string
	httpType                       bool
}{
	"vscode": {".vscode/mcp.json", "servers", "url", "${env:", true},
	"cursor": {".cursor/mcp.json", "mcpServers", "url", "${env:", false},
	"claude": {".mcp.json", "mcpServers", "url", "${", true},
	"gemini": {".gemini/settings.json", "mcpServers", "httpUrl", "${", false},
}

var geminiVariable = regexp.MustCompile(`\$[A-Za-z_]|%[A-Za-z_][A-Za-z0-9_]*%`)

func companionOverlay(dir, client string, s managedmcp.Status) (*ClientRegistration, []byte, error) {
	r := &ClientRegistration{Client: client, State: "blocked", Entry: "agentflow-" + s.ID}
	format, ok := companionClients[runtimeClient(client)]
	if !ok {
		r.Detail = "managed MCP registration supports claude, claude-desktop, cursor, gemini and vscode project configurations"
		return r, nil, nil
	}
	r.Path = filepath.FromSlash(format.path)
	if !s.Ready {
		r.Detail = "verify the declared MCP companion before registration"
		return r, nil, nil
	}
	// Editors interpolate variables in command strings; the registered path
	// must resolve to the same literal executable whose bytes were checked.
	if strings.Contains(s.Command, "${") || client == "gemini" && (geminiVariable.MatchString(s.Command) || geminiVariable.MatchString(s.Endpoint)) {
		r.Detail = "use a literal executable path and endpoint without client variable substitutions"
		return r, nil, nil
	}
	return serverOverlay(dir, r, format.group, companionEntry(client, s))
}

// Only companion keys (agentflow-<id>) can be replaced with fresh approval.
func serverOverlay(dir string, r *ClientRegistration, group string, wanted map[string]any) (*ClientRegistration, []byte, error) {
	current, err := registrationBytes(dir, r)
	if err != nil || r.Detail != "" {
		return r, nil, err
	}
	doc := registrationJSON(current, r)
	if doc == nil {
		return r, nil, nil
	}
	entries := map[string]any{}
	if existing, ok := doc[group]; ok {
		entries, ok = existing.(map[string]any)
		if !ok {
			r.Detail = "inspect the conflicting " + group + " in " + r.Path
			return r, nil, nil
		}
	}
	r.State, r.Detail = "pending", "add "+r.Entry+" to "+r.Path
	if r.Entry == "agentflow" {
		inspectDesktopLauncher(r, entries, wanted)
		if r.State != "pending" {
			return r, nil, nil
		}
	} else if prev, ok := entries[r.Entry]; ok {
		if reflect.DeepEqual(prev, wanted) {
			r.State, r.Detail = "configured", "managed entry matches the verified companion"
			return r, nil, nil
		}
		r.PreviousSHA256, err = entryDigest(prev)
		if err != nil {
			return nil, nil, err
		}
		r.Detail = "replace the reserved " + r.Entry + " entry in " + r.Path
	}
	entries[r.Entry] = wanted
	doc[group] = entries
	next, err := json.MarshalIndent(doc, "", "  ")
	return r, append(next, '\n'), err
}

func companionEntry(client string, s managedmcp.Status) map[string]any {
	format := companionClients[runtimeClient(client)]
	wanted := map[string]any{}
	env := format.envPrefix + s.AuthEnv + "}"
	if s.Kind == provision.RemoteMCP {
		wanted[format.urlKey] = s.Endpoint
		if format.httpType {
			wanted["type"] = "http"
		}
		if s.AuthEnv != "" {
			wanted["headers"] = map[string]any{"Authorization": "Bearer " + env}
		}
	} else {
		wanted["command"] = s.Command
		if client != "gemini" {
			wanted["type"] = "stdio"
		}
		if len(s.Args) > 0 {
			args := make([]any, len(s.Args))
			for i, arg := range s.Args {
				args[i] = arg
			}
			wanted["args"] = args
		}
		if s.AuthEnv != "" {
			wanted["env"] = map[string]any{s.AuthEnv: env}
		}
	}
	return wanted
}

func entryDigest(entry any) (string, error) {
	b, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

func companionReceiptName(name string) string { return name + ".agentflow-companions.json" }

func receiptedKey(config, key string) bool {
	return strings.HasPrefix(key, "agentflow-") || config == ".mcp.json" && key == "agentflow"
}

// Each client has a project-local receipt containing only keys and entry digests.
func readCompanionRecords(dir *os.Root, config string) (map[string]string, error) {
	name := companionReceiptName(config)
	data, err := readProjectFile(dir, name)
	if err != nil || data == nil {
		return nil, err
	}
	var records map[string]string
	invalid := fmt.Errorf("invalid companion registration receipt %s", name)
	if err := json.Unmarshal(data, &records); err != nil || len(records) == 0 {
		return nil, invalid
	}
	for key, digest := range records {
		sum, err := hex.DecodeString(digest)
		if !receiptedKey(config, key) || err != nil || len(sum) != sha256.Size {
			return nil, invalid
		}
	}
	return records, nil
}

// Stage the receipt first, then publish it only if the project entry was written.
func recordCompanion(dir *os.Root, name, key string, entry map[string]any, writeConfig func() error) error {
	records, err := readCompanionRecords(dir, name)
	if err != nil {
		return err
	}
	if records == nil {
		records = map[string]string{}
	}
	records[key], err = entryDigest(entry)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return publishAfter(dir, companionReceiptName(name), append(data, '\n'), writeConfig)
}
