package auth

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestConfluenceSiteCredentials(t *testing.T) {
	keyring.MockInit()
	const jira, wiki = "https://jira.example", "https://wiki.example"
	t.Setenv("JIRA_USER", "jira")
	t.Setenv("JIRA_TOKEN", "jira-fixture")
	t.Setenv("CONFLUENCE_USER", "")
	t.Setenv("CONFLUENCE_TOKEN", "")
	if _, err := LoadConfluence(wiki, jira); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("separate site without credentials: %v", err)
	}
	stored := Atlassian{Site: wiki, Email: "wiki", Token: "wiki-fixture"}
	if err := SaveAtlassian(stored); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadConfluence(wiki, jira); err != nil || got != stored {
		t.Fatal("separate site did not use its keychain entry", err)
	}
	if got, err := LoadConfluence(jira+"/", jira); err != nil || got.Email != "jira" || got.Token != "jira-fixture" {
		t.Fatal("shared site lost its Jira environment credentials", err)
	}
	t.Setenv("JIRA_USER", "")
	t.Setenv("JIRA_TOKEN", "")
	if got, err := LoadConfluence(wiki+"/", wiki); err != nil || got != stored {
		t.Fatal("shared site lost its keychain entry with a trailing slash", err)
	}
	t.Setenv("CONFLUENCE_USER", "docs")
	if _, err := LoadConfluence(jira, jira); err == nil {
		t.Fatal("accepted partial knowledge credentials")
	}
	t.Setenv("CONFLUENCE_TOKEN", "docs-fixture")
	if got, err := LoadConfluence(wiki, jira); err != nil || got.Email != "docs" || got.Token != "docs-fixture" {
		t.Fatal("knowledge credentials did not override the keychain", err)
	}
}
