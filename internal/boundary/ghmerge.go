package boundary

import (
	"regexp"
	"strings"
)

var (
	// restMergeRe matches the REST merge endpoint in an argument of gh api.
	restMergeRe = regexp.MustCompile(`(?i)pulls/[^/\s]+/merge\b`)
	// graphQLMergeRe matches the GraphQL mutations that merge or queue a merge.
	graphQLMergeRe = regexp.MustCompile(`(?i)mergepullrequest|enablepullrequestautomerge|enqueuepullrequest`)
)

// RunsGHMerge reports whether cmd merges a pull request through gh, in any
// spelling SimpleCommands finds: `gh pr merge` with quoted or escaped words or
// with -R/--repo before `merge` (gh -R o/r pr merge), and `gh api` with a merge
// endpoint or mutation. A gh found anywhere in a command counts, so it also
// matches gh behind a prefix SimpleCommands keeps (sudo -u me gh ...) and gh
// only mentioned as an argument (echo gh pr merge). A program named through a
// variable or a substitution ($GH, $(which gh)) may be gh, so it counts too.
//
// It does not see a merge through a gh alias, a script, a GraphQL query read
// from a file, or another HTTP client.
func RunsGHMerge(cmd string) bool {
	for _, words := range SimpleCommands(cmd) {
		for i, w := range words {
			if (ProgramName(w) == "gh" || maybeAnyProgram(w)) && ghMerges(words[i+1:]) {
				return true
			}
		}
	}
	return false
}

// maybeAnyProgram reports whether the shell decides what word w runs.
func maybeAnyProgram(w string) bool {
	return strings.HasPrefix(w, "$") || strings.Contains(w, "$(…)") || strings.Contains(w, "`…`")
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
		// A GET of the merge endpoint only asks whether the PR is merged.
		write := apiWrites(args)
		for _, a := range args {
			if graphQLMergeRe.MatchString(a) || (write && restMergeRe.MatchString(a)) {
				return true
			}
		}
	}
	return false
}

// apiWrites reports whether gh api args send anything but GET or HEAD, as
// Checker decides it: a method other than those, or a field or body.
func apiWrites(args []string) bool {
	method, fields := "", false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "-X" || a == "--method") && i+1 < len(args):
			i++
			method = strings.ToUpper(args[i])
		case strings.HasPrefix(a, "--method="):
			method = strings.ToUpper(strings.TrimPrefix(a, "--method="))
		case strings.HasPrefix(a, "-X") && len(a) > 2:
			method = strings.ToUpper(a[2:])
		case a == "-f" || a == "-F" || a == "--field" || a == "--raw-field" || a == "--input" ||
			strings.HasPrefix(a, "--field=") || strings.HasPrefix(a, "--raw-field=") || strings.HasPrefix(a, "--input="):
			fields = true
		}
	}
	return (method != "" && method != "GET" && method != "HEAD") || (method == "" && fields)
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
