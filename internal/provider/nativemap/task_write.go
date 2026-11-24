package nativemap

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/native"
)

// TaskWrite binds one vendor mutation and its typed acknowledgement to the
// exact scope and payload. The application prepares and dispatches the call
// through an authenticated write binding.
type TaskWrite struct {
	call   native.Call
	decode func(json.RawMessage) (provider.WriteResult, error)
}

func (w TaskWrite) Call() native.Call { return w.call }

func (w TaskWrite) Decode(raw json.RawMessage) (provider.WriteResult, error) {
	if w.decode == nil {
		return provider.WriteResult{}, errors.New("native task write was not prepared")
	}
	return w.decode(raw)
}

func taskWrite(kind Kind, tool string, args map[string]any, decode func(json.RawMessage) (provider.WriteResult, error)) TaskWrite {
	return TaskWrite{native.Call{Provider: string(kind), Server: string(kind), Tool: tool, Arguments: args}, decode}
}

func invalidTaskWriteResult(kind Kind) error {
	return fmt.Errorf("%w: %s did not acknowledge the exact task write", ErrInvalidResult, kind)
}
