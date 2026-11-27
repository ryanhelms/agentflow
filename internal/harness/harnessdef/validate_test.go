package harnessdef_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

func code(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	var ve *harnessdef.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error %v is not a *ValidationError", err)
	}
	return ve.Code
}

func TestValidateAcceptsOverlayEntries(t *testing.T) {
	claude := harnessdef.Definition{
		Kind:          harnessdef.KindAdapter,
		Command:       "claude-agent-acp",
		BaseName:      "Claude Code",
		BaseCommands:  []string{"claude"},
		StatusFrom:    harnessdef.StatusFromBase,
		StatusArgs:    []string{"auth", "status"},
		CredentialEnv: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
		StatusSource:  "https://code.claude.com/docs/en/cli-reference",
	}
	if err := harnessdef.Validate("claude-acp", claude, harnessdef.LayerAgentflow); err != nil {
		t.Fatal(err)
	}
	codex := harnessdef.Definition{
		Kind:                harnessdef.KindAdapter,
		Command:             "codex-acp",
		BaseCommands:        []string{"codex"},
		StatusFrom:          harnessdef.StatusFromBase,
		StatusArgs:          []string{"login", "status"},
		StatusSignedIn:      []int{0},
		StatusSignedOut:     []int{1},
		StatusSignedOutLine: "Not logged in",
		CredentialEnv:       []string{"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN"},
		ConfigEnv:           []string{"CODEX_HOME"},
		StatusSource:        "openai/codex codex-rs/cli/src/login.rs",
	}
	if err := harnessdef.Validate("codex-acp", codex, harnessdef.LayerAgentflow); err != nil {
		t.Fatal(err)
	}
	gemini := harnessdef.Definition{Command: "gemini", Args: []string{"--acp"}, MinVersion: "0.9.0"}
	for _, l := range []harnessdef.Layer{harnessdef.LayerRegistry, harnessdef.LayerAgentflow, harnessdef.LayerPlugin, harnessdef.LayerPersonal} {
		if err := harnessdef.Validate("gemini", gemini, l); err != nil {
			t.Fatalf("%s: %v", l, err)
		}
	}
}

