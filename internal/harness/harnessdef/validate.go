package harnessdef

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	idRule      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	commandRule = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	argRule     = regexp.MustCompile(`^[A-Za-z0-9._=:/@+,-]{1,128}$`)
	versionRule = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	envNameRule = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
)

// launchers are the programs phase 6 never starts: package runners,
// interpreters, shells and downloaders. They are compared without case and
// without ".exe".
var launchers = map[string]bool{
	"npx": true, "npm": true, "pnpm": true, "pnpx": true, "yarn": true,
	"bun": true, "bunx": true, "deno": true, "uv": true, "uvx": true,
	"pip": true, "pip3": true, "pipx": true, "python": true, "python3": true,
	"node": true, "sh": true, "bash": true, "zsh": true, "fish": true,
	"env": true, "sudo": true, "doas": true, "curl": true, "wget": true,
	"cmd": true, "powershell": true, "pwsh": true,
}

// launchSafetyNames change program lookup, loading or trust. They are
// compared without case; launchSafetyPrefixes match the start of a name.
var (
	launchSafetyNames = map[string]bool{
		"PATH": true, "NODE_OPTIONS": true, "NODE_PATH": true, "PYTHONPATH": true,
		"PYTHONSTARTUP": true, "BASH_ENV": true, "ENV": true, "HTTP_PROXY": true,
		"HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "SSL_CERT_FILE": true,
		"SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true, "REQUESTS_CA_BUNDLE": true,
		"GIT_SSH_COMMAND": true, "GIT_SSH": true,
	}
	launchSafetyPrefixes = []string{"LD_", "DYLD_"}
	credentialSuffixes   = []string{"_API_KEY", "_AUTH_TOKEN", "_ACCESS_TOKEN", "_OAUTH_TOKEN", "_REFRESH_TOKEN"}
)

// ValidationError is the first problem Validate found. Code is one of the
// Code* constants; Field is the YAML name of the field ("" for the ID).
// Message is agentflow's own text and never quotes a value.
type ValidationError struct {
	ID      string
	Layer   Layer
	Field   string
	Code    string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("harness %q (%s): %s", e.ID, e.Layer, e.Message)
	}
	return fmt.Sprintf("harness %q (%s): %s: %s", e.ID, e.Layer, e.Field, e.Message)
}

// ValidArg reports whether s passes the registry arg rule
// (^[A-Za-z0-9._=:/@+,-]{1,128}$).
func ValidArg(s string) bool { return argRule.MatchString(s) }

// ValidCommandName reports whether s is a bare command name
// (^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$). It does not check the launcher list.
func ValidCommandName(s string) bool { return commandRule.MatchString(s) }

// RefusedLauncher reports whether the base name of command, without case and
// without ".exe", is a package runner, interpreter, shell or downloader that
// phase 6 never starts.
func RefusedLauncher(command string) bool {
	base := command
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	base = strings.TrimSuffix(base, ".exe")
	return launchers[base]
}

// LaunchSafetyName reports whether the environment variable name changes
// program lookup, loading or trust (PATH, LD_*, DYLD_*, NODE_OPTIONS, the
// proxy and CA bundle variables and so on), in any case. Such a name is
// never passed to a status command and never shown in a sign-in command.
func LaunchSafetyName(name string) bool {
	up := strings.ToUpper(name)
	if launchSafetyNames[up] {
		return true
	}
	for _, p := range launchSafetyPrefixes {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	return false
}

// CredentialSuffix reports whether the environment variable name ends in
// _API_KEY, _AUTH_TOKEN, _ACCESS_TOKEN, _OAUTH_TOKEN or _REFRESH_TOKEN (in any case): a
// variable that could sign any harness in.
func CredentialSuffix(name string) bool {
	up := strings.ToUpper(name)
	for _, s := range credentialSuffixes {
		if strings.HasSuffix(up, s) {
			return true
		}
	}
	return false
}

// ConfigEnvRefused reports why name may not be a config_env name, or "".
// config_env names are passed, with their values, to a status command that
// runs with the person's real home, so only directory variables belong
// there: never a name that changes program lookup, loading or trust, never
// a credential-like name (one with TOKEN, SECRET, PASSWORD, PASSWD,
// CREDENTIAL or a _KEY ending, the credential suffixes, SSH_AUTH_SOCK, or a
// name in credentialEnv), and never an agentflow variable.
func ConfigEnvRefused(name string, credentialEnv []string) string {
	up := strings.ToUpper(name)
	switch {
	case LaunchSafetyName(name):
		return "names a variable that changes program lookup, loading or trust"
	case CredentialSuffix(name) || contains(credentialEnv, name) || credentialLike(up):
		return "names a credential variable, which is never passed to a status command"
	case strings.HasPrefix(up, "AGENTFLOW_") || strings.HasPrefix(up, "DEVFLOW_"):
		return "names an agentflow variable, which is never passed to a status command"
	}
	return ""
}

func credentialLike(up string) bool {
	if up == "SSH_AUTH_SOCK" || strings.HasSuffix(up, "_KEY") || up == "KEY" {
		return true
	}
	for _, w := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL"} {
		if strings.Contains(up, w) {
			return true
		}
	}
	return false
}

