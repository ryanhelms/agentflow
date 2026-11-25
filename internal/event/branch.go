package event

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/nscaledev/agentflow/internal/commit"
	"github.com/nscaledev/agentflow/internal/flowmap"
	"github.com/nscaledev/agentflow/internal/provider"
)

func (a GitHubActionsContext) refs(body, legacyText string) ([]provider.Ref, error) {
	if a.TaskInstance == "jira" {
		var refs []provider.Ref
		for _, key := range commit.KeysIn(legacyText) {
			project, _, _ := strings.Cut(key, "-")
			refs = append(refs, provider.Ref{Instance: "jira", Scope: project, ID: key})
		}
		return refs, nil
	}
	refs, err := provider.TaskRefs(body)
	if err != nil {
		return nil, err
	}
	canonical := refs[:0]
	for _, ref := range refs {
		if a.TaskInstance == "github-issues" && strings.EqualFold(ref.Scope, a.TaskScope) {
			ref.Scope = a.TaskScope
		}
		if ref.Instance != a.TaskInstance || ref.Scope != a.TaskScope {
			return nil, fmt.Errorf("Task-Ref %q is outside configured %s scope %q", ref.String(), a.TaskInstance, a.TaskScope)
		}
		if !slices.Contains(canonical, ref) {
			canonical = append(canonical, ref)
		}
	}
	return canonical, nil
}

func (p Plan) branchEvent(a GitHubActionsContext, payload []byte) (Plan, error) {
	var e struct {
		Repository githubRepository `json:"repository"`
		Ref        string           `json:"ref"`
		RefType    string           `json:"ref_type"`
		Commits    []struct {
			Message string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(payload, &e); err != nil {
		return Plan{}, fmt.Errorf("decode github %s: %w", a.EventName, err)
	}
	if err := a.validate(); err != nil {
		return Plan{}, err
	}
	if !a.matches(e.Repository) {
		p.Skip = RepositoryMismatch
		return p, nil
	}
	if a.EventName == "create" {
		if e.RefType != "branch" {
			p.Skip = UnsupportedAction
			return p, nil
		}
		p.Lifecycle = flowmap.BranchCreated
		refs, err := a.refs("", e.Ref)
		if err != nil {
			return Plan{}, err
		}
		for _, ref := range refs {
			p.Transitions = append(p.Transitions, Transition{Task: ref, OperationID: p.operationID(ref)})
		}
	} else {
		branch, ok := strings.CutPrefix(e.Ref, "refs/heads/")
		if !ok {
			p.Skip = UnsupportedAction
			return p, nil
		}
		if a.DefaultBranch == "" {
			return Plan{}, fmt.Errorf("github push requires the trusted default branch")
		}
		seen := map[string]bool{}
		for _, c := range e.Commits {
			if skipBody.MatchString(c.Message) {
				continue
			}
			subject, body, _ := strings.Cut(c.Message, "\n")
			verbs := commit.Parse(subject).Verbs
			if len(verbs) == 0 {
				continue
			}
			refs, err := a.refs(body, subject)
			if err != nil {
				return Plan{}, err
			}
			for _, ref := range refs {
				for _, verb := range verbs {
					if verb == "done" && branch != a.DefaultBranch {
						continue
					}
					intent := Transition{Task: ref, Lifecycle: "#" + verb}
					// Include the verb in the key so distinct transitions on one task survive replay.
					intent.OperationID = p.operationID(ref) + "/" + verb
					if !seen[intent.OperationID] {
						p.Transitions = append(p.Transitions, intent)
						seen[intent.OperationID] = true
					}
				}
			}
		}
	}
	if len(p.Transitions) == 0 {
		p.Skip = NoTaskRefs
	}
	return p, nil
}
