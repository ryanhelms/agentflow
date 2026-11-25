package slackhost

import (
	"regexp"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/nscaledev/agentflow/internal/provider/native"
	"github.com/nscaledev/agentflow/internal/provider/nativemap"
)

var timestamp = regexp.MustCompile(`^[0-9]{1,16}\.[0-9]{6}$`)

func (s *session) Check(call native.Call) error {
	if call.Provider != "slack" || call.Server != "slack" || !s.permits(call.Tool, call.Arguments) {
		return native.ErrDenied
	}
	if call.Tool == send && call.Scope != s.scope {
		return native.ErrDenied
	}
	schema := s.tools[call.Tool].input
	if schema == nil {
		return native.ErrDenied
	}
	// An open object can validate parameters the server does not recognize.
	// Require a declared, typed property for every parameter we rely on.
	for field := range call.Arguments {
		want := "string"
		switch field {
		case "exclude_archived":
			want = "boolean"
		case "limit":
			want = "integer"
		}
		if !declaresType(schema.Properties[field], want) {
			return native.ErrDenied
		}
	}
	if native.ValidateInputSchema(schema, call.Arguments) != nil {
		return native.ErrDenied
	}
	return nil
}

func declaresType(s *jsonschema.Schema, want string) bool {
	if s == nil {
		return false
	}
	if s.Type == want || slices.Contains(s.Types, want) || want == "integer" && s.Type == "number" {
		return true
	}
	return slices.ContainsFunc(s.AnyOf, func(alt *jsonschema.Schema) bool { return declaresType(alt, want) })
}

func (s *session) permits(tool string, args map[string]any) bool {
	switch tool {
	case send:
		return len(args) == 2 && args["channel_id"] == s.channel && args["message"] == s.message
	case profile:
		return len(args) == 0 // Omitting user_id selects the authenticated user.
	case channels:
		if args["team_id"] != s.team || args["types"] != nativemap.SlackChannelTypes(s.channel) ||
			args["format"] != "ids_only" || args["exclude_archived"] != true {
			return false
		}
		cursor, _ := args["cursor"].(string)
		return len(args) == 4 || len(args) == 5 && cursor != "" && len(cursor) <= 512
	case channelHistory:
		return len(args) == 1 && args["channel_id"] == s.channel
	case thread:
		ts, _ := args["message_ts"].(string)
		limit := args["limit"]
		return len(args) == 4 && args["channel_id"] == s.channel && timestamp.MatchString(ts) &&
			(limit == 20 || limit == float64(20)) && args["response_format"] == "detailed"
	default:
		return false
	}
}
