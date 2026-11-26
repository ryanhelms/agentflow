package bus

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPhase2TypesRegistered(t *testing.T) {
	for _, info := range phase2Types {
		got, ok := Lookup(info.Type)
		if !ok || got != info {
			t.Fatalf("%s: registered as %+v, %v", info.Type, got, ok)
		}
		family, _, _ := strings.Cut(string(info.Type), ".")
		for _, reserved := range ReservedFamilies {
			if family == reserved {
				t.Errorf("%s is registered but its family is still reserved", info.Type)
			}
		}
		if info.Phase != 2 {
			t.Errorf("%s: phase %d", info.Type, info.Phase)
		}
	}
}

func TestValidateGlobalRule(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	repo := RepoID("0123456789abcdef")
	cases := []struct {
		name string
		e    Event
		ok   bool
	}{
		{"personal config in global", Event{Type: TypeConfigLayerChanged, Repo: GlobalRepo}, true},
		{"repo config in a repository", Event{Type: TypeConfigLayerChanged, Repo: repo, Worktree: "main"}, true},
		{"user client settings in global", Event{Type: TypeClientSettingsChanged, Repo: GlobalRepo}, true},
		{"git event in global", Event{Type: TypeGitRefsChanged, Repo: GlobalRepo}, false},
		{"approval in global", Event{Type: TypeApprovalRequested, Repo: GlobalRepo}, false},
		{"global with a worktree", Event{Type: TypeConfigLayerChanged, Repo: GlobalRepo, Worktree: "main"}, false},
		{"approval with a worktree", Event{Type: TypeApprovalRequested, Repo: repo, Worktree: "main"}, false},
		{"commit without a worktree", Event{Type: TypeCommitCreated, Repo: repo}, false},
		{"commit in a worktree", Event{Type: TypeCommitCreated, Repo: repo, Worktree: "main"}, true},
	}
	for _, c := range cases {
		c.e.Source = "test"
		err := c.e.Normalize(now).Validate()
		if c.ok != (err == nil) {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if err != nil && !errors.Is(err, ErrInvalidEnvelope) {
			t.Errorf("%s: %v is not ErrInvalidEnvelope", c.name, err)
		}
	}
}

func TestCombineCI(t *testing.T) {
	cases := []struct {
		c    CICounts
		want CIState
	}{
		{CICounts{}, CINone},
		{CICounts{Total: 3, Success: 3}, CISuccess},
		{CICounts{Total: 3, Success: 2, Neutral: 1}, CISuccess},
		{CICounts{Total: 3, Success: 1, Pending: 2}, CIPending},
		{CICounts{Total: 3, Failure: 1, Pending: 2}, CIFailure},
	}
	for _, c := range cases {
		if got := CombineCI(c.c); got != c.want {
			t.Errorf("CombineCI(%+v) = %q, want %q", c.c, got, c.want)
		}
	}
}
