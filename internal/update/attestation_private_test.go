package update

import (
	"context"
	"path/filepath"
	"testing"
)

func TestVerifyAttestationSkipsPrivateReleases(t *testing.T) {
	t.Setenv("AGENTFLOW_INSECURE_SKIP_ATTESTATION", "")
	saved := privateRepo
	t.Cleanup(func() { privateRepo = saved })
	privateRepo = func(context.Context) bool { return true }
	if err := VerifyAttestation(context.Background(), filepath.Join(t.TempDir(), "missing.tar.gz")); err != nil {
		t.Fatalf("private release still required an attestation: %v", err)
	}
}
