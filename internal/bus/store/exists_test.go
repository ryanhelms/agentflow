package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExistsOnlyAfterTheOwnerCreatesTheStore(t *testing.T) {
	common := t.TempDir()
	if Exists(common) {
		t.Fatal("a fresh repository has a store")
	}
	if _, err := os.Stat(filepath.Join(common, "agentflow")); !os.IsNotExist(err) {
		t.Fatalf("Exists created files: %v", err)
	}
	s, err := Open(context.Background(), common, RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !Exists(common) {
		t.Fatal("no store after the owner opened it")
	}
}
