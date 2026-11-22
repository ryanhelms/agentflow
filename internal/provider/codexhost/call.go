// Package codexhost invokes an explicitly selected MCP tool through Codex.
package codexhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	callTimeout  = 60 * time.Second
	maxPages     = 16
	activeEnvKey = "DEVFLOW_CODEX_HOST_ACTIVE"
)

// Call discovers and invokes one MCP tool in an ephemeral Codex thread. Codex
// owns the server configuration and authentication. An error after dispatch
// can leave the tool's outcome unknown.
func Call(ctx context.Context, dir, server, tool string, args map[string]any) (json.RawMessage, error) {
	if os.Getenv(activeEnvKey) != "" || strings.EqualFold(strings.TrimSpace(server), "devflow") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(tool)), "devflow_") {
		return nil, hostError(Permission, "select target", "recursive devflow calls are disabled")
	}
	if strings.TrimSpace(server) == "" || strings.TrimSpace(tool) == "" {
		return nil, hostError(Unavailable, "select target", "server and tool are required")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, hostError(Failure, "select workspace", "cannot resolve workspace directory")
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	host, err := startHost(ctx, dir)
	if err != nil {
		return nil, err
	}
	defer host.close()

	threadID, err := host.startThread(dir)
	if err != nil {
		return nil, err
	}
	status, err := host.findTool(threadID, server, tool)
	if err != nil {
		return nil, err
	}

	if args == nil {
		args = map[string]any{}
	}
	const method = "mcpServer/tool/call"
	result, err := host.request(method, toolCall{
		ThreadID: threadID, Server: server, Tool: tool, Arguments: args, EnvironmentID: status.EnvironmentID,
	})
	if err != nil {
		return nil, err
	}
	var outcome struct {
		Content []json.RawMessage `json:"content"`
		IsError bool              `json:"isError"`
	}
	if err := json.Unmarshal(result, &outcome); err != nil || outcome.Content == nil {
		return nil, hostError(Failure, method, "invalid tool result")
	}
	if outcome.IsError {
		return nil, hostError(toolErrorKind(outcome.Content), method, "tool reported an error")
	}
	return result, nil
}

type toolCall struct {
	ThreadID      string         `json:"threadId"`
	Server        string         `json:"server"`
	Tool          string         `json:"tool"`
	Arguments     map[string]any `json:"arguments"`
	EnvironmentID *string        `json:"environmentId,omitempty"`
}

func (h *host) startThread(dir string) (string, error) {
	_, err := h.request("initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "devflow", "version": "0.1.0"},
		"capabilities": map[string]bool{"explicitGatewayOauth": true},
	})
	if err != nil {
		return "", err
	}
	if err := h.send("initialized", map[string]string{"method": "initialized"}); err != nil {
		return "", err
	}

	const method = "thread/start"
	raw, err := h.request(method, map[string]any{
		"cwd": dir, "ephemeral": true, "approvalPolicy": "never",
		"sandbox": "read-only",
	})
	if err != nil {
		return "", err
	}
	var result struct {
		Thread struct {
			ID        string `json:"id"`
			Ephemeral bool   `json:"ephemeral"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Thread.ID == "" {
		return "", hostError(Failure, method, "invalid thread result")
	}
	if !result.Thread.Ephemeral {
		return "", hostError(Permission, method, "app-server did not create an ephemeral thread")
	}
	return result.Thread.ID, nil
}

type serverStatus struct {
	Name          string                     `json:"name"`
	EnvironmentID *string                    `json:"environmentId"`
	AuthStatus    string                     `json:"authStatus"`
	RuntimeStatus string                     `json:"runtimeStatus"`
	ToolsError    string                     `json:"toolsError"`
	Tools         map[string]json.RawMessage `json:"tools"`
}

func (h *host) findTool(threadID, server, tool string) (*serverStatus, error) {
	const method = "mcpServerStatus/list"
	var target *serverStatus
	var cursor *string
	for range maxPages {
		raw, err := h.request(method, struct {
			ThreadID   string  `json:"threadId"`
			ServerName string  `json:"serverName"`
			Detail     string  `json:"detail"`
			Cursor     *string `json:"cursor,omitempty"`
		}{threadID, server, "toolsAndAuthOnly", cursor})
		if err != nil {
			return nil, err
		}
		var page struct {
			Data       []serverStatus `json:"data"`
			NextCursor *string        `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil || page.Data == nil {
			return nil, hostError(Failure, method, "invalid server list")
		}
		for _, status := range page.Data {
			if status.Name != server {
				continue
			}
			if target != nil {
				return nil, hostError(Permission, method, "server registration is ambiguous")
			}
			target = &status
		}
		cursor = page.NextCursor
		if cursor == nil {
			return target, target.checkTool(tool)
		}
	}
	return nil, hostError(Failure, method, "server list exceeded page limit")
}

func (s *serverStatus) checkTool(tool string) error {
	const method = "mcpServerStatus/list"
	if s == nil || s.RuntimeStatus == "disabled" {
		return hostError(Unavailable, method, "server is unavailable")
	}
	if s.RuntimeStatus == "authenticationRequired" {
		return hostError(Authentication, method, "server requires authentication")
	}
	if kind := classifyError(0, s.ToolsError); kind != Failure {
		return hostError(kind, method, "server tool discovery failed")
	}
	_, found := s.Tools[tool]
	if s.AuthStatus == "notLoggedIn" && !found && s.RuntimeStatus != "connected" {
		return hostError(Authentication, method, "server requires authentication")
	}
	if s.ToolsError != "" || s.RuntimeStatus == "failed" || s.RuntimeStatus == "cancelled" {
		return hostError(Failure, method, "server tool discovery failed")
	}
	if s.Tools == nil {
		return hostError(Failure, method, "server returned an invalid tool catalog")
	}
	if !found {
		return hostError(Unavailable, method, "tool is unavailable")
	}
	return nil
}
