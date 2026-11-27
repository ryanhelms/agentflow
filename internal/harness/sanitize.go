package harness

import (
	"regexp"
	"sort"
	"strings"

	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// Content Filter for agent-supplied text ("Sign-in guidance"): one line,
// control and bidirectional characters removed, clamped.

// SanitizeText cleans untrusted display text and clamps it to max bytes
// (harnessdef.CleanText).
func SanitizeText(s string, max int) string { return harnessdef.CleanText(s, max) }

// AuthMethod is one sign-in method an agent advertised in its initialize
// answer, as lane 6.2's probe read it (it converts acp.AuthMethod). Type ""
// means "agent".
type AuthMethod struct {
	ID          string
	Name        string
	Description string
	Type        string // "", "agent" or "terminal"
	Args        []string
	Env         map[string]string
}

// Sign-in method types.
const (
	MethodAgent    = "agent"
	MethodTerminal = "terminal"
)

var signInEnvName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// SignInGuidance turns advertised methods into Harness.SignIn: at most
// MaxSignInMethods, text sanitized and clamped, unknown types and methods
// without an ID dropped. command is the found command name (never its
// path) and args the definition's args.
//
// A terminal method gets a command to copy: the method's env as a
// NAME=value prefix (sorted), command, args, then the method's args. The
// command is left out and Unsafe set when an arg breaks the arg rule, the
// method has more than MaxSignInArgs args, an env name is not
// ^[A-Z_][A-Z0-9_]*$ or is a launch-safety name, or an env value breaks the
// arg rule or is over MaxSignInEnvValue bytes (the value is pasted into a
// shell).
func SignInGuidance(command string, args []string, methods []AuthMethod) []SignIn {
	var out []SignIn
	for _, m := range methods {
		if len(out) == MaxSignInMethods {
			break
		}
		id := SanitizeText(m.ID, MaxIDLen)
		if id == "" {
			continue
		}
		typ := m.Type
		if typ == "" {
			typ = MethodAgent
		}
		if typ != MethodAgent && typ != MethodTerminal {
			continue
		}
		s := SignIn{
			Method:      id,
			Type:        typ,
			Name:        SanitizeText(m.Name, MaxNameLen),
			Description: SanitizeText(m.Description, MaxDescriptionLen),
		}
		if typ == MethodTerminal {
			if c, ok := terminalCommand(command, args, m); ok {
				s.Command = c
			} else {
				s.Unsafe = true
			}
		}
		out = append(out, s)
	}
	return out
}

func terminalCommand(command string, args []string, m AuthMethod) (string, bool) {
	if !harnessdef.ValidCommandName(command) || len(m.Args) > MaxSignInArgs {
		return "", false
	}
	for _, a := range append(append([]string(nil), args...), m.Args...) {
		if !harnessdef.ValidArg(a) {
			return "", false
		}
	}
	names := make([]string, 0, len(m.Env))
	for name, value := range m.Env {
		if !signInEnvName.MatchString(name) || harnessdef.LaunchSafetyName(name) ||
			len(value) > MaxSignInEnvValue || !harnessdef.ValidArg(value) {
			return "", false
		}
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names)+1+len(args)+len(m.Args))
	for _, n := range names {
		parts = append(parts, n+"="+m.Env[n])
	}
	parts = append(parts, command)
	parts = append(parts, args...)
	parts = append(parts, m.Args...)
	return strings.Join(parts, " "), true
}
