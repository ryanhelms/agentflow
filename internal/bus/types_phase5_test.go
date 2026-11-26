package bus

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPhase5TypesRegistered(t *testing.T) {
	for _, info := range phase5Types {
		got, ok := Lookup(info.Type)
		if !ok || got != info {
			t.Fatalf("%s: registered as %+v, %v", info.Type, got, ok)
		}
		if !strings.HasPrefix(string(info.Type), "plugin.") || info.Phase != 5 {
			t.Errorf("%s: %+v", info.Type, info)
		}
		if Audited(info.Type) {
			t.Errorf("%s is audited by the tap", info.Type)
		}
	}
	for _, reserved := range ReservedFamilies {
		if reserved == "plugin" {
			t.Error("plugin is registered but still reserved")
		}
	}
}

func TestPluginEventScope(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	repo := RepoID("0123456789abcdef")
	cases := []struct {
		name string
		e    Event
		ok   bool
	}{
		{"lifecycle in global", Event{Type: TypePluginLifecycleChanged, Repo: GlobalRepo}, true},
		{"lifecycle in a repository", Event{Type: TypePluginLifecycleChanged, Repo: repo}, false},
		{"delivery failure in global", Event{Type: TypePluginDeliveryFailed, Repo: GlobalRepo}, true},
		{"annotation in a worktree", Event{Type: TypePluginAnnotationAdded, Repo: repo, Worktree: "main"}, true},
		{"annotation repository-wide", Event{Type: TypePluginAnnotationAdded, Repo: repo}, true},
		{"annotation in global", Event{Type: TypePluginAnnotationAdded, Repo: GlobalRepo}, false},
	}
	for _, c := range cases {
		c.e.Source = "plugin:test"
		err := c.e.Normalize(now).Validate()
		if c.ok != (err == nil) {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if err != nil && !errors.Is(err, ErrInvalidEnvelope) {
			t.Errorf("%s: %v is not ErrInvalidEnvelope", c.name, err)
		}
	}
}

func TestPluginAnnotationClamp(t *testing.T) {
	p := PluginAnnotationAdded{Plugin: "p", Text: "first line\nsecond " + strings.Repeat("é", 300), Level: AnnotationInfo}.Clamp()
	if p.Text != "first line" {
		t.Errorf("text = %q", p.Text)
	}
	long := PluginAnnotationAdded{Text: strings.Repeat("é", 300)}.Clamp()
	if len(long.Text) > MaxPayloadText {
		t.Errorf("text is %d bytes", len(long.Text))
	}
}
