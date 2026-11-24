package asanahost

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"

	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/asana"
	"github.com/nscaledev/agentflow/internal/provider/native"
	"github.com/nscaledev/agentflow/internal/provider/nativemap"
)

var gid = regexp.MustCompile("^[1-9][0-9]{0,31}$")

type operation struct {
	title, notes string // Approved create fields for readback.
	ref          provider.Ref
	write        nativemap.TaskWrite
}

// Rebuild the mapped write from its semantic inputs. Equality to the original
// call rejects extra fields at every level and keeps a private, immutable copy.
func prepareOperation(scope Scope, call native.Call) (operation, error) {
	var op operation
	if call.Server != "asana" || !gid.MatchString(call.Scope) || scope.Project != "" && call.Scope != scope.Project {
		return op, native.ErrDenied
	}
	ref := provider.Ref{Instance: "asana", Scope: call.Scope}
	items, _ := call.Arguments["tasks"].([]any)
	var item map[string]any
	if len(items) == 1 {
		item, _ = items[0].(map[string]any)
	}
	var mapped nativemap.TaskWrite
	var err error
	switch call.Tool {
	case create:
		title, _ := item["name"].(string)
		notes, _ := item["notes"].(string)
		op.title, op.notes = title, notes
		description, pin, links, decodeErr := asana.DecodeTaskNotes(notes)
		if decodeErr != nil {
			return op, native.ErrDenied
		}
		draft := provider.TaskDraft{Scope: call.Scope, Title: title, Description: description, Template: pin, Links: links}
		mapped, err = nativemap.AsanaCreate(draft)
	case comment:
		ref.ID, _ = call.Arguments["task_id"].(string)
		body, _ := call.Arguments["text"].(string)
		mapped, err = nativemap.AsanaComment(ref, body)
	case complete:
		if !scope.EnableCompletion {
			return op, native.ErrDenied
		}
		ref.ID, _ = item["task_id"].(string)
		mapped, err = nativemap.AsanaComplete(ref)
	default:
		return op, native.ErrDenied
	}
	if err != nil || !reflect.DeepEqual(mapped.Call(), call) {
		return op, native.ErrDenied
	}
	op.ref, op.write = ref, mapped
	return op, nil
}

func taskArgs(id string) map[string]any {
	return map[string]any{"task_gid": id, "include_subtasks": false, "include_comments": false}
}

// Reads and comments may wrap one resource in data. Batch receipts have a
// separate top-level shape. Keep error objects and nested MCP envelopes out of
// both the receipt decoder and the App's native.Payload conversion.
func resultObject(raw json.RawMessage, allowData bool) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, native.ErrDenied
	}
	if data, found := fields["data"]; found {
		if !allowData || len(fields) != 1 {
			return nil, native.ErrDenied
		}
		return resultObject(data, false)
	}
	for key := range fields {
		switch strings.ToLower(key) {
		case "error", "errors", "content", "structuredcontent", "iserror", "resulttype":
			return nil, native.ErrDenied
		}
	}
	return raw, nil
}
