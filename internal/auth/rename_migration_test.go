package auth

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestLegacyAtlassianCredentialFallback(t *testing.T) {
	keyring.MockInit()
	const site = "https://jira.example.invalid"
	const legacyRecord = `{"site":"https://jira.example.invalid","email":"old@example.invalid","token":"legacy-fixture"}`
	if err := keyring.Set("devflow", "atlassian:"+site, legacyRecord); err != nil {
		t.Fatal(err)
	}
	credential, err := storedAtlassian(site)
	if err != nil || credential.Email != "old@example.invalid" {
		t.Fatalf("failed to load the legacy keyring entry: %v", err)
	}
	if err := SaveAtlassian(Atlassian{Site: site, Email: "new@example.invalid", Token: "new-fixture"}); err != nil {
		t.Fatal(err)
	}
	credential, err = storedAtlassian(site)
	if err != nil || credential.Email != "new@example.invalid" {
		t.Fatalf("current keyring entry should take precedence: %v", err)
	}
	if err := DeleteAtlassian(site); err != nil {
		t.Fatal(err)
	}
	if _, err := storedAtlassian(site); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("logout should remove both entries, got %v", err)
	}
}
