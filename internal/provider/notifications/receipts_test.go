package notifications

import (
	"crypto/sha256"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ryanhelms/agentflow/internal/provider"
)

func TestFileReceiptStoreConcurrentClaimAndReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "receipts")
	key := ReceiptKey{OperationID: "event-1", Destination: "team"}
	fingerprint := sha256.Sum256([]byte("payload"))
	claimRound := func() {
		t.Helper()
		const workers = 12
		var group sync.WaitGroup
		claims := make(chan ReceiptState, workers)
		for range workers {
			group.Go(func() {
				state, err := (FileStore{Dir: dir}).Claim(t.Context(), key, fingerprint)
				if err != nil {
					t.Errorf("claim: %v", err)
				}
				claims <- state
			})
		}
		group.Wait()
		close(claims)
		var owners int
		for state := range claims {
			switch state {
			case Claimed:
				owners++
			case Unconfirmed:
			default:
				t.Errorf("unexpected claim state %s", state)
			}
		}
		if owners != 1 {
			t.Fatalf("claim owners = %d, want 1", owners)
		}
	}

	claimRound()
	store := FileStore{Dir: dir}
	// A fresh instance cannot reclaim work abandoned by the original caller.
	if state, err := store.Claim(t.Context(), key, fingerprint); err != nil || state != Unconfirmed {
		t.Fatalf("abandoned attempt = %s, %v", state, err)
	}
	if err := store.Finish(t.Context(), key, Retryable); err != nil {
		t.Fatal(err)
	}
	changed := sha256.Sum256([]byte("changed payload"))
	if _, err := store.Claim(t.Context(), key, changed); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("changed payload = %v", err)
	}
	claimRound()
	if err := store.Finish(t.Context(), key, Delivered); err != nil {
		t.Fatal(err)
	}
	if state, err := store.Claim(t.Context(), key, fingerprint); err != nil || state != Delivered {
		t.Fatalf("final outcome = %s, %v", state, err)
	}
}
