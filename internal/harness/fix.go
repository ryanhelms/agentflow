package harness

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/harness/acpregistry"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// Fix text ("Phase 6 fix text") is agentflow's own template, chosen by state
// and code. Values come only from validated fields: a command name, a
// package name, an https URL, a SHA-256, a version, a configuration key.
// Registry descriptions and agent text never become fix text. Commands are
// quoted for POSIX shells (PowerShell on Windows), shown and never run.

// Fix commands agentflow shows.
const (
	ProbeCommand   = "agentflow ai harnesses --probe"
	TimeoutCommand = "agentflow config set ai.harnesses.probe_timeout=15s"
)

// loginCommands are the sign-in commands for harnesses with a verified
// status command, from the same sources as their overlay entries: Claude
// Code CLI reference (claude auth login) and openai/codex
// codex-rs/cli/src/main.rs (codex login).
var loginCommands = map[string]string{
	"claude-acp": "claude auth login",
	"codex-acp":  "codex login",
}

// FixInput is what FixFor chooses from.
type FixInput struct {
	Harness Harness
	// Definition is the layered definition; Entry the registry entry, if any.
	Definition harnessdef.Definition
	Entry      *acpregistry.Entry
	// Platform is the binary distribution key; ProbeTimeout the timeout in
	// force; Windows quotes commands for PowerShell.
	Platform     string
	ProbeTimeout time.Duration
	Windows      bool
}

// FixFor returns what the person can do about the harness's state, or nil.
func FixFor(in FixInput) *Fix {
	h := in.Harness
	name := h.Name
	if name == "" {
		name = h.ID
	}
	q := func(s string) string { return quoteArg(s, in.Windows) }
	cmdLine := strings.TrimSpace(strings.Join(append([]string{h.Command}, h.Args...), " "))
	repo := ""
	if in.Entry != nil {
		repo = in.Entry.Repository
	}

	switch h.State {
	case StateAdapterAvailable, StateNotInstalled:
		return installFix(in, name, q)
	case StateIncompatible:
		install := installCommand(in, q)
		switch h.Code {
		case CodeProtocolVersion:
			n := "another version"
			if h.ACP != nil {
				n = fmt.Sprintf("version %d", h.ACP.ProtocolVersion)
			}
			return &Fix{Text: fmt.Sprintf("%s speaks ACP %s; agentflow needs version 1. Update %s.", name, n, name), Command: install, URL: repo}
		case CodeProtocol:
			return &Fix{Text: fmt.Sprintf("`%s` did not answer like an ACP agent. Check the arguments in Harness overrides on the Settings page.", cmdLine), URL: repo}
		case CodeVersionTooOld:
			return &Fix{Text: fmt.Sprintf("%s %s is older than %s. Update it.", name, h.Version, in.Definition.MinVersion), Command: install, URL: repo}
		}
	case StateNeedsAuth:
		return &Fix{Text: fmt.Sprintf("Sign in to %s, then probe again.", name), Command: loginCommands[h.ID]}
	}

	switch h.Probe.Status {
	case ProbeNone:
		return &Fix{Text: "Not checked yet. Probe it to confirm it speaks ACP.", Command: ProbeCommand}
	case ProbeFailed, ProbeCached:
		switch h.Probe.Code {
		case CodeTimeout:
			return &Fix{Text: fmt.Sprintf("`%s` did not answer within %s. Probe again, or allow more time.", h.Command, shortTimeout(in.ProbeTimeout)), Command: TimeoutCommand}
		case CodeExited:
			return &Fix{Text: fmt.Sprintf("`%s` exited before it answered. Check that this `%s` is %s and supports ACP. If it needs other arguments, set them in Harness overrides on the Settings page.", cmdLine, h.Command, name), URL: repo}
		case CodeLaunch:
			return &Fix{Text: fmt.Sprintf("agentflow could not start `%s`. Check that the file is executable.", h.Path)}
		}
	case ProbeRefused:
		switch h.Probe.Code {
		case CodeNeedsPerson:
			return &Fix{Text: "Automatic probes are off. Probe it yourself, or turn them on.", Command: ProbeCommand}
		case CodeShim:
			return &Fix{Text: fmt.Sprintf("`%s` is a Windows command script, which agentflow does not start. Point the override at the program's `.exe`.", h.Path)}
		case CodePluginLaunch:
			return &Fix{Text: "A plugin chose the command or its arguments, so agentflow never probes it automatically, for an agent or in a probe of every harness. Probe it by name if you trust it.", Command: ProbeCommand + " --id " + h.ID}
		}
	}
	if h.Auth.Status == SignInUnknown && h.Auth.Code == CodeEnvCredential {
		return &Fix{Text: "You may be signed in with an environment variable; agentflow cannot check that."}
	}
	return nil
}

