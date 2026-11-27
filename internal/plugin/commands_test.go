package plugin_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ryanhelms/agentflow/internal/command"
	_ "github.com/ryanhelms/agentflow/internal/command/ops" // the real Specs
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// TestInvocableCommandsTakeJSONInput checks that every command a plugin
// may invoke is a registered JSONCommand, and that the real notify and
// comment decode the inputs the samples send.
func TestInvocableCommandsTakeJSONInput(t *testing.T) {
	for _, name := range manifest.InvocableCommands {
		c, ok := command.Default.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if _, ok := c.(command.JSONCommand); !ok {
			t.Fatalf("%s is not a JSONCommand", name)
		}
	}
	// Bind and stop: the Invoker never runs the operation.
	var bound []*command.Call
	inv := command.InvokerFunc(func(_ context.Context, c *command.Call) (any, error) { bound = append(bound, c); return nil, nil })
	for name, input := range map[string]string{
		"notify":  `{"signal":"ci.failed","operation_id":"ci-notify-abc","title":"CI failed on feat","body":"Failed checks: lint"}`,
		"comment": `{"text":"note"}`,
	} {
		c, _ := command.Default.Lookup(name)
		res := c.(command.JSONCommand).InvokeJSON(context.Background(), inv, nil, json.RawMessage(input), "", command.Origin{Source: "plugin:x", Client: "plugin:x"})
		if res.Err != nil {
			t.Fatalf("%s rejected the sample input: %v", name, res.Err)
		}
		if bad := c.(command.JSONCommand).InvokeJSON(context.Background(), inv, nil, json.RawMessage(`{"signal":"s","confirm_token":"cf_x"}`), "", command.Origin{}); bad.Err == nil {
			t.Fatalf("%s accepted a confirm token", name)
		}
	}
	if len(bound) != 2 || bound[0].Origin.Source != "plugin:x" {
		t.Fatalf("bound: %d", len(bound))
	}
}
