package okf

import (
	"bytes"
	"regexp"

	"gopkg.in/yaml.v3"
)

var yaml12Directive = regexp.MustCompile(`^%YAML[ \t]+1\.2(?:[ \t]|$)`)

// yaml.v3 only accepts 1.1 in its version directive. Let its syntax parser
// consume a 1.2 directive while applying the core schema ourselves. Only the
// preamble is eligible: a directive-shaped line in a scalar is content.
func parserInput(header []byte) []byte {
	for rest := header; len(rest) > 0; {
		line, next := cutLine(rest)
		if match := yaml12Directive.Find(line); match != nil {
			pos := len(header) - len(rest) + bytes.Index(match, []byte("1.2")) + 2
			adapted := bytes.Clone(header)
			adapted[pos] = '1'
			return adapted
		}
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) != 0 && trimmed[0] != '#' && !bytes.HasPrefix(line, []byte("%")) {
			break
		}
		rest = next
	}
	return header
}

// yaml.v3 also discards the non-specific tag (!) from plain scalars. Recover
// it at the parser's own node positions so quoted text and comments cannot
// be mistaken for tags. Columns count Unicode characters, not bytes.
type sourceLines [][]rune

func tagSource(header []byte) sourceLines {
	if !bytes.ContainsRune(header, '!') {
		return nil
	}
	var lines sourceLines
	for len(header) > 0 {
		line, rest := cutLine(header)
		lines = append(lines, bytes.Runes(line))
		header = rest
	}
	return lines
}

func (lines sourceLines) nonSpecific(n *yaml.Node) bool {
	if n.Line < 1 || n.Line > len(lines) {
		return false
	}
	line := lines[n.Line-1]
	if n.Column < 1 || n.Column > len(line) {
		return false
	}
	line = line[n.Column-1:]
	if line[0] == '&' && n.Anchor != "" {
		// An anchor can precede the tag, even on a separate line.
		line = line[min(len(line), 1+len(n.Anchor)):]
		for i := n.Line; ; i++ {
			for len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
				line = line[1:]
			}
			if len(line) > 0 && line[0] != '#' {
				break
			}
			if i == len(lines) {
				return false
			}
			line = lines[i]
		}
	}
	return len(line) > 0 && line[0] == '!' && (len(line) == 1 || line[1] == ' ' || line[1] == '\t')
}