func TestValidateRefuses(t *testing.T) {
	ok := harnessdef.Definition{Command: "my-agent"}
	cases := []struct {
		name  string
		id    string
		d     harnessdef.Definition
		layer harnessdef.Layer
		want  string
	}{
		{"bad id", "My_Agent", ok, harnessdef.LayerPersonal, harnessdef.CodeID},
		{"long id", "a" + strings.Repeat("b", 64), ok, harnessdef.LayerPersonal, harnessdef.CodeID},
		{"unknown layer", "x", ok, "repo", harnessdef.CodeRule},
		{"two-line name", "x", harnessdef.Definition{Name: "a\nb"}, harnessdef.LayerPersonal, harnessdef.CodeName},
		{"bidi name", "x", harnessdef.Definition{Name: "a\u202eb"}, harnessdef.LayerPersonal, harnessdef.CodeName},
		{"long name", "x", harnessdef.Definition{Name: strings.Repeat("n", 65)}, harnessdef.LayerPersonal, harnessdef.CodeName},
		{"bad kind", "x", harnessdef.Definition{Kind: "shell"}, harnessdef.LayerPersonal, harnessdef.CodeRule},
		{"npx", "x", harnessdef.Definition{Command: "npx"}, harnessdef.LayerPersonal, harnessdef.CodeLauncher},
		{"NPX.exe", "x", harnessdef.Definition{Command: "NPX.exe"}, harnessdef.LayerPersonal, harnessdef.CodeLauncher},
		{"absolute bash", "x", harnessdef.Definition{Command: "/bin/bash"}, harnessdef.LayerPersonal, harnessdef.CodeLauncher},
		{"absolute in plugin", "x", harnessdef.Definition{Command: "/opt/x/agent"}, harnessdef.LayerPlugin, harnessdef.CodeCommand},
		{"absolute in overlay", "x", harnessdef.Definition{Command: "/opt/x/agent"}, harnessdef.LayerAgentflow, harnessdef.CodeCommand},
		{"relative path", "x", harnessdef.Definition{Command: "bin/agent"}, harnessdef.LayerPersonal, harnessdef.CodeCommand},
		{"shell arg", "x", harnessdef.Definition{Command: "a", Args: []string{"$(id)"}}, harnessdef.LayerPersonal, harnessdef.CodeArgs},
		{"empty arg", "x", harnessdef.Definition{Command: "a", VersionArgs: []string{""}}, harnessdef.LayerPersonal, harnessdef.CodeArgs},
		{"17 args", "x", harnessdef.Definition{Command: "a", Args: make17()}, harnessdef.LayerPersonal, harnessdef.CodeArgs},
		{"base command path", "x", harnessdef.Definition{BaseCommands: []string{"../claude"}}, harnessdef.LayerPersonal, harnessdef.CodeCommand},
		{"base command launcher", "x", harnessdef.Definition{BaseCommands: []string{"node"}}, harnessdef.LayerPersonal, harnessdef.CodeLauncher},
		{"min version", "x", harnessdef.Definition{MinVersion: "1.2"}, harnessdef.LayerPersonal, harnessdef.CodeVersion},
		{"status in plugin", "x", harnessdef.Definition{Command: "a", StatusFrom: "self"}, harnessdef.LayerPlugin, harnessdef.CodeStatusNotAllowed},
		{"credential env in plugin", "x", harnessdef.Definition{CredentialEnv: []string{"X_KEY"}}, harnessdef.LayerPlugin, harnessdef.CodeStatusNotAllowed},
		{"config env in plugin", "x", harnessdef.Definition{ConfigEnv: []string{"X_HOME"}}, harnessdef.LayerPlugin, harnessdef.CodeStatusNotAllowed},
		{"status in registry", "x", harnessdef.Definition{StatusArgs: []string{"status"}}, harnessdef.LayerRegistry, harnessdef.CodeStatusNotAllowed},
		{"status_from other", "x", harnessdef.Definition{StatusFrom: "path"}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"base on native", "x", harnessdef.Definition{Kind: harnessdef.KindNative, StatusFrom: "base"}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"status args alone", "x", harnessdef.Definition{StatusArgs: []string{"status"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"status arg", "x", harnessdef.Definition{StatusFrom: "self", StatusArgs: []string{"-p", "do it"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"exit code", "x", harnessdef.Definition{StatusFrom: "self", StatusSignedIn: []int{256}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"overlapping codes", "x", harnessdef.Definition{StatusFrom: "self", StatusSignedIn: []int{0, 1}, StatusSignedOut: []int{1}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"signed-out line", "x", harnessdef.Definition{StatusFrom: "self", StatusSignedOutLine: "a\nb"}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"untrimmed line", "x", harnessdef.Definition{StatusFrom: "self", StatusSignedOutLine: " Not logged in"}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"lower env name", "x", harnessdef.Definition{CredentialEnv: []string{"api_key"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"config PATH", "x", harnessdef.Definition{ConfigEnv: []string{"PATH"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"config LD_", "x", harnessdef.Definition{ConfigEnv: []string{"LD_PRELOAD"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"config DYLD_", "x", harnessdef.Definition{ConfigEnv: []string{"DYLD_INSERT_LIBRARIES"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"config proxy", "x", harnessdef.Definition{ConfigEnv: []string{"HTTPS_PROXY"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"config credential suffix", "x", harnessdef.Definition{ConfigEnv: []string{"OPENAI_API_KEY"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"config named credential", "x", harnessdef.Definition{CredentialEnv: []string{"CODEX_SECRET"}, ConfigEnv: []string{"CODEX_SECRET"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"plugin abs version arg", "x", harnessdef.Definition{Command: "rsync", VersionArgs: []string{"/etc/passwd"}}, harnessdef.LayerPlugin, harnessdef.CodeArgs},
		{"plugin url arg", "x", harnessdef.Definition{Command: "rsync", Args: []string{"https://evil.example/x"}}, harnessdef.LayerPlugin, harnessdef.CodeArgs},
		{"plugin host arg", "x", harnessdef.Definition{Command: "rsync", Args: []string{"evil.example:x"}}, harnessdef.LayerPlugin, harnessdef.CodeArgs},
		{"plugin abs after =", "x", harnessdef.Definition{Command: "a", Args: []string{"--config=/etc/x"}}, harnessdef.LayerPlugin, harnessdef.CodeArgs},
		{"plugin abs after ,", "x", harnessdef.Definition{Command: "a", Args: []string{"--f=a,/etc/x"}}, harnessdef.LayerPlugin, harnessdef.CodeArgs},
		{"plugin drive", "x", harnessdef.Definition{Command: "a", Args: []string{"--f=C:/x"}}, harnessdef.LayerPlugin, harnessdef.CodeArgs},
		{"plugin dotdot", "x", harnessdef.Definition{Command: "a", VersionArgs: []string{"--f=a/../b"}}, harnessdef.LayerPlugin, harnessdef.CodeArgs},
		{"plugin at host", "x", harnessdef.Definition{Command: "a", Args: []string{"--to=@evil"}}, harnessdef.LayerPlugin, harnessdef.CodeArgs},
		{"config agentflow", "x", harnessdef.Definition{ConfigEnv: []string{"AGENTFLOW_HOME"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"config token", "x", harnessdef.Definition{ConfigEnv: []string{"GITHUB_TOKEN"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"config secret", "x", harnessdef.Definition{ConfigEnv: []string{"AWS_SECRET_ACCESS_KEY"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"config password", "x", harnessdef.Definition{ConfigEnv: []string{"DB_PASSWORD"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"config ssh agent", "x", harnessdef.Definition{ConfigEnv: []string{"SSH_AUTH_SOCK"}}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"config key", "x", harnessdef.Definition{ConfigEnv: []string{"OPENAI_KEY"}}, harnessdef.LayerAgentflow, harnessdef.CodeStatus},
		{"config node options overlay", "x", harnessdef.Definition{ConfigEnv: []string{"NODE_OPTIONS"}}, harnessdef.LayerAgentflow, harnessdef.CodeStatus},
		{"two-line source", "x", harnessdef.Definition{StatusFrom: "self", StatusSource: "a\nb"}, harnessdef.LayerPersonal, harnessdef.CodeStatus},
		{"overlay without source", "x", harnessdef.Definition{StatusFrom: "self"}, harnessdef.LayerAgentflow, harnessdef.CodeStatus},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := harnessdef.Validate(c.id, c.d, c.layer)
			if got := code(t, err); got != c.want {
				t.Fatalf("code = %q (%v), want %q", got, err, c.want)
			}
		})
	}
}

func TestValidationErrorNamesNoValue(t *testing.T) {
	err := harnessdef.Validate("x", harnessdef.Definition{Command: "a", Args: []string{"secret-$(value)"}}, harnessdef.LayerPersonal)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), `"x"`) || !strings.Contains(err.Error(), "args") {
		t.Fatalf("err = %v, want the ID and the field", err)
	}
}

func TestRuleHelpers(t *testing.T) {
	for _, s := range []string{"--acp", "acp", "a=b", "@scope/pkg@1.2.3", "x,y:z"} {
		if !harnessdef.ValidArg(s) {
			t.Errorf("ValidArg(%q) = false", s)
		}
	}
	for _, s := range []string{"", "a b", "$x", "a;b", "`x`", strings.Repeat("a", 129)} {
		if harnessdef.ValidArg(s) {
			t.Errorf("ValidArg(%q) = true", s)
		}
	}
	if !harnessdef.RefusedLauncher(`C:\bin\PowerShell.EXE`) || harnessdef.RefusedLauncher("gemini") {
		t.Fatal("RefusedLauncher")
	}
	for _, n := range []string{"path", "Ld_Library_Path", "dyld_x", "no_proxy", "GIT_SSH"} {
		if !harnessdef.LaunchSafetyName(n) {
			t.Errorf("LaunchSafetyName(%q) = false", n)
		}
	}
	if harnessdef.LaunchSafetyName("CODEX_HOME") {
		t.Error("CODEX_HOME is not a launch-safety name")
	}
	for _, n := range []string{"X_API_KEY", "gh_auth_token", "A_ACCESS_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "x_refresh_token"} {
		if !harnessdef.CredentialSuffix(n) {
			t.Errorf("CredentialSuffix(%q) = false", n)
		}
	}
	if harnessdef.CredentialSuffix("API_KEYS") {
		t.Error("API_KEYS has no credential suffix")
	}
}

func make17() []string {
	out := make([]string, 17)
	for i := range out {
		out[i] = "a"
	}
	return out
}

func TestCleanText(t *testing.T) {
	cases := map[string]string{
		"plain":                   "plain",
		"  two\nlines\r\n here ":  "two lines here",
		"bidi\u202e text\u200b":   "bidi text",
		"bell\x07 and \x00nul":    "bell and nul",
		"tab\tand\u2028sep":       "tab and sep",
		string([]byte{0xff, 'a'}): "a",
	}
	for in, want := range cases {
		if got := harnessdef.CleanText(in, 64); got != want {
			t.Errorf("CleanText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := harnessdef.CleanText("aé", 2); got != "a" {
		t.Errorf("cut inside a rune: %q", got)
	}
	if got := harnessdef.CleanText(strings.Repeat("x", 300), 256); len(got) != 256 {
		t.Errorf("len = %d", len(got))
	}
}

// TestPluginArgAllowlist: a plugin's args and version_args must be plain
// tokens. The security review's bypasses of the old deny rules (a path
// glued to a short or long flag, a user@host) are refused; the arguments
// the pinned registry and overlay use still pass.
func TestPluginArgAllowlist(t *testing.T) {
	for _, a := range []string{"acp", "--acp", "-V", "--short", "--experimental-skills", "--mode=acp", "-n", "v1.2.3", "x_y", "--level=0.5"} {
		if !harnessdef.PluginArgAllowed(a) || harnessdef.PluginArgRefused(a) {
			t.Errorf("PluginArgAllowed(%q) = false", a)
		}
	}
	for _, a := range []string{
		"-C/Users/x/repo", "-f/etc/passwd", "--output-file/x", "user@host",
		"/etc/passwd", "--files-from=/etc/passwd", "evil.example:loot", "https://evil.example/drop",
		"@file", "~/x", "~", "..", "a..b", "--x=..", "a\\b", "a,b", "--a=b,c", "a/b", "--x=", "=x",
		"---x", "-", "--", "-.x", "_x", "a=b=c", "a+b", "", strings.Repeat("a", 129),
	} {
		if harnessdef.PluginArgAllowed(a) || !harnessdef.PluginArgRefused(a) {
			t.Errorf("PluginArgAllowed(%q) = true", a)
		}
	}
}

// TestValidatePluginGitReset is the review's example: a plugin that names
// git with -C/path, reset, --hard is refused in the plugin layer, in args
// and in version_args. The registry layer keeps its own rule.
func TestValidatePluginGitReset(t *testing.T) {
	for _, d := range []harnessdef.Definition{
		{Command: "git", Args: []string{"-C/path", "reset", "--hard"}},
		{Command: "git", VersionArgs: []string{"-C/path", "reset", "--hard"}},
	} {
		err := harnessdef.Validate("git-acp", d, harnessdef.LayerPlugin)
		var ve *harnessdef.ValidationError
		if !errors.As(err, &ve) || ve.Code != harnessdef.CodeArgs {
			t.Fatalf("%+v: err = %v, want code args", d, err)
		}
	}
	if err := harnessdef.Validate("git-acp", harnessdef.Definition{Command: "git", Args: []string{"-C/path"}}, harnessdef.LayerRegistry); err != nil {
		t.Fatalf("registry layer: %v", err)
	}
}