// installFix is the adapter_available and not_installed text, by
// distribution kind (Strategy: npx, uvx, binary for this platform).
func installFix(in FixInput, name string, q func(string) string) *Fix {
	h := in.Harness
	subject := name + " is not installed."
	if h.State == StateAdapterAvailable {
		base := name
		if h.Base != nil && h.Base.Name != "" {
			base = h.Base.Name
		}
		subject = base + " is installed, but its ACP adapter is not."
	}
	url := ""
	var d acpregistry.Distribution
	if in.Entry != nil {
		url, d = in.Entry.Repository, in.Entry.Distribution
		if url == "" {
			url = in.Entry.Website
		}
	}
	switch {
	case d.NPX != nil:
		return &Fix{Text: subject + " Install it yourself; agentflow never installs it.", Command: "npm install -g " + q(d.NPX.Package), URL: url}
	case d.UVX != nil:
		return &Fix{Text: subject + " Install it yourself; agentflow never installs it.", Command: "uv tool install " + q(uvSpec(d.UVX.Package)), URL: url}
	}
	if t, ok := d.Binary[in.Platform]; ok {
		text := subject + fmt.Sprintf(" Download it, check its SHA-256, and put `%s` on your PATH.", binaryName(t.Cmd))
		if t.SHA256 != "" {
			text += " SHA-256: " + t.SHA256 + "."
		}
		return &Fix{Text: text, URL: t.Archive}
	}
	command := in.Definition.Command
	if command == "" {
		return &Fix{Text: subject, URL: url}
	}
	if h.State == StateAdapterAvailable {
		base := name
		if h.Base != nil && h.Base.Name != "" {
			base = h.Base.Name
		}
		return &Fix{Text: fmt.Sprintf("%s is installed, but `%s` is not on your PATH.", base, command), URL: url}
	}
	return &Fix{Text: fmt.Sprintf("`%s` is not on your PATH.", command), URL: url}
}

// installCommand is the install command for the registry version, or "".
func installCommand(in FixInput, q func(string) string) string {
	if in.Entry == nil {
		return ""
	}
	d := in.Entry.Distribution
	switch {
	case d.NPX != nil:
		return "npm install -g " + q(d.NPX.Package)
	case d.UVX != nil:
		return "uv tool install " + q(uvSpec(d.UVX.Package))
	}
	return ""
}

// uvSpec writes a package as uv expects a pinned requirement: name==version.
func uvSpec(pkg string) string {
	if strings.Contains(pkg, "==") {
		return pkg
	}
	name, version := acpregistry.PackageVersion(pkg)
	if version == "" {
		return name
	}
	return name + "==" + version
}

func binaryName(cmd string) string {
	if i := strings.LastIndexAny(cmd, `/\`); i >= 0 {
		cmd = cmd[i+1:]
	}
	return cmd
}

func shortTimeout(d time.Duration) string {
	if d <= 0 {
		d = DefaultProbeTimeout
	}
	return d.String()
}

var plainArg = regexp.MustCompile(`^[A-Za-z0-9._/@=:+,-]+$`)

// quoteArg quotes s for a POSIX shell, or for PowerShell on Windows, unless
// it needs no quoting.
func quoteArg(s string, windows bool) string {
	if plainArg.MatchString(s) {
		return s
	}
	if windows {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func isWindows() bool { return runtime.GOOS == "windows" }
