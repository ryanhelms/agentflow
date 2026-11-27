package harness

import (
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/harness/acpregistry"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// TestOverlayMatchesRegistry checks the agentflow overlay against the pinned
// registry copy: every ID is pinned, every entry passes Validate for its
// layer, a sign-in check cites its source, the ADR's rows are there, and on
// every platform the composed definition has a command that is a bare name.
func TestOverlayMatchesRegistry(t *testing.T) {
	cat, err := acpregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := LoadOverlay()
	if err != nil {
		t.Fatal(err)
	}
	for id, d := range overlay {
		if _, ok := cat.Get(id); !ok {
			t.Errorf("overlay ID %s is not in the pinned copy", id)
		}
		if err := harnessdef.Validate(id, d, harnessdef.LayerAgentflow); err != nil {
			t.Errorf("%v", err)
		}
		if d.StatusFrom != "" && strings.TrimSpace(d.StatusSource) == "" {
			t.Errorf("%s: a status command without status_source", id)
		}
		if d.Kind == harnessdef.KindAdapter && (d.BaseName == "" || len(d.BaseCommands) == 0) {
			t.Errorf("%s: an adapter needs base_name and base_commands", id)
		}
	}
	want := []struct {
		id, kind, command string
		args              []string
		base              string
	}{
		{"claude-acp", "adapter", "claude-agent-acp", nil, "claude"},
		{"codex-acp", "adapter", "codex-acp", nil, "codex"},
		{"gemini", "native", "gemini", []string{"--acp"}, ""},
		{"github-copilot-cli", "native", "copilot", []string{"--acp"}, ""},
		{"goose", "native", "goose", []string{"acp"}, ""},
		{"opencode", "native", "opencode", []string{"acp"}, ""},
		{"cursor", "native", "cursor-agent", []string{"acp"}, ""},
	}
	for _, platform := range []string{"darwin-aarch64", "linux-x86_64", "linux-aarch64-musl", "windows-x86_64", ""} {
		defs, problems := Compose(Layers{Catalog: cat, Overlay: overlay, Platform: platform})
		for _, p := range problems {
			if p.Layer != SourceRegistry {
				t.Errorf("%s: %+v", platform, p)
			}
		}
		got := map[string]Layered{}
		for _, d := range defs {
			got[d.ID] = d
		}
		if len(got) != len(cat.Entries) {
			t.Errorf("%s: %d definitions for %d pinned entries", platform, len(got), len(cat.Entries))
		}
		for _, w := range want {
			d := got[w.id]
			if platform == "windows-x86_64" && w.id == "cursor" {
				continue // the Windows build is cursor-agent.cmd, a shim agentflow does not start
			}
			if e, _ := cat.Get(w.id); w.id != "gemini" && w.id != "github-copilot-cli" {
				if _, _, err := acpregistry.DefaultCommand(e.Distribution, platform); err != nil {
					continue // no build for this platform: listed as not_installed
				}
			}
			if string(d.Definition.Kind) != w.kind || d.Definition.Command != w.command ||
				strings.Join(d.Definition.Args, " ") != strings.Join(w.args, " ") {
				t.Errorf("%s: %s = %+v", platform, w.id, d.Definition)
			}
			if w.base != "" && !slices.Contains(d.Definition.BaseCommands, w.base) {
				t.Errorf("%s: %s base commands = %v", platform, w.id, d.Definition.BaseCommands)
			}
			if !slices.Contains(d.Sources, SourceAgentflow) {
				t.Errorf("%s: %s sources = %v", platform, w.id, d.Sources)
			}
		}
		for _, d := range defs {
			if c := d.Definition.Command; c != "" && (!harnessdef.ValidCommandName(c) || harnessdef.RefusedLauncher(c)) {
				t.Errorf("%s: %s command %q", platform, d.ID, c)
			}
		}
	}
	for _, id := range []string{"claude-acp", "codex-acp"} {
		if overlay[id].StatusFrom != harnessdef.StatusFromBase || len(overlay[id].StatusArgs) == 0 {
			t.Errorf("%s: the verified status command is missing", id)
		}
	}
	if overlay["codex-acp"].StatusSignedOutLine != "Not logged in" || !slices.Contains(overlay["codex-acp"].ConfigEnv, "CODEX_HOME") {
		t.Errorf("codex-acp status = %+v", overlay["codex-acp"])
	}
	if !slices.Contains(overlay["claude-acp"].ConfigEnv, "CLAUDE_CONFIG_DIR") {
		t.Errorf("claude-acp config_env = %v", overlay["claude-acp"].ConfigEnv)
	}
}

func TestComposeLayers(t *testing.T) {
	cat, err := acpregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	overlay, _ := LoadOverlay()
	defs, problems := Compose(Layers{
		Catalog: cat, Overlay: overlay, Platform: "darwin-aarch64",
		Plugins: []PluginHarness{
			{Plugin: "acme", ID: "acme-agent", Definition: harnessdef.Definition{Name: "Acme", Command: "acme", Args: []string{"acp"}}},
			{Plugin: "evil", ID: "gemini", Definition: harnessdef.Definition{Command: "evil"}},
			{Plugin: "evil", ID: "evil-status", Definition: harnessdef.Definition{Command: "evil", StatusFrom: "self"}},
			{Plugin: "zeta", ID: "acme-agent", Definition: harnessdef.Definition{Command: "other"}},
		},
		Personal: map[string]harnessdef.Definition{
			"gemini":   {Args: []string{"--experimental-acp"}},
			"my-agent": {Name: "Mine", Command: "/opt/me/agent"},
			"bad":      {Command: "npx"},
			"halfway":  {Command: "ok", StatusFrom: "base"},
		},
		Disabled: []string{"kimi"},
	})
	got := map[string]Layered{}
	for _, d := range defs {
		got[d.ID] = d
	}
	if d := got["gemini"]; d.Definition.Command != "gemini" || strings.Join(d.Definition.Args, " ") != "--experimental-acp" ||
		!slices.Equal(d.Sources, []string{"registry", "agentflow", "personal"}) {
		t.Errorf("gemini = %+v", d)
	}
	if d := got["acme-agent"]; d.Definition.Command != "acme" || !slices.Equal(d.Sources, []string{"plugin:acme"}) || d.Entry != nil {
		t.Errorf("acme-agent = %+v", d)
	}
	if d := got["my-agent"]; d.Definition.Command != "/opt/me/agent" || d.Definition.Kind != harnessdef.KindNative {
		t.Errorf("my-agent = %+v", d)
	}
	for _, id := range []string{"kimi", "bad", "halfway", "evil-status"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s should be left out", id)
		}
	}
	codes := map[string]string{}
	for _, p := range problems {
		if p.Layer != SourceRegistry {
			codes[p.Layer+"/"+p.ID] = p.Code
		}
	}
	for key, want := range map[string]string{
		"plugin:evil/gemini":      harnessdef.CodeIDTaken,
		"plugin:evil/evil-status": harnessdef.CodeStatusNotAllowed,
		"plugin:zeta/acme-agent":  harnessdef.CodeIDTaken,
		"personal/bad":            harnessdef.CodeLauncher,
		"personal/halfway":        harnessdef.CodeStatus,
	} {
		if codes[key] != want {
			t.Errorf("%s: code %q, want %q (all: %v)", key, codes[key], want, codes)
		}
	}
	// A registry entry without a build for this platform is still listed,
	// with no command.
	defs, _ = Compose(Layers{Catalog: cat, Overlay: overlay, Platform: "linux-aarch64-musl"})
	for _, d := range defs {
		if d.ID == "cursor" && d.Definition.Command != "" {
			t.Errorf("cursor on musl = %+v", d)
		}
	}
}

func TestOverlayRefusesUnknownKeys(t *testing.T) {
	if _, err := decodeOverlay([]byte("gemini:\n  comand: gemini\n")); err == nil {
		t.Fatal("unknown key accepted")
	}
}
