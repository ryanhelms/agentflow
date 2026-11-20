package atlassian

import (
	"encoding/json"
	"strings"
)

type adfNode struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Attrs   json.RawMessage `json:"attrs"`
	Content []adfNode       `json:"content"`
}

// ADFText converts an Atlassian Document Format value to plain text with
// "- " bullets and "[ ]"/"[x]" task markers. Non-ADF strings are returned as-is.
func ADFText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var doc adfNode
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	var b strings.Builder
	render(&b, doc, 0)
	return strings.TrimSpace(collapse(b.String()))
}

func render(b *strings.Builder, n adfNode, depth int) {
	switch n.Type {
	case "text":
		b.WriteString(n.Text)
		return
	case "hardBreak":
		b.WriteString("\n")
		return
	case "mention", "emoji", "inlineCard":
		var a struct {
			Text, URL, ShortName string
		}
		_ = json.Unmarshal(n.Attrs, &a)
		b.WriteString(firstNonEmpty(a.Text, a.ShortName, a.URL))
		return
	case "listItem", "taskItem":
		b.WriteString(strings.Repeat("  ", max(depth-1, 0)))
		if n.Type == "taskItem" {
			var a struct{ State string }
			_ = json.Unmarshal(n.Attrs, &a)
			if a.State == "DONE" {
				b.WriteString("[x] ")
			} else {
				b.WriteString("[ ] ")
			}
		} else {
			b.WriteString("- ")
		}
		for _, c := range n.Content {
			if c.Type == "paragraph" {
				for _, t := range c.Content {
					render(b, t, depth)
				}
				b.WriteString("\n")
				continue
			}
			render(b, c, depth+1)
		}
		return
	case "bulletList", "orderedList", "taskList":
		for _, c := range n.Content {
			render(b, c, depth+1)
		}
		b.WriteString("\n")
		return
	case "heading":
		for _, c := range n.Content {
			render(b, c, depth)
		}
		b.WriteString("\n")
		return
	case "codeBlock":
		b.WriteString("```\n")
		for _, c := range n.Content {
			render(b, c, depth)
		}
		b.WriteString("\n```\n")
		return
	}
	for _, c := range n.Content {
		render(b, c, depth)
	}
	if n.Type == "paragraph" || n.Type == "blockquote" || n.Type == "rule" {
		b.WriteString("\n\n")
	}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func collapse(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// ADFDoc wraps plain text (paragraphs separated by blank lines) in an ADF document.
func ADFDoc(text string) map[string]any {
	var content []any
	for _, para := range strings.Split(strings.TrimSpace(text), "\n\n") {
		var inline []any
		for i, line := range strings.Split(para, "\n") {
			if i > 0 {
				inline = append(inline, map[string]any{"type": "hardBreak"})
			}
			if line != "" {
				inline = append(inline, map[string]any{"type": "text", "text": line})
			}
		}
		content = append(content, map[string]any{"type": "paragraph", "content": inline})
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}
