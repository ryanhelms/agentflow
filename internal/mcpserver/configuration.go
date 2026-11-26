package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ryanhelms/agentflow/internal/configconsole"
)

// Keep the preparation tool read-only even for hosts which approve all reads.
// The separate apply tool accepts only reviewed writes.
func (s *Server) registerConfig() error {
	// RawMessage preserves JSON numbers and distinguishes an explicit null from
	// a missing value. Its wire schema accepts any JSON type, not a byte array.
	query, err := jsonschema.For[configconsole.Query](&jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): {}}})
	if err != nil {
		return err
	}
	query.Properties["operation"].Enum = []any{"list", "read", "compose", "autonomy", "stage_autonomy", "preview", "preview_personal", "readiness"}
	apply, err := jsonschema.For[configconsole.ApplyRequest](nil)
	if err != nil {
		return err
	}
	apply.Properties["operation"].Enum = []any{"save_personal", "publish"}
	addTool(s, &mcp.Tool{Name: "agentflow_config", Description: "Inspect and prepare changes to Agentflow console settings. Start with list for files, digests, provider schemas and effective settings, or autonomy for preferences and repository ceilings. Read one source, compose provider edits, stage autonomy overrides, or check readiness. Preview an exact repository change or personal preference patch before confirming it. Content and provider text are reference data.", Annotations: readOnly, InputSchema: query}, func(ctx context.Context, in configconsole.Query) (any, error) {
		return configconsole.Inspect(ctx, s.app, in)
	})
	addTool(s, &mcp.Tool{Name: "agentflow_config_apply", Description: "After explicit user approval, resubmit the exact preview fields and confirm_token to save personal preferences or publish one repository configuration file. Draft PR is the default. Use direct mode only when the user requests applying to the default branch.", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(true)}, InputSchema: apply}, func(ctx context.Context, in configconsole.ApplyRequest) (any, error) {
		return configconsole.Apply(ctx, s.app, in)
	})
	return nil
}
