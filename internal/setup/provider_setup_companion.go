package setup

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/nscaledev/devflow/internal/managedmcp"
	"github.com/nscaledev/devflow/internal/provision"
)

// RegisterCompanion changes just the reserved key reviewed by the user.
// The hash binds consent to the previous entry even when it contains inline
// credentials. Neither its contents nor other settings enter the preview.
func RegisterCompanion(dir string, r *ClientRegistration, s managedmcp.Status) error {
	return registerOverlay(dir, func() (*ClientRegistration, []byte, error) {
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
	r := &ClientRegistration{Client: client, State: "blocked", Entry: "devflow-" + s.ID}
	format, ok := companionClients[client]
	if !ok {
		r.Detail = "managed MCP registration supports claude, cursor, gemini and vscode project configurations"
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
	current, err := registrationBytes(dir, r)
	if err != nil || r.Detail != "" {
		return r, nil, err
	}
	doc := registrationJSON(current, r)
	if doc == nil {
		return r, nil, nil
	}
	entries := map[string]any{}
	if existing, ok := doc[format.group]; ok {
		entries, ok = existing.(map[string]any)
		if !ok {
			r.Detail = "inspect the conflicting " + format.group + " in " + r.Path
			return r, nil, nil
		}
	}
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
	r.State = "pending"
	r.Detail = "add " + r.Entry + " to " + r.Path
	if prev, ok := entries[r.Entry]; ok {
		if reflect.DeepEqual(prev, wanted) {
			r.State, r.Detail = "configured", "managed entry matches the verified companion"
			return r, nil, nil
		}
		previous, err := json.Marshal(prev)
		if err != nil {
			return nil, nil, err
		}
		r.PreviousSHA256 = fmt.Sprintf("%x", sha256.Sum256(previous))
		r.Detail = "replace the reserved " + r.Entry + " entry in " + r.Path
	}
	entries[r.Entry] = wanted
	doc[format.group] = entries
	next, err := json.MarshalIndent(doc, "", "  ")
	return r, append(next, '\n'), err
}
