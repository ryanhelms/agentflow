package app

import (
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
)

func TestNeedsReload(t *testing.T) {
	const repo, other = bus.RepoID("0123456789abcdef"), bus.RepoID("fedcba9876543210")
	ev := func(r bus.RepoID, w bus.WorktreeID, typ bus.Type, payload string) bus.Event {
		return bus.Event{Type: typ, Repo: r, Worktree: w, Payload: []byte(payload)}
	}
	cases := []struct {
		name string
		e    bus.Event
		want bool
	}{
		{"own repo layer", ev(repo, "main", bus.TypeConfigLayerChanged, `{"layer":"repo"}`), true},
		{"other worktree", ev(repo, "feature-0badc0de", bus.TypeConfigLayerChanged, `{"layer":"repo"}`), false},
		{"other repository", ev(other, "main", bus.TypeConfigLayerChanged, `{"layer":"repo"}`), false},
		{"personal layer", ev(bus.GlobalRepo, "", bus.TypeConfigLayerChanged, `{"layer":"personal"}`), true},
		{"personal layer outside global", ev(repo, "main", bus.TypeConfigLayerChanged, `{"layer":"personal"}`), false},
		{"client settings", ev(bus.GlobalRepo, "", bus.TypeClientSettingsChanged, `{"client":"claude"}`), false},
		{"git event", ev(repo, "main", bus.TypeGitHeadChanged, `{}`), false},
	}
	for _, c := range cases {
		if got := NeedsReload(c.e, repo, "main"); got != c.want {
			t.Errorf("%s: NeedsReload = %v", c.name, got)
		}
	}
	for _, topic := range ReloadTopics(repo, "main") {
		if !topic.Types.Match(bus.TypeConfigLayerChanged) || topic.Types.Match(bus.TypeGitHeadChanged) {
			t.Errorf("topic %v", topic)
		}
	}
}
