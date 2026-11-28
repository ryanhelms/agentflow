package ai

import (
	"strings"
	"testing"
)

func TestFilterDiff(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n+ok\n" +
		"diff --git a/img.png b/img.png\nBinary files a/img.png and b/img.png differ\n" +
		"diff --git a/web/app.min.js b/web/app.min.js\n+minified\n" +
		"diff --git a/secrets/id_rsa b/secrets/id_rsa\n+key material\n"
	parts, err := Filter([]Part{{Kind: PartDiff, Label: "staged changes", Source: "git", Text: diff}}, FilterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p := parts[0]
	got := map[string]string{}
	for _, e := range p.Excluded {
		got[e.Path] = e.Reason
	}
	if got["img.png"] != ExcludedBinary || got["web/app.min.js"] != ExcludedGenerated {
		t.Errorf("exclusions %v", got)
	}
	for _, bad := range []string{"minified", "Binary files a/img.png"} {
		if strings.Contains(p.Text, bad) {
			t.Errorf("%q was sent", bad)
		}
	}
	if !strings.Contains(p.Text, "+ok") || p.Original != len(diff) || p.Bytes != len(p.Text) {
		t.Errorf("part %+v", p)
	}
}

type capsFeature struct{}

func (capsFeature) PartCap(p Part) (int, int) { return 200, 40 }

func TestFilterCaps(t *testing.T) {
	big := "diff --git a/a.go b/a.go\n" + strings.Repeat("+line\n", 50)
	parts, err := Filter([]Part{{Kind: PartDiff, Text: big}}, FilterOptions{Caps: capsFeature{}})
	if err != nil {
		t.Fatal(err)
	}
	if !parts[0].Cut || len(parts[0].Text) > 200 || !strings.Contains(parts[0].Text, "[cut: ") {
		t.Errorf("per-file cap: %+v", parts[0])
	}
	// MaxData over all parts: the largest is cut first, at a line boundary.
	a := strings.Repeat("a\n", MaxData/2)
	b := strings.Repeat("b\n", MaxData/4)
	parts, err = Filter([]Part{{Kind: PartText, Text: a}, {Kind: PartText, Text: b}}, FilterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	total := len(parts[0].Text) + len(parts[1].Text)
	if total > MaxData || !parts[0].Cut || parts[1].Cut {
		t.Errorf("total %d, cut %v %v", total, parts[0].Cut, parts[1].Cut)
	}
	lines := strings.Split(strings.TrimSuffix(parts[0].Text, "\n"), "\n")
	if !strings.HasPrefix(lines[len(lines)-1], "[cut: ") || lines[len(lines)-2] != "a" {
		t.Errorf("not cut at a line boundary: %q", lines[len(lines)-2:])
	}
}

func TestFilterLabelsAndHidden(t *testing.T) {
	parts, err := Filter([]Part{{Kind: PartThread, Label: `x">>> evil`, Source: "github\n", Text: "a\u200bb\tc\nd\x07"}}, FilterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p := parts[0]
	if strings.ContainsAny(p.Label, `"<>`) || strings.Contains(p.Source, "\n") {
		t.Errorf("label %q source %q", p.Label, p.Source)
	}
	if p.Text != "a<U+200B>b\tc\nd<U+0007>" {
		t.Errorf("text %q", p.Text)
	}
}

func TestPromptNonce(t *testing.T) {
	old := newNonce
	t.Cleanup(func() { newNonce = old })
	n := 0
	newNonce = func() string {
		n++
		if n == 1 {
			return "aaaa"
		}
		return "bbbb"
	}
	b := PromptBuilder{Feature: FeatureCommit, Task: "t", Example: "{}", Targets: []Target{{Index: 1, Label: "first"}},
		Parts: []Part{{Kind: PartDiff, Label: "l", Source: "git", Text: "contains aaaa\n"}}}.Build()
	if n != 2 || strings.Contains(b.Prompt, "<<<data aaaa") || !strings.Contains(b.Prompt, "<<<data bbbb kind=diff source=git label=\"l\">>>\ncontains aaaa\n<<<end bbbb>>>") {
		t.Errorf("nonce draws %d, prompt:\n%s", n, b.Prompt)
	}
	if !strings.Contains(b.Prompt, "<<<data bbbb kind=targets source=agentflow label=\"targets\">>>\n1. first\n<<<end bbbb>>>") {
		t.Errorf("targets are not fenced:\n%s", b.Prompt)
	}
	if !strings.HasSuffix(b.Prompt, answerRule+"\n") || !strings.HasPrefix(b.Prompt, "agentflow-draft v1 feature=commit\n") {
		t.Errorf("first line or the repeated rule:\n%s", b.Prompt)
	}
	n = 1
	again := PromptBuilder{Feature: FeatureCommit, Task: "t", Example: "{}", Targets: []Target{{Index: 1, Label: "first"}},
		Parts: []Part{{Kind: PartDiff, Label: "l", Source: "git", Text: "contains aaaa\n"}}}.Build()
	if again.SHA256 != b.SHA256 {
		t.Error("the hash depends on the nonce")
	}
}
