package boundary

import (
	"regexp"
	"strings"
)

// apiMergeRe matches a REST merge endpoint or a GraphQL merge mutation in an
// argument of gh api.
var apiMergeRe = regexp.MustCompile(`(?i)pulls/[^/\s]+/merge\b|mergepullrequest|enablepullrequestautomerge`)

// RunsGHMerge reports whether cmd merges a pull request through gh, in any
// spelling SimpleCommands finds: `gh pr merge` with quoted or escaped words or
// with -R/--repo before `merge` (gh -R o/r pr merge), and `gh api` with a merge
// endpoint or mutation. A gh found anywhere in a command counts, so it also
// matches gh behind a prefix SimpleCommands keeps (sudo -u me gh ...) and gh
// only mentioned as an argument (echo gh pr merge).
//
// It does not see a merge through a gh alias, a script, a request body read
// from a file, or another HTTP client.
func RunsGHMerge(cmd string) bool {
	for _, words := range SimpleCommands(cmd) {
		for i, w := range words {
			if ProgramName(w) == "gh" && ghMerges(words[i+1:]) {
				return true
			}
		}
	}
	return false
}

// ghMerges reports whether gh args merge a pull request.
func ghMerges(args []string) bool {
	var positional []string
	for i := 0; i < len(args) && len(positional) < 2; i++ {
		a := args[i]
		switch {
		case a == "-R" || a == "--repo":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			positional = append(positional, a)
		}
	}
	switch {
	case len(positional) == 2 && positional[0] == "pr" && positional[1] == "merge":
		return true
	case len(positional) > 0 && positional[0] == "api":
		for _, a := range args {
			if apiMergeRe.MatchString(a) {
				return true
			}
		}
	}
	return false
}

// Words returns the words of a command line that is one simple command, with
// quotes and escapes removed as the shell removes them. ok is false when the
// line holds an operator, a redirection, a substitution or a second line.
func Words(cmd string) (words []string, ok bool) {
	lx := lex(cmd)
	if len(lx.subs) > 0 {
		return nil, false
	}
	for _, t := range lx.tokens {
		if t.op != "" {
			return nil, false
		}
		words = append(words, t.word)
	}
	return words, true
}
