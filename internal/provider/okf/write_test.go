package okf_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/provider/okf"
	"gopkg.in/yaml.v3"
)

func TestMarshalPreservesUnknownMetadata(t *testing.T) {
	raw, err := os.ReadFile("testdata/sensor.md")
	if err != nil {
		t.Fatal(err)
	}
	c := parse(t, string(raw))
	if err := okf.SetField(c.Frontmatter, "title", "Updated"); err != nil {
		t.Fatal(err)
	}
	out, err := okf.Marshal(c.Frontmatter, c.Body)
	if err != nil {
		t.Fatal(err)
	}
	got := parse(t, string(out))
	if got.Type != c.Type || got.Description != c.Description || got.Body != c.Body || !slices.Equal(got.Tags, c.Tags) || got.Title != "Updated" {
		t.Fatalf("round trip = %+v", got)
	}
	item := field(t, field(t, got.Frontmatter, "x-calibration"), "sources").Content[0]
	instrument := field(t, item, "instrument")
	if field(t, item, "alias").Alias != instrument || instrument.Style != yaml.DoubleQuotedStyle ||
		field(t, item, "command").Tag != "!recipe" || field(t, item, "command").Value != "echo some text" {
		t.Fatalf("changed nested extension: %s", out)
	}
}

func TestMarshalAfterReplacingAnchor(t *testing.T) {
	const markdown = "---\ntype: T\ntags: &tags [a, &second b]\ncopy-item: *second\ncopy-list: *tags\nagain: *tags\nx-cycle: &cycle [*cycle]\n---\nbody\r\n---"
	c := parse(t, markdown)
	if err := okf.SetField(c.Frontmatter, "tags", []string{"new"}); err != nil {
		t.Fatal(err)
	}
	out, err := okf.Marshal(c.Frontmatter, c.Body)
	if err != nil {
		t.Fatal(err)
	}
	got := parse(t, string(out))
	untouched := field(t, got.Frontmatter, "copy-list")
	if !slices.Equal(got.Tags, []string{"new"}) || got.Body != c.Body ||
		field(t, got.Frontmatter, "again").Alias != untouched || len(untouched.Content) != 2 ||
		untouched.Content[1].Alias != field(t, got.Frontmatter, "copy-item") ||
		untouched.Content[1].Alias.Value != "b" {
		t.Fatalf("changed values behind aliases: %s", out)
	}
	cycle := field(t, got.Frontmatter, "x-cycle")
	if cycle.Content[0].Alias != cycle {
		t.Fatal("recursive alias lost")
	}
}

func TestMarshalCoreStringsAndRequiredType(t *testing.T) {
	c := parse(t, "---\ntype: ! 42\nother: ! 0e2\n---")
	large := strings.Repeat("9", 500)
	if err := okf.SetField(c.Frontmatter, "description", large); err != nil {
		t.Fatal(err)
	}
	out, err := okf.Marshal(c.Frontmatter, c.Body)
	if err != nil {
		t.Fatal(err)
	}
	got := parse(t, string(out))
	if got.Type != "42" || got.Description != large || field(t, got.Frontmatter, "other").Tag != "!!str" {
		t.Fatalf("changed YAML core types: %s", out)
	}
	if _, err := okf.Marshal(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, ""); err == nil {
		t.Fatal("marshaled a concept without a type")
	}
}
