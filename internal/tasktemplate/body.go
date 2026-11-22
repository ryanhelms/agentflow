package tasktemplate

import "strings"

func appendBlocks(body string, blocks []string) string {
	if len(blocks) == 0 {
		return body
	}
	if body != "" {
		body += "\n\n"
	}
	return body + strings.Join(blocks, "\n\n")
}

type bodyIndex struct {
	sections map[string]bool
	items    map[string]bool
}

// inspectBody reads the portable line format described in README.md.
func inspectBody(body string) bodyIndex {
	index := bodyIndex{make(map[string]bool), make(map[string]bool)}
	var heading, fence string
	for raw := range strings.Lines(body) {
		line := strings.TrimRight(raw, "\r\n \t")
		if fence != "" {
			if strings.HasPrefix(line, fence) && strings.Trim(line, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(line, "## ") {
			heading = strings.TrimSpace(line[3:])
			if _, ok := index.sections[heading]; !ok {
				index.sections[heading] = false
			}
			continue
		}
		if strings.HasPrefix(line, "# ") {
			heading = ""
		}
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fence = line[:len(line)-len(strings.TrimLeft(line, line[:1]))]
		}
		for _, prefix := range []string{"- [ ] ", "- [x] ", "- [X] "} {
			if item, ok := strings.CutPrefix(line, prefix); ok {
				index.items[item] = true
			}
		}
		if heading != "" && strings.TrimSpace(line) != "" {
			index.sections[heading] = true
		}
	}
	return index
}
