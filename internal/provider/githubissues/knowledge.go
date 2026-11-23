package githubissues

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
)

var markdownHeading = regexp.MustCompile(`^#{1,6}([ \t]|$)`)

// RelatedKnowledge reads top-level "- REF" entries under the literal heading.
// Keep the issue body intact for templates, checklists and legacy readers.
func RelatedKnowledge(body string) (links []provider.Link, warnings []string) {
	const heading = "## Related Knowledge"
	if !strings.Contains(body, heading) {
		return nil, nil
	}
	seen := make(map[provider.Ref]bool)
	var inSection, inComment bool
	var fence string
	for i, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r \t")
		if inComment {
			if strings.Contains(line, "-->") {
				inComment = false
			}
			continue
		}
		// Markdown permits up to three spaces before a fence or heading.
		block := strings.TrimLeft(line, " ")
		if len(line)-len(block) > 3 {
			continue
		}
		if fence != "" {
			if strings.HasPrefix(block, fence) && strings.Trim(block, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(block, "```") || strings.HasPrefix(block, "~~~") {
			fence = block[:len(block)-len(strings.TrimLeft(block, block[:1]))]
			continue
		}
		if before, after, ok := strings.Cut(line, "<!--"); ok {
			inComment = !strings.Contains(after, "-->")
			line = before
			block = strings.TrimLeft(line, " ")
		}
		if markdownHeading.MatchString(block) {
			inSection = line == heading
			continue
		}
		value, bullet := strings.CutPrefix(line, "- ")
		if !inSection || !bullet {
			continue
		}
		if len(value) >= 2 && value[0] == '`' && value[len(value)-1] == '`' {
			value = value[1 : len(value)-1]
		}
		ref, err := provider.ParseRef(value)
		if err != nil || ref.String() != value {
			warnings = append(warnings, fmt.Sprintf("Related Knowledge line %d: expected a qualified instance/scope/id reference", i+1))
			continue
		}
		if !seen[ref] {
			links = append(links, provider.Link{Ref: &ref})
			seen[ref] = true
		}
	}
	return links, warnings
}