// Validate checks d for the layer it comes from and returns the first
// problem as a *ValidationError, or nil. A definition that fails is left out
// whole by the caller, never half-applied. Rules that need the other layers
// (a plugin ID equal to a registry ID, status_from "base" when the kind comes
// from a lower layer, a definition left with no command) are the registry's.
func Validate(id string, d Definition, layer Layer) error {
	v := validator{id: id, layer: layer}
	v.check(d)
	if v.err == nil {
		return nil
	}
	return v.err
}

type validator struct {
	id    string
	layer Layer
	err   *ValidationError
}

func (v *validator) fail(field, code, msg string) {
	if v.err == nil {
		v.err = &ValidationError{ID: v.id, Layer: v.layer, Field: field, Code: code, Message: msg}
	}
}

func (v *validator) check(d Definition) {
	switch v.layer {
	case LayerRegistry, LayerAgentflow, LayerPlugin, LayerPersonal:
	default:
		v.fail("", CodeRule, "unknown definition layer")
		return
	}
	if !idRule.MatchString(v.id) {
		v.fail("", CodeID, "the ID must match ^[a-z][a-z0-9-]{0,63}$")
		return
	}
	if d.Name != "" && !oneLine(d.Name, MaxNameLen) {
		v.fail("name", CodeName, "must be one line of at most 64 bytes")
	}
	switch d.Kind {
	case "", KindNative, KindAdapter:
	default:
		v.fail("kind", CodeRule, `must be "native" or "adapter"`)
	}
	if d.Command != "" {
		v.command("command", d.Command, v.layer == LayerPersonal)
	}
	v.args("args", d.Args)
	v.args("version_args", d.VersionArgs)
	if v.layer == LayerPlugin {
		v.pluginArgs("args", d.Args)
		v.pluginArgs("version_args", d.VersionArgs)
	}
	if d.BaseName != "" && !oneLine(d.BaseName, MaxNameLen) {
		v.fail("base_name", CodeName, "must be one line of at most 64 bytes")
	}
	v.args("base_commands", d.BaseCommands)
	for _, c := range d.BaseCommands {
		v.command("base_commands", c, false)
	}
	if d.MinVersion != "" && !versionRule.MatchString(d.MinVersion) {
		v.fail("min_version", CodeVersion, "must be a version X.Y.Z")
	}
	v.status(d)
}

// command checks a bare command name, or an absolute path when abs is
// allowed. The launcher list applies to both.
func (v *validator) command(field, c string, abs bool) {
	switch {
	case commandRule.MatchString(c):
	case abs && filepath.IsAbs(c) && oneLine(c, 4096):
	case abs:
		v.fail(field, CodeCommand, "must be a command name or an absolute path")
		return
	default:
		v.fail(field, CodeCommand, "must be a command name matching ^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$")
		return
	}
	if RefusedLauncher(c) {
		v.fail(field, CodeLauncher, "names a package runner, interpreter, shell or downloader, which agentflow never starts")
	}
}

func (v *validator) args(field string, args []string) {
	if len(args) > MaxArgs {
		v.fail(field, CodeArgs, "has more than 16 items")
		return
	}
	for _, a := range args {
		if !argRule.MatchString(a) {
			v.fail(field, CodeArgs, "each item must match ^[A-Za-z0-9._=:/@+,-]{1,128}$")
			return
		}
	}
}

// pluginArgs refuses, in a plugin's args and version_args, any value that
// could point the program at a file or a host: an absolute path (also after
// "=" or ",", and Windows drive forms), "..", "~", and anything with a ":"
// (URLs, host:path, drive letters). A plugin chooses these values, not the
// person, and the program they reach may be any program on PATH.
func (v *validator) pluginArgs(field string, args []string) {
	for _, a := range args {
		if PluginArgRefused(a) {
			v.fail(field, CodeArgs, "a plugin's arguments may not hold a path, a host or a URL (no leading /, no .., ~ or :)")
			return
		}
	}
}

