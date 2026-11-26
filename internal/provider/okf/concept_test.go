package okf_test

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/provider/okf"
	"gopkg.in/yaml.v3"
)

func parse(t *testing.T, markdown string) okf.Concept {
	t.Helper()
	c, err := okf.Parse([]byte(markdown))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Frontmatter == nil || c.Frontmatter.Kind != yaml.MappingNode {
		t.Fatalf("Frontmatter = %#v, want a YAML mapping", c.Frontmatter)
	}
	return c
}

func field(t *testing.T, mapping *yaml.Node, key string) *yaml.Node {
	t.Helper()
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	t.Fatalf("missing key %q", key)
	return nil
}

func TestConcept(t *testing.T) {
	data, err := os.ReadFile("testdata/sensor.md")
	if err != nil {
		t.Fatal(err)
	}
	c := parse(t, string(data))
	if c.Type != "Sensor Calibration" || c.Title != "O₂: bench #4" ||
		c.Description != "Factory calibration for sensor 4." ||
		!slices.Equal(c.Tags, []string{"sensors", "lab, west"}) ||
		c.Body != "# Bench notes\n\n---\ntype: This line belongs to the body.\n" {
		t.Errorf("unexpected concept fields: %+v", c)
	}
	extension := field(t, c.Frontmatter, "x-calibration")
	item := field(t, extension, "sources").Content[0]
	instrument := field(t, item, "instrument")
	if instrument.Style != yaml.DoubleQuotedStyle || field(t, item, "alias").Alias != instrument {
		t.Error("quoted scalar or its alias lost")
	}
	if command := field(t, item, "command"); command.Tag != "!recipe" || command.Value != "echo some text" {
		t.Errorf("unexpected tagged command: %+v", command)
	}
	if field(t, extension, "template").Value != "${SENSOR_TOKEN}" ||
		field(t, field(t, c.Frontmatter, "executor"), "resource").Value != "./tools/check.py" {
		t.Error("template or resource changed")
	}
}

func TestDelimiterAndBody(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		markdown := strings.Join([]string{"---", "type: Novel", "description: |", "  ---", "---"}, newline)
		if c := parse(t, markdown); c.Type != "Novel" || c.Description != "---\n" || c.Body != "" {
			t.Errorf("terminal delimiter %q: %+v", newline, c)
		}
		body := newline + "---\r\n--- # second doc?\n[bad: yaml\n# café\x00"
		if got := parse(t, markdown+newline+body).Body; got != body {
			t.Errorf("body after %q = %q, want %q", newline, got, body)
		}
	}
	if c := parse(t, "---\ntype: Novel\n---"); c.Title != "" || c.Description != "" || c.Tags != nil {
		t.Errorf("type-only concept: %+v", c)
	}
}

func TestNotConcept(t *testing.T) {
	for _, markdown := range []string{
		"", "# ordinary\n---\n", "--- \ntype: T\n---", "\t---\ntype: T\n---",
		"\n---\ntype: T\n---", "\ufeff---\ntype: T\n---", "---\n{}\n---",
		"---\ntitle: Unmarked\n---", "---\nType: Case matters\n---",
		"---\ntype:\n---", "---\ntype: ''\n---", "---\ntype: []\n---", "---\ntype: ~\n---",
	} {
		if _, err := okf.Parse([]byte(markdown)); !errors.Is(err, okf.ErrNotConcept) {
			t.Errorf("Parse(%q) error = %v, want ErrNotConcept", markdown, err)
		}
	}
}

func TestMalformed(t *testing.T) {
	for _, markdown := range []string{
		"---", "---\ntype: T\n--- ", "---\ntype: T\n ---", "---\ntype: T\n---\t",
		"---\n---", "---\n# only a comment\n---", "---\n[not, a, mapping]\n---",
		"---\ntype: [\n---", "---\ntype: T\n--- # extra document\n{}\n---",
		"---\ntype: T\n...\ninvalid trailing text\n---",
		"---\ntype: T\n---\n\xff", "---\ntype: \xff\n---",
		"---\ntype: 42\n---", "---\ntype: true\n---", "---\ntype: {invalid: shape}\n---",
		"---\ntype: [invalid]\n---", "---\ntype: *missing\n---",
	} {
		if _, err := okf.Parse([]byte(markdown)); err == nil || errors.Is(err, okf.ErrNotConcept) {
			t.Errorf("Parse(%q) error = %v, want malformed input error", markdown, err)
		}
	}
}

