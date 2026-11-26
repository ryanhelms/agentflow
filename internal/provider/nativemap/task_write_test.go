package nativemap

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ryanhelms/agentflow/internal/provider"
)

func TestAsanaNativeWriteReceipts(t *testing.T) {
	ref := provider.Ref{Instance: "asana", Scope: "10", ID: "101"}
	complete, err := AsanaComplete(ref)
	if err != nil {
		t.Fatal(err)
	}
	comment, err := AsanaComment(ref, "ready")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		write TaskWrite
		reply string
		ok    bool
	}{
		{"exact completion", complete, `{"succeeded":[{"task_id":"101","completed":true}]}`, true},
		{"mixed outcome", complete, `{"succeeded":[{"gid":"101"}],"failed":[{}]}`, false},
		{"wrong completion", complete, `{"succeeded":[{"gid":"102"}]}`, false},
		{"wrong membership", complete, `{"succeeded":[{"gid":"101","memberships":[{"project":{"gid":"20"}}]}]}`, false},
		{"exact story", comment, `{"data":{"gid":"202","anchor_gid":"101","text":"ready","resource_subtype":"comment_added"}}`, true},
		{"wrong story body", comment, `{"gid":"202","anchor_gid":"101","text":"revised"}`, false},
		{"conflicting story target", comment, `{"gid":"202","anchor_gid":"101","target":{"gid":"102"},"text":"ready"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.write.Decode(json.RawMessage(tc.reply))
			if tc.ok {
				if err != nil || got.Ref != ref || got.Status != provider.Applied {
					t.Fatalf("receipt %+v, %v", got, err)
				}
			} else if !errors.Is(err, ErrInvalidResult) {
				t.Fatalf("expected invalid receipt, got %+v, %v", got, err)
			}
		})
	}
}

func TestGitHubNativeCommentReceipts(t *testing.T) {
	ref := provider.Ref{Instance: "github-issues", Scope: "org/repo", ID: "42"}
	write, err := GitHubIssueComment(ref, "ready")
	if err != nil {
		t.Fatal(err)
	}
	for _, reply := range []string{
		`{"id":"77","url":"https://github.com/org/repo/issues/42#issuecomment-77"}`,
		`{"single":{"id":"77","html_url":"https://github.com/Org/Repo/issues/42#issuecomment-77"}}`,
	} {
		got, err := write.Decode(json.RawMessage(reply))
		if err != nil || got.Ref != ref || got.Status != provider.Applied {
			t.Errorf("receipt %+v, %v", got, err)
		}
	}
	for _, reply := range []string{
		`{"id":"77","url":"https://github.com/org/repo/pull/42#issuecomment-77"}`,
		`{"id":"77","url":"https://github.com/org/other/issues/42#issuecomment-77"}`,
		`{"id":"77","url":"https://github.com/org/repo/issues/43#issuecomment-77"}`,
		`{"id":"77","url":"https://github.com/org/repo/issues/42#issuecomment-78"}`,
	} {
		if got, err := write.Decode(json.RawMessage(reply)); !errors.Is(err, ErrInvalidResult) {
			t.Errorf("expected invalid receipt, got %+v, %v", got, err)
		}
	}
}
