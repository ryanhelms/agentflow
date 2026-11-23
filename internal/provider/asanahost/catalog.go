package asanahost

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/provider/native"
)

type toolSchema struct {
	input, output *jsonschema.Resolved
	contract      string
}

func parseSchema(value any) (*jsonschema.Resolved, error) {
	raw, err := json.Marshal(value)
	var schema jsonschema.Schema
	if err != nil || json.Unmarshal(raw, &schema) != nil || schema.Type != "object" {
		return nil, native.ErrDenied
	}
	resolved, err := schema.Resolve(nil) // No external schema loader.
	if err != nil {
		return nil, native.ErrDenied
	}
	return resolved, nil
}

func (s *session) loadCatalog(ctx context.Context) error {
	tools := map[string]toolSchema{}
	wanted := []string{s.write.Call().Tool, getTask, getProject, getMe}
	params := &mcp.ListToolsParams{}
	seen := map[string]bool{}
	for range 24 {
		page, err := s.conn.ListTools(ctx, params)
		if err != nil {
			return safeError(err)
		}
		for _, tool := range page.Tools {
			if !slices.Contains(wanted, tool.Name) {
				continue
			}
			if _, ok := tools[tool.Name]; ok {
				return fmt.Errorf("%w: duplicate Asana tool", native.ErrDenied)
			}
			input, err := parseSchema(tool.InputSchema)
			if err != nil {
				return err
			}
			spec := toolSchema{input: input}
			if tool.OutputSchema != nil {
				spec.output, err = parseSchema(tool.OutputSchema)
				if err != nil {
					return err
				}
			}
			raw, err := json.Marshal([]any{tool.InputSchema, tool.OutputSchema})
			if err != nil {
				return native.ErrDenied
			}
			spec.contract = string(raw)
			if s.tools != nil && s.tools[tool.Name].contract != spec.contract {
				return fmt.Errorf("%w: Asana tool schema changed", native.ErrDenied)
			}
			tools[tool.Name] = spec
		}
		if page.NextCursor == "" {
			if len(tools) != len(wanted) {
				return fmt.Errorf("%w: missing Asana write or verification tool", native.ErrDenied)
			}
			if !tools[s.write.Call().Tool].accepts(s.write.Call().Arguments) {
				return native.ErrDenied
			}
			// A creation cannot yet supply its future GID. Verify the readback
			// contract now, then validate the actual GID before its read.
			id := s.ref.ID
			if id == "" {
				id = s.ref.Scope // Validate a numeric example without sending a request.
			}
			if !tools[getTask].accepts(taskArgs(id)) {
				return native.ErrDenied
			}
			s.tools = tools
			return nil
		}
		if seen[page.NextCursor] {
			break
		}
		seen[page.NextCursor] = true
		params.Cursor = page.NextCursor
	}
	return fmt.Errorf("%w: incomplete Asana tool catalog", native.ErrDenied)
}

func (s toolSchema) accepts(args map[string]any) bool {
	return s.input != nil && declares(s.input.Schema(), args) && s.input.Validate(args) == nil
}

// An open object may validate fields that a server silently ignores. Require
// typed declarations for the submitted fields, including the single batch item.
func declares(s *jsonschema.Schema, v any) bool {
	if s == nil {
		return false
	}
	switch x := v.(type) {
	case map[string]any:
		if s.Type != "object" {
			return false
		}
		for k, v := range x {
			if !declares(s.Properties[k], v) {
				return false
			}
		}
	case []any:
		if s.Type != "array" || len(x) != 1 || !declares(s.Items, x[0]) {
			return false
		}
	case string:
		return s.Type == "string" || slices.Contains(s.Types, "string")
	case bool:
		return s.Type == "boolean" || slices.Contains(s.Types, "boolean")
	default:
		return false
	}
	return true
}
