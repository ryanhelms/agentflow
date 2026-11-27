package setup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheckHomeQuestions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	q, err := CheckHomeQuestions()
	if err != nil || !q.Needed || q.Path != filepath.Join(home, "config.yml") {
		t.Fatalf("fresh home: %+v %v", q, err)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("the check wrote %v", entries)
	}
	if err := SaveHomeAnswers(q.Path, nil, ""); err != nil {
		t.Fatal(err)
	}
	if q, err = CheckHomeQuestions(); err != nil || q.Needed {
		t.Fatalf("initialised home: %+v %v", q, err)
	}
}

func TestSaveHomeAnswersNeverReplacesAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home", "config.yml")
	if err := SaveHomeAnswers(path, []string{"acme"}, ""); err == nil {
		t.Fatal("saved owners without a mode")
	}
	if err := SaveHomeAnswers(path, []string{"acme"}, "ask"); err != nil {
		t.Fatal(err)
	}
	if err := SaveHomeAnswers(path, nil, ""); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("replaced the file: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "init:\n  auto: ask\n  orgs:\n    - acme\n" {
		t.Fatalf("config = %q", b)
	}
}

func TestParseHomeAnswers(t *testing.T) {
	for in, want := range map[string][]string{
		"":                   {},
		"  ":                 {},
		"acme":               {"acme"},
		"acme, example-org":  {"acme", "example-org"},
		"acme Example ACME,": {"acme", "Example"},
	} {
		got, err := ParseOwners(in)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("ParseOwners(%q) = %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"acme/demo", "-acme", "acme_corp", "https://github.com/acme"} {
		if _, err := ParseOwners(in); err == nil {
			t.Errorf("ParseOwners(%q) accepted", in)
		}
	}
	for in, want := range map[string]string{"auto": AutoInit, "ASK": AutoAsk, "off": AutoOff} {
		if got, err := ParseAutoMode(in); err != nil || got != want {
			t.Errorf("ParseAutoMode(%q) = %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "yes", "on"} {
		if _, err := ParseAutoMode(in); err == nil {
			t.Errorf("ParseAutoMode(%q) accepted", in)
		}
	}
}
