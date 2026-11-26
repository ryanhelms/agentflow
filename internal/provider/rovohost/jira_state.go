package rovohost

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/internal/provider/native"
	"github.com/ryanhelms/agentflow/internal/provider/nativemap"
)

func jiraStateTool(name string) bool {
	switch name {
	case issueTool, accountTool, transitionsTool, assignTool, moveTool:
		return true
	}
	return false
}

// Discovery reads run before the exact write arguments are known. Each lease
// subsequently permits only the same issue and its own required state reads.
func (s *session) jiraStateCall(call native.Call) (native.Call, error) {
	args := call.Arguments
	if s.site == "" || args["cloudId"] != s.site ||
		call.Tool != accountTool && (s.issueKey == "" || args["issueIdOrKey"] != s.issueKey) {
		return native.Call{}, native.ErrDenied
	}
	switch call.Tool {
	case issueTool:
		fields, _ := args["fields"].([]string)
		if len(args) != 4 || args["fieldsByKeys"] != true || !slices.Equal(fields, []string{"summary", "status", "project", "assignee"}) {
			return native.Call{}, native.ErrDenied
		}
		call.Arguments = maps.Clone(args)
		call.Arguments["view"] = "full"
	case accountTool:
		if len(args) != 1 {
			return native.Call{}, native.ErrDenied
		}
	case transitionsTool:
		if len(args) != 2 {
			return native.Call{}, native.ErrDenied
		}
	case moveTool:
		if len(args) != 3 || s.stateTarget == "" || args["transitionId"] != s.stateTarget {
			return native.Call{}, native.ErrDenied
		}
	case assignTool:
		if len(args) != 3 || s.stateTarget == "" || args["assignee"] != s.stateTarget {
			return native.Call{}, native.ErrDenied
		}
		schema := s.tools[assignTool]
		if schema == nil {
			return native.Call{}, native.ErrDenied
		}
		if schema.Properties["assignee"] == nil {
			call.Arguments = map[string]any{"cloudId": s.site, "issueIdOrKey": s.issueKey,
				"fields": map[string]any{"assignee": map[string]any{"accountId": s.stateTarget}}}
		}
	default:
		return native.Call{}, native.ErrDenied
	}
	return call, s.checkSchema(call.Tool, call.Arguments)
}

// The issue readback proves the effect. Reject an explicit error or a
// contradictory write acknowledgement even if another actor reached that state.
func (s *session) checkStateResult(tool string, raw json.RawMessage) error {
	if err := nativemap.CheckJiraWriteResult(raw); err != nil {
		return err
	}
	var result struct {
		OK           *bool       `json:"ok"`
		Key          string      `json:"key"`
		IssueKey     string      `json:"issueKey"`
		IssueIDOrKey string      `json:"issueIdOrKey"`
		TransitionID json.Number `json:"transitionId"`
	}
	if json.Unmarshal(raw, &result) != nil || result.OK != nil && !*result.OK {
		return fmt.Errorf("%w: Jira state operation returned an unsuccessful or malformed result", nativemap.ErrInvalidResult)
	}
	if tool == assignTool || tool == moveTool {
		for _, key := range []string{result.Key, result.IssueKey, result.IssueIDOrKey} {
			if key != "" && !strings.EqualFold(key, s.issueKey) {
				return fmt.Errorf("%w: Jira acknowledgement identifies another issue", nativemap.ErrInvalidResult)
			}
		}
		if tool == moveTool && result.TransitionID != "" && result.TransitionID.String() != s.stateTarget {
			return fmt.Errorf("%w: Jira acknowledgement identifies another transition", nativemap.ErrInvalidResult)
		}
	}
	return nil
}

// Distinguish an explicitly unassigned issue from a projection that omitted
// assignee or project. The shared App decoder also checks key, title and status.
func (s *session) checkStateIssue(raw json.RawMessage) error {
	var issue struct {
		Fields struct {
			Project struct {
				Key string `json:"key"`
			} `json:"project"`
			Assignee json.RawMessage `json:"assignee"`
		} `json:"fields"`
	}
	keyEnd := strings.LastIndexByte(s.issueKey, '-')
	if keyEnd <= 0 || len(raw) > nativemap.MaxResultBytes || json.Unmarshal(raw, &issue) != nil ||
		issue.Fields.Project.Key == "" || !strings.EqualFold(issue.Fields.Project.Key, s.issueKey[:keyEnd]) ||
		issue.Fields.Assignee == nil {
		return fmt.Errorf("%w: Jira state read omitted the approved project or assignee", nativemap.ErrInvalidResult)
	}
	if string(issue.Fields.Assignee) != "null" {
		if _, err := nativemap.DecodeJiraCurrentUser(issue.Fields.Assignee); err != nil {
			return err
		}
	}
	return nil
}