func TestOptionalValues(t *testing.T) {
	for _, tc := range []struct {
		fields string
		tags   []string
	}{
		{"title: [bad]\ndescription: {}\ntags: 'one, two'", []string{"one, two"}},
		{"title: false\ndescription: 1\ntags: [one, null, 4, !!str 5, {bad: shape}, '', one]", []string{"one", "5", "one"}},
		{"title: null\ndescription: ''\ntags: {bad: shape}", nil},
		{"title: !display private\ndescription: []\ntags: null", nil},
		{"tags: &cycle [*cycle, last]", []string{"last"}},
	} {
		c := parse(t, "---\ntype: Reference\n"+tc.fields+"\n---")
		if c.Title != "" || c.Description != "" || !slices.Equal(c.Tags, tc.tags) {
			t.Errorf("fields %q: got %+v, want tags %q", tc.fields, c, tc.tags)
		}
	}
}

func TestYAMLValues(t *testing.T) {
	const header = `type: &label "  odd\u0020type  "
title: *label
description: |+
  first

  last

tags: &list [yes, ON, 2026-05-28, 1_000, 0b10, 0x5, true, 2.5, '.inf']
x-tags: *list
<<: {type: unused}
x-numbers: [0010, 0o12, .INF, -1.0e+5, null, FALSE]
`
	c := parse(t, "---\n"+header+"---")
	if c.Type != "  odd type  " || c.Title != c.Type || c.Description != "first\n\nlast\n\n" ||
		!slices.Equal(c.Tags, []string{"yes", "ON", "2026-05-28", "1_000", "0b10", ".inf"}) {
		t.Errorf("unexpected core values: %+v", c)
	}
	for i, tag := range []string{"!!int", "!!int", "!!float", "!!float", "!!null", "!!bool"} {
		if n := field(t, c.Frontmatter, "x-numbers").Content[i]; n.Tag != tag {
			t.Errorf("scalar %q has tag %q, want %q", n.Value, n.Tag, tag)
		}
	}
	for i := 0; i < len(c.Frontmatter.Content); i += 2 {
		key := c.Frontmatter.Content[i]
		if key.Value == "<<" && key.Tag != "!!str" {
			t.Errorf("merge-like key has tag %q", key.Tag)
		}
	}
	c = parse(t, "---\nx: &tags [two, three]\ntags: *tags\ntype: T\n---")
	if !slices.Equal(c.Tags, []string{"two", "three"}) {
		t.Errorf("tags alias = %q", c.Tags)
	}
}

func TestDuplicateKeys(t *testing.T) {
	for _, fields := range []string{
		"'type': duplicate",
		"x: [{nested: one, nested: two}]",
		"x: {0o13: octal, 0xB: hex}",
		"x: {010: decimal, 10: same}",
		"x: {TRUE: one, True: two}",
		"x: {null: one, ~: two}",
		"x: {2.000: one, 0.2E+1: two}",
		"x: {&label name: one, *label : two}",
		"x: {[a, b]: one, [a, b]: two}",
		"x: {{a: 1, b: 2}: one, {b: 2, a: 1}: two}",
	} {
		_, err := okf.Parse([]byte("---\ntype: T\n" + fields + "\n---"))
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("fields %q: got %v, want duplicate key error", fields, err)
		}
	}
	parse(t, "---\ntype: T\nx: {1: int, '1': str, 1.0: float, 1.0000000000000000001: distinct}\n---")
	_, err := okf.Parse([]byte("---\ntype: T\nx: {&cycle [*cycle]: example}\n---"))
	if err == nil || !strings.Contains(err.Error(), "recursive YAML key") {
		t.Errorf("recursive mapping key: got %v", err)
	}
}

func TestExplicitYAML12(t *testing.T) {
	const markdown = "---\n# preamble\n%YAML 1.2 # core schema\n--- {type: ! 42, title: &s ! false, tags: [*s, 42]}\n...\n---"
	c := parse(t, markdown)
	if c.Type != "42" || c.Title != "false" || !slices.Equal(c.Tags, []string{"false"}) {
		t.Errorf("explicit core strings: %+v", c)
	}
	c = parse(t, "---\ntype: &label # anchor before tag\n  ! 3\nx: 'multiline\n  %YAML 1.2'\n---")
	if c.Type != "3" || field(t, c.Frontmatter, "x").Value != "multiline %YAML 1.2" {
		t.Errorf("tag or directive in scalar: %+v", c)
	}
}
