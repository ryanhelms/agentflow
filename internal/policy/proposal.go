package policy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ProposalNonce supplies a stable provider operation identity when the provider
// must know the destination before the human can review the final snapshot.
// A new intentional create following a completed one gets a new identity.
// The full gate check still binds the concrete provider target and payload.
func (g *Gate) ProposalNonce(class Class, action string, proposal any, credential string) (string, error) {
	key, err := g.fingerprint(class, action, nil, proposal)
	if err != nil {
		return "", err
	}
	var nonce string
	err = g.locked(func() error {
		if credential != "" {
			id, _, err := g.credential(credential)
			if err != nil {
				return err
			}
			r, err := g.load(id)
			if err != nil {
				return err
			}
			if err = g.refresh(&r); err != nil {
				return err
			}
			if err = stateError(r.State); err != nil {
				return err
			}
			if r.ProposalKey != key || r.ProposalNonce == "" {
				return ErrChanged
			}
			nonce = r.ProposalNonce
			return nil
		}
		ref, err := os.ReadFile(filepath.Join(g.directory(), "proposal-"+key))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if len(ref) > 0 {
			r, err := g.load(string(ref))
			if err != nil {
				return err
			}
			if err = g.refresh(&r); err != nil {
				return err
			}
			if r.State == pending || r.State == approved {
				nonce = r.ProposalNonce
				return nil
			}
		}
		mac := hmac.New(sha256.New, g.secret)
		fmt.Fprintf(mac, "proposal\x00%s\x00%s", key, ref)
		nonce = hex.EncodeToString(mac.Sum(nil)[:16])
		return nil
	})
	return nonce, err
}

// BindProposal associates the provider's precomputed destination with the
// receipt before the caller returns a preview or exposes its bearer.
func (g *Gate) BindProposal(class Class, action string, proposal any, id, nonce string) error {
	key, err := g.fingerprint(class, action, nil, proposal)
	if err != nil {
		return err
	}
	return g.locked(func() error {
		r, err := g.load(id)
		if err != nil {
			return err
		}
		if r.ProposalKey != "" && (r.ProposalKey != key || r.ProposalNonce != nonce) {
			return ErrChanged
		}
		if r.ProposalKey == "" {
			r.ProposalKey, r.ProposalNonce = key, nonce
			if err = g.persist(&r); err != nil {
				return err
			}
		}
		ptr := filepath.Join(g.directory(), "proposal-"+key)
		previous, err := os.ReadFile(ptr)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if len(previous) > 0 {
			if string(previous) == id {
				return nil
			}
			prior, err := g.load(string(previous))
			if err != nil {
				return err
			}
			if prior.IssuedAt.After(r.IssuedAt) {
				return nil
			}
		}
		return atomicFile(ptr, []byte(id))
	})
}
