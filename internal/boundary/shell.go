package boundary

import (
	"strings"
)

// token is a word or a shell operator from a command line.
type token struct {
	op   string // "" for a word; ";", "&&", "||", "|", "&", "(", ")" or "redir"
	word string
}

// lexed is a tokenised command line plus the command substitutions found in it,
// which run as commands of their own.
type lexed struct {
	tokens []token
	subs   []string
}

// lex splits a shell command line into words and operators. It honours single
// and double quotes, backslash escapes, comments, heredocs (whose bodies are
// skipped) and $(...) or `...` substitutions (returned in subs). It is not a full
// shell parser: it only needs to find the simple commands and their words.
func lex(s string) lexed {
	var out lexed
	var cur strings.Builder
	started := false
	var heredocs []string // pending delimiters; bodies start at the next newline
	expectDelim := false
	emit := func() {
		if !started {
			return
		}
		w := cur.String()
		cur.Reset()
		started = false
		if expectDelim {
			expectDelim = false
			heredocs = append(heredocs, strings.TrimLeft(w, "-"))
			return
		}
		out.tokens = append(out.tokens, token{word: w})
	}
	op := func(o string) {
		emit()
		out.tokens = append(out.tokens, token{op: o})
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 < len(s) {
				i++
				if s[i] != '\n' {
					cur.WriteByte(s[i])
					started = true
				}
			}
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				end = len(s) - i - 1
			}
			cur.WriteString(s[i+1 : i+1+end])
			started = true
			i += end + 1
		case c == '"':
			started = true
			for i++; i < len(s) && s[i] != '"'; i++ {
				switch {
				case s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`\n", s[i+1]) >= 0:
					i++
					if s[i] != '\n' {
						cur.WriteByte(s[i])
					}
				case s[i] == '$' && i+1 < len(s) && s[i+1] == '(':
					body, n := balanced(s[i+1:])
					out.subs = append(out.subs, body)
					cur.WriteString("$(…)")
					i += n
				case s[i] == '`':
					end := strings.IndexByte(s[i+1:], '`')
					if end < 0 {
						end = len(s) - i - 1
					}
					out.subs = append(out.subs, s[i+1:i+1+end])
					cur.WriteString("`…`")
					i += end + 1
				default:
					cur.WriteByte(s[i])
				}
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			body, n := balanced(s[i+1:])
			out.subs = append(out.subs, body)
			cur.WriteString("$(…)")
			started = true
			i += n
		case c == '`':
			end := strings.IndexByte(s[i+1:], '`')
			if end < 0 {
				end = len(s) - i - 1
			}
			out.subs = append(out.subs, s[i+1:i+1+end])
			cur.WriteString("`…`")
			started = true
			i += end + 1
		case c == '#' && !started:
			for i+1 < len(s) && s[i+1] != '\n' {
				i++
			}
		case c == ' ' || c == '\t':
			emit()
		case c == '\n':
			op(";")
			if len(heredocs) > 0 {
				i = skipHeredocs(s, i+1, heredocs) - 1
				heredocs = nil
			}
		case c == ';':
			if i+1 < len(s) && s[i+1] == ';' {
				i++
			}
			op(";")
		case c == '&':
			next := byte(0)
			if i+1 < len(s) {
				next = s[i+1]
			}
			switch next {
			case '&':
				i++
				op("&&")
			case '>':
				i++
				op("redir")
			default:
				op("&")
			}
		case c == '|':
			if i+1 < len(s) && s[i+1] == '|' {
				i++
				op("||")
			} else {
				if i+1 < len(s) && s[i+1] == '&' {
					i++
				}
				op("|")
			}
		case c == '(' || c == ')':
			op(string(c))
		case c == '<' || c == '>':
			if started && allDigits(cur.String()) {
				cur.Reset()
				started = false
			}
			if c == '<' && strings.HasPrefix(s[i:], "<<<") {
				i += 2
				op("redir")
				continue
			}
			if c == '<' && strings.HasPrefix(s[i:], "<<") {
				i++
				if i+1 < len(s) && s[i+1] == '-' {
					i++
				}
				emit()
				expectDelim = true
				continue
			}
			for i+1 < len(s) && (s[i+1] == '>' || s[i+1] == '&' || s[i+1] == '|') {
				i++
			}
			op("redir")
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	emit()
	return out
}

// balanced returns the body of the parenthesised group at the start of s and the
// number of bytes it spans, including both parentheses.
func balanced(s string) (string, int) {
	depth := 0
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '\\':
			i++
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return s[1:i], i
			}
		}
	}
	if len(s) < 1 {
		return "", 0
	}
	return s[1:], len(s) - 1
}

// skipHeredocs returns the index just after the last heredoc body that starts at i.
func skipHeredocs(s string, i int, delims []string) int {
	for _, d := range delims {
		for i < len(s) {
			end := strings.IndexByte(s[i:], '\n')
			line := s[i:]
			next := len(s)
			if end >= 0 {
				line, next = s[i:i+end], i+end+1
			}
			i = next
			if strings.TrimLeft(line, "\t") == d {
				break
			}
		}
	}
	return i
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
