package boundary

import (
	"slices"
	"strings"
)

// SimpleCommands returns the words of every simple command that cmd runs: of
// the line itself, of its command substitutions, and of the command lines it
// hands to a shell (sh -c, cmd /c, powershell -Command), to eval, to script or
// to expect. Assignments, shell keywords and prefixes such as env, sudo and
// nohup are removed, so words[0] is the program. Words that are only arguments
// of another program, quoted or not, are never a command of their own. A
// command run through xargs ends with InputArgs, for the arguments xargs adds.
//
// It only reads the line. A program named through a variable, a command line
// read from a pipe or a file, or one built while it runs is not seen. Nor is a
// command behind a prefix option that takes a separate value (sudo -u me), or
// behind a shell option before -c (bash -o pipefail -c).
func SimpleCommands(cmd string) [][]string {
	var out [][]string
	collect(cmd, 0, &out)
	return out
}

// InputArgs stands for the arguments a command takes from its input (xargs).
const InputArgs = "…"

// ProgramName is the name a program is invoked by: the last element of path,
// with either kind of separator, in lower case and without a Windows
// extension.
func ProgramName(path string) string {
	name := strings.ToLower(path[strings.LastIndexAny(path, `/\`)+1:])
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1"} {
		name = strings.TrimSuffix(name, ext)
	}
	return name
}

func collect(cmd string, depth int, out *[][]string) {
	if depth > maxDepth {
		return
	}
	lx := lex(cmd)
	for _, sub := range lx.subs {
		collect(sub, depth+1, out)
	}
	var words []string
	skipNext := false
	for _, t := range lx.tokens {
		switch {
		case t.op == "redir":
			skipNext = true
		case t.op != "":
			addCommand(words, depth, out)
			words = nil
		case skipNext:
			skipNext = false
		default:
			words = append(words, t.word)
		}
	}
	addCommand(words, depth, out)
}

// addCommand records one simple command and the commands it runs in turn.
func addCommand(words []string, depth int, out *[][]string) {
	all := words
	words = unwrap(words)
	if len(words) == 0 || depth > maxDepth {
		return
	}
	// unwrap only drops from the front.
	if slices.ContainsFunc(all[:len(all)-len(words)], func(w string) bool { return ProgramName(w) == "xargs" }) {
		words = append(slices.Clone(words), InputArgs)
	}
	*out = append(*out, words)
	args := words[1:]
	line := func(s string) { collect(s, depth+1, out) }
	// Windows command lines use backslashes, which are escapes to the lexer.
	windows := func(a []string) { line(strings.ReplaceAll(strings.Join(a, " "), `\`, "/")) }
	run := func(w []string) { addCommand(w, depth+1, out) }
	switch ProgramName(words[0]) {
	case "sh", "bash", "zsh", "dash", "ksh", "fish":
		for i, a := range args {
			if commandFlag(a) {
				if i+1 < len(args) {
					line(args[i+1])
				}
				return
			}
			if !strings.HasPrefix(a, "-") {
				run(args[i:]) // sh path/to/program args
				return
			}
		}
	case "eval":
		line(strings.Join(args, " "))
	case "script":
		scriptCommands(args, line, run)
	case "expect":
		for i, a := range args {
			if a == "-c" && i+1 < len(args) {
				line(args[i+1])
			}
		}
	case "spawn": // inside an expect script
		run(args)
	case "cmd":
		for i, a := range args {
			if strings.EqualFold(a, "/c") || strings.EqualFold(a, "/k") {
				windows(args[i+1:])
				return
			}
		}
	case "powershell", "pwsh":
		for i, a := range args {
			switch strings.ToLower(a) {
			case "-c", "-command", "-f", "-file":
				windows(args[i+1:])
				return
			}
		}
	}
}

// scriptCommands finds the command that script(1) runs, in both forms:
// util-linux takes it as -c COMMAND, and BSD and macOS as the words after the
// transcript file.
func scriptCommands(args []string, line func(string), run func([]string)) {
	for i, a := range args {
		switch {
		case strings.HasPrefix(a, "--command="):
			line(strings.TrimPrefix(a, "--command="))
		case (a == "--command" || commandFlag(a)) && i+1 < len(args):
			line(args[i+1])
		}
	}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-F" || args[i] == "-t": // BSD options with a value
			i++
		case args[i] == "--" || strings.HasPrefix(args[i], "-"):
		default:
			run(args[i+1:])
			return
		}
	}
}

// commandFlag reports whether a is -c, alone or ending a cluster such as -lc.
func commandFlag(a string) bool {
	return len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.HasSuffix(a, "c")
}

var shellKeywords = map[string]bool{"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true, "until": true, "!": true, "{": true}

// unwrap drops everything before the program: assignments, shell keywords and
// command prefixes, however they are combined.
func unwrap(words []string) []string {
	env := map[string]string{}
	for len(words) > 0 {
		if assignRe.MatchString(words[0]) || shellKeywords[words[0]] {
			words = words[1:]
			continue
		}
		next := stripWrappers(words, env)
		if len(next) > 0 {
			switch ProgramName(next[0]) {
			case "setsid", "stdbuf", "unbuffer", "caffeinate", "doas":
				next = next[1:]
				for len(next) > 0 && strings.HasPrefix(next[0], "-") {
					next = next[1:]
				}
			}
		}
		if len(next) == len(words) {
			break
		}
		words = next
	}
	return words
}
