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
	credentialSuffixes   = []string{"_API_KEY", "_AUTH_TOKEN", "_ACCESS_TOKEN", "_OAUTH_TOKEN"}
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
// _API_KEY, _AUTH_TOKEN, _ACCESS_TOKEN or _OAUTH_TOKEN (in any case): a
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
		switch {
		case LaunchSafetyName(name):
			v.fail("config_env", CodeStatus, "names a variable that changes program lookup, loading or trust")
		case CredentialSuffix(name) || contains(d.CredentialEnv, name):
			v.fail("config_env", CodeStatus, "names a credential variable, which is never passed to a status command")
		case strings.HasPrefix(name, "AGENTFLOW_"):
			v.fail("config_env", CodeStatus, "names an agentflow variable, which is never passed to a status command")
		}
	}
	switch {
	case v.layer == LayerPersonal && d.StatusSource != "":
		v.fail("status_source", CodeRule, "is only for agentflow's overlay")
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
