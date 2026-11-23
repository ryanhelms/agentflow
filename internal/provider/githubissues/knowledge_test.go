package githubissues

import (
	"slices"
	"strings"
	"testing"
)

func TestRelatedKnowledge(t *testing.T) {
	const section = "## Related Knowledge\n- repository/eng/one\n"
	tests := []struct {
		name     string
		body     string
		want     []string
		warnings int
	}{
		{"legacy", "Design: repository/eng/one\n- [handbook](https://example.invalid/book)\n- [ ] Verify", nil, 0},
		{"examples", "   ~~~~md\n" + section + "~~~\n" + section + "~~~~\n<!--\n" + section + "-->\n> ## Related Knowledge\n> - filesystem/eng/two", nil, 0},
		{"sections", "## Related Knowledge\r\n- `repository/a%2Fb/design%20v2`\r\n- repository/a%2Fb/design%20v2\r\n### Other\n- repository/eng/ignored\n" + section, []string{"repository/a%2Fb/design%20v2", "repository/eng/one"}, 0},
		{"malformed", section + "- https://example.invalid/book\n- [read](repository/eng/two)\n- repository/eng/%0A\n- repository/eng/%xx\n- repository/eng/two plus prose\n- repository/eng/two\n", []string{"repository/eng/one", "repository/eng/two"}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			links, warnings := RelatedKnowledge(tt.body)
			var got []string
			for _, link := range links {
				got = append(got, link.Ref.String())
			}
			if !slices.Equal(got, tt.want) || len(warnings) != tt.warnings || strings.Contains(strings.Join(warnings, "\n"), "https://") {
				t.Fatalf("related knowledge = %v, warnings = %v", got, warnings)
			}
		})
	}
}
