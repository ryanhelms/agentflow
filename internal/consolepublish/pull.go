package consolepublish

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/nscaledev/agentflow/internal/gh"
)

type pullBranch struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo struct {
		Name string `json:"full_name"`
	} `json:"repo"`
}

type pull struct {
	gh.PR
	Head           pullBranch `json:"head"`
	Base           pullBranch `json:"base"`
	Node           string     `json:"node_id"`
	MergeableState string     `json:"mergeable_state"`
	Mergeable      *bool      `json:"mergeable"`
	AutoMerge      *struct{}  `json:"auto_merge"`
	MergeSHA       string     `json:"merge_commit_sha"`
}

func (a remote) pull(ctx context.Context, base, head string) (*pull, error) {
	owner, _, _ := strings.Cut(a.slug, "/")
	values := url.Values{"state": {"all"}, "base": {base}, "head": {owner + ":" + head}, "sort": {"created"}, "direction": {"desc"}, "per_page": {"1"}}
	var prs []pull
	if err := a.client.Do(ctx, "GET", a.url+"/pulls?"+values.Encode(), nil, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	pr := prs[0]
	if !strings.EqualFold(pr.Base.Repo.Name, a.slug) || !strings.EqualFold(pr.Head.Repo.Name, a.slug) || pr.Base.Ref != base || pr.Head.Ref != head || pr.Number < 1 || pr.Node == "" {
		return nil, errors.New("GitHub returned an unexpected configuration pull request")
	}
	return &pr, nil
}

func (a remote) finish(ctx context.Context, repo repository, pr pull, p proposal, head string, draft bool) Result {
	result := Result{URL: pr.HTMLURL, State: "open", Branch: pr.Head.Ref, Revision: head}
	if pr.Draft {
		result.State = "draft"
	}
	if draft {
		return result
	}
	if pr.Draft {
		var ready struct {
			MarkPullRequestReadyForReview struct {
				PullRequest *struct {
					IsDraft bool `json:"isDraft"`
				} `json:"pullRequest"`
			} `json:"markPullRequestReadyForReview"`
		}
		err := a.client.GraphQL(ctx, `mutation($id:ID!){markPullRequestReadyForReview(input:{pullRequestId:$id}){pullRequest{isDraft}}}`, map[string]any{"id": pr.Node}, &ready)
		if err != nil || ready.MarkPullRequestReadyForReview.PullRequest == nil || ready.MarkPullRequestReadyForReview.PullRequest.IsDraft {
			result.Warning = "The proposal is still a draft. Open it to mark it ready or retry."
			return result
		}
		result.State = "open"
	}
	if !repo.Squash {
		result.Warning = "Squash merging is disabled. Use the repository’s configured merge method on the PR."
		return result
	}
	endpoint := fmt.Sprintf("%s/pulls/%d", a.url, pr.Number)
	fresh, err := a.mergeStatus(ctx, endpoint)
	if err != nil {
		result.Warning = "Open the PR to verify the current merge state."
		return result
	}
	if fresh.MergedAt != "" && fresh.Head.SHA == head && fresh.Base.Ref == repo.Default {
		result.State = "merged"
		result.Revision = fresh.MergeSHA
		return result
	}
	if !strings.EqualFold(fresh.Base.Repo.Name, a.slug) || !strings.EqualFold(fresh.Head.Repo.Name, a.slug) || fresh.Node != pr.Node || fresh.Head.Ref != pr.Head.Ref || fresh.State != "open" || fresh.Draft || fresh.Head.SHA != head || fresh.Base.Ref != repo.Default || fresh.Title != p.Title {
		result.Warning = "The PR changed after preview. Check its current revision before merging."
		return result
	}
	if fresh.AutoMerge != nil {
		result.State = "auto_merge_requested"
		return result
	}
	// Use the host's CLEAN decision and expected SHA. In particular, never
	// request an administrator override when checks or reviews are outstanding.
	headline := fmt.Sprintf("%s (#%d)", p.Title, pr.Number)
	if fresh.MergeableState == "clean" && fresh.Mergeable != nil && *fresh.Mergeable {
		var merged struct {
			Merged bool   `json:"merged"`
			SHA    string `json:"sha"`
		}
		err := a.client.Do(ctx, "PUT", endpoint+"/merge", map[string]string{"merge_method": "squash", "sha": head, "commit_title": headline, "commit_message": p.body()}, &merged)
		if err == nil && merged.Merged {
			result.State = "merged"
			result.Revision = merged.SHA
			return result
		}
		result.Warning = "GitHub did not confirm the merge. Inspect the PR or retry the same edit to reconcile."
		return result
	}
	if !repo.Auto {
		result.Warning = "Auto-merge is disabled for this repository. The PR is ready for its normal merge workflow."
		return result
	}
	input := map[string]any{"pullRequestId": fresh.Node, "expectedHeadOid": head, "mergeMethod": "SQUASH", "commitHeadline": headline, "commitBody": p.body()}
	var armed struct {
		EnablePullRequestAutoMerge struct {
			PullRequest struct {
				AutoMergeRequest *struct{} `json:"autoMergeRequest"`
			} `json:"pullRequest"`
		} `json:"enablePullRequestAutoMerge"`
	}
	err = a.client.GraphQL(ctx, `mutation($input:EnablePullRequestAutoMergeInput!){enablePullRequestAutoMerge(input:$input){pullRequest{autoMergeRequest{enabledAt}}}}`, map[string]any{"input": input}, &armed)
	if err != nil || armed.EnablePullRequestAutoMerge.PullRequest.AutoMergeRequest == nil {
		result.Warning = "GitHub did not confirm auto-merge. Open the PR to check its requirements, or retry the same edit."
		return result
	}
	result.State = "auto_merge_requested"
	return result
}

// A newly created PR can temporarily have a null mergeable field while
// GitHub evaluates it. Give that evaluation a bounded opportunity to finish,
// in particular when the repository has disabled automatic merge requests.
func (a remote) mergeStatus(ctx context.Context, endpoint string) (pull, error) {
	var p pull
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return pull{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * 350 * time.Millisecond):
			}
		}
		if err := a.client.Do(ctx, "GET", endpoint, nil, &p); err != nil {
			return pull{}, err
		}
		if p.Mergeable != nil || p.State != "open" || p.AutoMerge != nil {
			break
		}
	}
	return p, nil
}
