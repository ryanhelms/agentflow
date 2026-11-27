package acpregistry_test

import (
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/harness/acpregistry"
)

// TestPinnedRegistry decodes every pinned file strictly, checks SOURCE.json
// against the files and fails when an agent breaks a registry rule.
func TestPinnedRegistry(t *testing.T) {
	c, err := acpregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(c.Source.Commit) {
		t.Fatalf("SOURCE.json commit = %q", c.Source.Commit)
	}
	if c.Source.Repository != "https://github.com/agentclientprotocol/registry" || c.Source.CommittedAt.IsZero() {
		t.Fatalf("SOURCE.json = %+v", c.Source)
	}
	names, _ := fs.Glob(acpregistry.FS(), "v1/agents/*.json")
	if c.Source.Agents != len(names) || len(c.Entries) != len(names) {
		t.Fatalf("SOURCE.json says %d agents; %d files, %d entries", c.Source.Agents, len(names), len(c.Entries))
	}
	for _, p := range c.Problems {
		t.Errorf("%s: %s: %s", p.ID, p.Field, p.Message)
	}
	var quarantine map[string]string
	b, _ := fs.ReadFile(acpregistry.FS(), "v1/quarantine.json")
	if err := json.Unmarshal(b, &quarantine); err != nil {
		t.Fatal(err)
	}
	for id := range quarantine {
		e, ok := c.Get(id)
		if !ok {
			t.Errorf("quarantine.json names %s, which is not in the copy", id)
			continue
		}
		if !e.Quarantined || e.QuarantineReason == "" {
			t.Errorf("%s: quarantined %v, reason %q", id, e.Quarantined, e.QuarantineReason)
		}
	}
	for _, e := range c.Entries {
		if e.Name == "" || e.Description == "" || e.Version == "" {
			t.Errorf("%s: name, description or version missing after the rules", e.ID)
		}
		if len(e.Name) > acpregistry.MaxNameLen || len(e.Description) > acpregistry.MaxDescriptionLen {
			t.Errorf("%s: display text not clamped", e.ID)
		}
		d := e.Distribution
		if len(d.Binary) == 0 && d.NPX == nil && d.UVX == nil {
			t.Errorf("%s: no distribution left", e.ID)
		}
	}
	for _, f := range []string{"v1/LICENSE", "v1/agent.schema.json"} {
		if b, err := fs.ReadFile(acpregistry.FS(), f); err != nil || len(b) == 0 {
			t.Errorf("%s: %v", f, err)
		}
	}
	for _, name := range names {
		if strings.Contains(path.Base(name), "icon") {
			t.Errorf("%s: icons are never copied", name)
		}
	}
}

func TestCheckDropsBadValues(t *testing.T) {
	a := acpregistry.Agent{
		ID: "x", Name: "Evil‮ name\nwith lines", Version: "1.2.3",
		Description: strings.Repeat("é", 200), Repository: "http://example.com", Website: "https://user@example.com",
		Distribution: acpregistry.Distribution{
			Binary: map[string]acpregistry.BinaryTarget{
				"darwin-aarch64": {Archive: "https://example.com/a.tgz", SHA256: "XYZ", Cmd: "./x", Args: []string{"$(id)"}},
				"plan9-amd64":    {Archive: "https://example.com/a.tgz", Cmd: "./x"},
				"linux-x86_64":   {Archive: "ftp://example.com/a.tgz", Cmd: "./x"},
			},
			NPX: &acpregistry.Package{Package: "evil; rm -rf /", Args: []string{"--acp"}},
			UVX: &acpregistry.Package{Package: "tool==1.0", Env: map[string]string{"A": "b"}},
		},
	}
	e, problems := acpregistry.Check(a)
	if e.Name != "Evil name with lines" {
		t.Errorf("name = %q", e.Name)
	}
	if len(e.Description) > 256 || !strings.HasPrefix(e.Description, "é") || !utf8Valid(e.Description) {
		t.Errorf("description = %d bytes", len(e.Description))
	}
	if e.Repository != "" || e.Website != "" {
		t.Errorf("urls = %q %q", e.Repository, e.Website)
	}
	bin := e.Distribution.Binary
	if len(bin) != 1 || bin["darwin-aarch64"].SHA256 != "" || bin["darwin-aarch64"].Args != nil {
		t.Errorf("binary = %+v", bin)
	}
	if e.Distribution.NPX != nil || e.Distribution.UVX == nil || !e.EnvIgnored {
		t.Errorf("packages = %+v %+v, env ignored %v", e.Distribution.NPX, e.Distribution.UVX, e.EnvIgnored)
	}
	if len(problems) < 6 {
		t.Errorf("problems = %+v", problems)
	}
	for _, p := range problems {
		if strings.Contains(p.Message, "rm -rf") || strings.Contains(p.Message, "$(id)") {
			t.Errorf("a problem quotes the value: %+v", p)
		}
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "") == s }

func TestDefaultCommand(t *testing.T) {
	c, err := acpregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id, platform, command string
		args                  []string
	}{
		{"cursor", "darwin-aarch64", "cursor-agent", []string{"acp"}},
		{"cursor", "windows-x86_64", "cursor-agent.cmd", []string{"acp"}},
		{"codex-acp", "darwin-aarch64", "codex-acp", nil},
		{"gemini", "linux-x86_64", "gemini-cli", []string{"--acp"}},
		{"claude-acp", "darwin-aarch64", "claude-agent-acp", nil},
		{"claude-acp", "windows-x86_64", "claude-agent-acp", nil},
		{"goose", "linux-x86_64", "goose", []string{"acp"}},
		{"fast-agent", "darwin-aarch64", "fast-agent-acp", []string{"-x"}},
	}
	for _, tc := range cases {
		e, ok := c.Get(tc.id)
		if !ok {
			t.Fatalf("%s not pinned", tc.id)
		}
		cmd, args, err := acpregistry.DefaultCommand(e.Distribution, tc.platform)
		if err != nil || cmd != tc.command || strings.Join(args, " ") != strings.Join(tc.args, " ") {
			t.Errorf("%s on %s = %q %v %v, want %q %v", tc.id, tc.platform, cmd, args, err, tc.command, tc.args)
		}
	}
}

func TestPlatformAndPackages(t *testing.T) {
	if got := acpregistry.Platform("linux", "amd64", true); got != "linux-x86_64-musl" {
		t.Error(got)
	}
	if got := acpregistry.Platform("darwin", "arm64", false); got != "darwin-aarch64" {
		t.Error(got)
	}
	if got := acpregistry.Platform("freebsd", "amd64", false); got != "" {
		t.Error(got)
	}
	for in, want := range map[string][2]string{
		"@agentclientprotocol/codex-acp@2.12.1": {"@agentclientprotocol/codex-acp", "2.12.1"},
		"fast-agent-acp==0.10.66":               {"fast-agent-acp", "0.10.66"},
		"minion-code@0.1.44":                    {"minion-code", "0.1.44"},
		"@scope/name":                           {"@scope/name", ""},
	} {
		if n, v := acpregistry.PackageVersion(in); n != want[0] || v != want[1] {
			t.Errorf("PackageVersion(%q) = %q %q", in, n, v)
		}
	}
	for u, want := range map[string]bool{"https://a.b/c": true, "http://a.b": false, "https://": false, "https://u@h/x": false, "https://a b": false} {
		if acpregistry.HTTPS(u) != want {
			t.Errorf("HTTPS(%q) != %v", u, want)
		}
	}
}
