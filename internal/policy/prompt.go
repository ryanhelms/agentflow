package policy

import (
	"errors"
	"os"

	"github.com/ryanhelms/agentflow/internal/filelock"
)

// OpenPrompt holds a kernel lease while a foreground CLI waits for a human.
// Machine prepare calls deliberately omit this step: their bearer and owner
// remain valid after the process that returned the preview exits.
func (g *Gate) OpenPrompt(id, token string) (func(), error) {
	var f *os.File
	path := g.recordPath(id) + ".prompt"
	err := g.locked(func() error {
		signed, bearer, err := g.credential(token)
		if err != nil || !bearer || signed != id {
			return ErrToken
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
		if r.State != pending || r.PromptOwner != g.owner {
			return ErrNotApproved
		}
		f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		locked, err := filelock.TryExclusive(f)
		if err != nil {
			return err
		}
		if !locked {
			return errors.New("this confirmation prompt is already open")
		}
		r.InteractivePrompt = true
		return g.persist(&r)
	})
	if err != nil {
		if f != nil {
			_ = f.Close()
		}
		return nil, err
	}
	return func() { _ = f.Close(); _ = os.Remove(path) }, nil
}

// The caller holds the journal lock. A foreground reader that exited (including
// SIGKILL) no longer owns the file lock and cannot receive an answer.
func (g *Gate) abandonedPrompt(id string) (bool, error) {
	path := g.recordPath(id) + ".prompt"
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	acquired, err := filelock.TryExclusive(f)
	_ = f.Close()
	if acquired {
		_ = os.Remove(path)
	}
	return acquired, err
}
