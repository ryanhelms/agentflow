package nativemap

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/githubissues"
)

func GitHubIssueCreate(draft provider.TaskDraft) (TaskWrite, error) {
	if err := githubissues.ValidateDraft("github-issues", draft); err != nil {
		return TaskWrite{}, err
	}
	if draft.Template != nil {
		return TaskWrite{}, provider.Unsupported("github-issues", "native confirmation of a stored issue template marker")
	}
	args, err := githubWriteRepo(draft.Scope)
	if err != nil {
		return TaskWrite{}, err
	}
	args["method"], args["title"], args["body"] = "create", draft.Title, githubissues.EncodeIssueBody(draft.Description)
	if len(draft.Labels) > 0 {
		args["labels"] = append([]string(nil), draft.Labels...)
	}
	ref := provider.Ref{Instance: "github-issues", Scope: draft.Scope}
	return taskWrite(GitHub, "issue_write", args, func(raw json.RawMessage) (provider.WriteResult, error) {
		return githubWriteResult(ref, false, raw)
	}), nil
}

func GitHubIssueClose(ref provider.Ref) (TaskWrite, error) {
	args, err := githubWriteIssue(ref)
	if err != nil {
		return TaskWrite{}, err
	}
	args["method"], args["state"] = "update", "closed"
	return taskWrite(GitHub, "issue_write", args, func(raw json.RawMessage) (provider.WriteResult, error) {
		return githubWriteResult(ref, false, raw)
	}), nil
}

func GitHubIssueComment(ref provider.Ref, body string) (TaskWrite, error) {
	if strings.TrimSpace(body) == "" {
		return TaskWrite{}, errors.New("github issue comment is empty")
	}
	args, err := githubWriteIssue(ref)
	if err != nil {
		return TaskWrite{}, err
	}
	args["body"] = githubissues.EncodeIssueBody(body)
	return taskWrite(GitHub, "add_issue_comment", args, func(raw json.RawMessage) (provider.WriteResult, error) {
		return githubWriteResult(ref, true, raw)
	}), nil
}

func githubWriteRepo(scope string) (map[string]any, error) {
	owner, repo, _ := strings.Cut(scope, "/")
	if !repositoryScope.MatchString(scope) || repo == "." || repo == ".." {
		return nil, provider.Unsupported("github-issues", "native writes without an owner/repo scope")
	}
	return map[string]any{"owner": owner, "repo": repo}, nil
}

func githubWriteIssue(ref provider.Ref) (map[string]any, error) {
	if ref.Instance != "github-issues" {
		return nil, errors.New("expected a github-issues reference")
	}
	args, err := githubWriteRepo(ref.Scope)
	if err != nil {
		return nil, err
	}
	number, err := strconv.ParseInt(ref.ID, 10, 32)
	if err != nil || number <= 0 || strconv.FormatInt(number, 10) != ref.ID {
		return nil, provider.Unsupported("github-issues", "native writes without a supported issue number")
	}
	args["issue_number"] = number
	return args, nil
}

func githubWriteResult(ref provider.Ref, comment bool, raw json.RawMessage) (provider.WriteResult, error) {
	type receipt struct {
		ID      json.RawMessage `json:"id"`
		URL     string          `json:"url"`
		HTMLURL string          `json:"html_url"`
		Number  int64           `json:"number"`
	}
	var result struct {
		receipt
		Single   *receipt        `json:"single"`
		Combined json.RawMessage `json:"combined"`
	}
	if err := decodeObject(raw, &result); err != nil {
		return provider.WriteResult{}, err
	}
	if len(result.Combined) != 0 {
		return provider.WriteResult{}, invalidTaskWriteResult(GitHub)
	}
	if result.Single != nil {
		if !comment || len(result.ID) != 0 || result.URL != "" || result.HTMLURL != "" || result.Number != 0 {
			return provider.WriteResult{}, invalidTaskWriteResult(GitHub)
		}
		result.receipt = *result.Single
	}
	id := strings.Trim(string(result.ID), "\"")
	if !positiveGitHubID(id, 64) {
		return provider.WriteResult{}, invalidTaskWriteResult(GitHub)
	}
	url := result.URL
	if result.HTMLURL != "" {
		if url != "" && url != result.HTMLURL {
			return provider.WriteResult{}, invalidTaskWriteResult(GitHub)
		}
		url = result.HTMLURL
	}
	u, ok := httpsURL(url)
	if !ok || !strings.EqualFold(u.Host, "github.com") || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" {
		return provider.WriteResult{}, invalidTaskWriteResult(GitHub)
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 5 || !strings.EqualFold(parts[1]+"/"+parts[2], ref.Scope) || parts[3] != "issues" ||
		!positiveGitHubID(parts[4], 32) || (ref.ID != "" && ref.ID != parts[4]) ||
		(result.Number != 0 && strconv.FormatInt(result.Number, 10) != parts[4]) {
		return provider.WriteResult{}, invalidTaskWriteResult(GitHub)
	}
	if (comment && u.Fragment != "issuecomment-"+id) || (!comment && u.Fragment != "") {
		return provider.WriteResult{}, invalidTaskWriteResult(GitHub)
	}
	ref.ID = parts[4]
	return provider.WriteResult{Ref: ref, Status: provider.Applied, URL: url}, nil
}

func positiveGitHubID(value string, bits int) bool {
	id, err := strconv.ParseInt(value, 10, bits)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

// GitHubIssueRepository maps a bounded lookup for the named repository before
// creation. Filter by name and owner and verify the full name in the result.
func GitHubIssueRepository(scope string) (Read[int64], error) {
	args, err := githubWriteRepo(scope)
	if err != nil {
		return Read[int64]{}, err
	}
	query := fmt.Sprintf("%q in:name user:%s repo:%s", args["repo"], args["owner"], scope)
	call := Call{
		capability: Capability{Kind: GitHub, Tool: "search_repositories"},
		instance:   "github-issues",
	}
	call.args, err = json.Marshal(map[string]any{"query": query, "perPage": 20})
	if err != nil {
		return Read[int64]{}, err
	}
	return Read[int64]{call: call, decode: func(raw json.RawMessage) (int64, error) {
		var result struct {
			Incomplete bool `json:"incomplete_results"`
			Items      []struct {
				ID       int64  `json:"id"`
				FullName string `json:"full_name"`
			} `json:"items"`
		}
		if err := decodeObject(raw, &result); err != nil {
			return 0, err
		}
		var id int64
		for _, item := range result.Items {
			if strings.EqualFold(item.FullName, scope) {
				if id != 0 || item.ID <= 0 {
					return 0, invalidTaskWriteResult(GitHub)
				}
				id = item.ID
			}
		}
		if result.Incomplete || id == 0 {
			return 0, invalidTaskWriteResult(GitHub)
		}
		return id, nil
	}}, nil
}