// PluginArgRefused reports whether a plugin-layer argument holds a path,
// a host or a URL.
func PluginArgRefused(a string) bool {
	if strings.ContainsAny(a, ":~\\") || strings.Contains(a, "..") {
		return true
	}
	for _, part := range strings.FieldsFunc(a, func(r rune) bool { return r == '=' || r == ',' }) {
		if strings.HasPrefix(part, "/") || strings.HasPrefix(part, "@") {
			return true
		}
	}
	return false
}

func hasStatus(d Definition) bool {
	return d.StatusFrom != "" || len(d.StatusArgs) > 0 || len(d.StatusSignedIn) > 0 ||
		len(d.StatusSignedOut) > 0 || d.StatusSignedOutLine != "" || len(d.CredentialEnv) > 0 ||
		len(d.ConfigEnv) > 0 || d.StatusSource != ""
}

func (v *validator) status(d Definition) {
	if !hasStatus(d) {
		return
	}
	if v.layer == LayerRegistry || v.layer == LayerPlugin {
		v.fail("status_from", CodeStatusNotAllowed, "only agentflow's overlay and your own overrides may set a sign-in check")
		return
	}
	switch d.StatusFrom {
	case "":
		if len(d.StatusArgs) > 0 || len(d.StatusSignedIn) > 0 || len(d.StatusSignedOut) > 0 || d.StatusSignedOutLine != "" {
			v.fail("status_from", CodeStatus, "is needed for the other sign-in check fields")
		}
	case StatusFromSelf:
	case StatusFromBase:
		if d.Kind != "" && d.Kind != KindAdapter {
			v.fail("status_from", CodeStatus, `"base" is only for kind adapter`)
		}
	default:
		v.fail("status_from", CodeStatus, `must be "self" or "base"`)
	}
	if len(d.StatusArgs) > MaxArgs {
		v.fail("status_args", CodeStatus, "has more than 16 items")
	}
	for _, a := range d.StatusArgs {
		if !argRule.MatchString(a) {
			v.fail("status_args", CodeStatus, "each item must match ^[A-Za-z0-9._=:/@+,-]{1,128}$")
			break
		}
	}
	v.exitCodes("status_signed_in", d.StatusSignedIn)
	v.exitCodes("status_signed_out", d.StatusSignedOut)
	for _, in := range d.StatusSignedIn {
		for _, out := range d.StatusSignedOut {
			if in == out {
				v.fail("status_signed_out", CodeStatus, "an exit code cannot mean both signed in and signed out")
			}
		}
	}
	if d.StatusSignedOutLine != "" &&
		(!oneLine(d.StatusSignedOutLine, MaxSignedOutLine) || strings.TrimSpace(d.StatusSignedOutLine) != d.StatusSignedOutLine) {
		v.fail("status_signed_out_line", CodeStatus, "must be one trimmed line of at most 128 bytes")
	}
	v.envNames("credential_env", d.CredentialEnv)
	v.envNames("config_env", d.ConfigEnv)
	for _, name := range d.ConfigEnv {
		if why := ConfigEnvRefused(name, d.CredentialEnv); why != "" {
			v.fail("config_env", CodeStatus, why)
		}
	}
	switch {
	case d.StatusSource != "" && !oneLine(d.StatusSource, MaxStatusSourceLen):
		v.fail("status_source", CodeStatus, "must be one line of at most 512 bytes")
	case v.layer == LayerAgentflow && d.StatusFrom != "" && strings.TrimSpace(d.StatusSource) == "":
		v.fail("status_source", CodeStatus, "an overlay sign-in check must cite its source")
	}
}

func (v *validator) exitCodes(field string, codes []int) {
	for _, c := range codes {
		if c < 0 || c > MaxStatusExitCode {
			v.fail(field, CodeStatus, "exit codes must be from 0 to 255")
			return
		}
	}
}

func (v *validator) envNames(field string, names []string) {
	for _, n := range names {
		if !envNameRule.MatchString(n) {
			v.fail(field, CodeStatus, "names must match ^[A-Z_][A-Z0-9_]{0,63}$")
			return
		}
	}
}

// CleanText makes untrusted display text safe to show: line breaks and tabs
// become spaces, other control and bidirectional formatting characters and
// invalid UTF-8 are removed, runs of spaces are joined, the ends are trimmed,
// and the result is cut to at most max bytes at a character boundary.
func CleanText(s string, max int) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToValidUTF8(s, "") {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == '\u2028' || r == '\u2029' || unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || unicode.Is(unicode.Cf, r):
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = strings.TrimSpace(out[:cut])
	}
	return out
}

// oneLine reports whether s is valid UTF-8 of at most max bytes with no
// control, line-break or bidirectional formatting characters.
func oneLine(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == ' ' || r == ' ' {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
