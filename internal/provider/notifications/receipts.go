package notifications

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/nscaledev/agentflow/internal/provider"
)

// FileStore keeps immutable attempt and outcome records in Dir. All processes
// using it must share the same directory on a filesystem with atomic hard links.
// Records contain only the payload digest and the final state, when known.
type FileStore struct{ Dir string }

var _ ReceiptStore = FileStore{}

func (s FileStore) Claim(ctx context.Context, key ReceiptKey, fingerprint [sha256.Size]byte) (ReceiptState, error) {
	if err := os.Mkdir(s.Dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	// Sync the parent even if another writer just created the directory.
	if err := syncReceiptDirectory(filepath.Dir(s.Dir)); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return "", err
	}
	defer root.Close()

	id, digest := receiptID(key), hex.EncodeToString(fingerprint[:])
	for number := 1; ; number++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := attemptName(id, number)
		data, err := root.ReadFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			if err := publishReceipt(root, name, digest); !errors.Is(err, fs.ErrExist) {
				if err != nil {
					return "", err
				}
				return Claimed, nil
			}
			// A concurrent process won the exclusive link. Read its claim.
			data, err = root.ReadFile(name)
		}
		if err != nil {
			return "", err
		}
		if string(data) != digest {
			return "", provider.ErrConflict
		}
		state, err := readOutcome(root, name)
		if err != nil || state != Retryable {
			return state, err
		}
	}
}

func (s FileStore) Finish(ctx context.Context, key ReceiptKey, state ReceiptState) error {
	if state != Delivered && state != Retryable {
		return errors.New("invalid final notification receipt state")
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return err
	}
	defer root.Close()

	id := receiptID(key)
	for number := 1; ; number++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := attemptName(id, number)
		if _, err := root.Stat(name); err != nil {
			return err
		}
		current, err := readOutcome(root, name)
		if err != nil {
			return err
		}
		switch current {
		case Delivered:
			return errors.New("notification receipt is already finalized")
		case Unconfirmed:
			return publishReceipt(root, name+".outcome", string(state))
		}
	}
}

func attemptName(id string, number int) string { return id + "." + strconv.Itoa(number) + ".claim" }

func readOutcome(root *os.Root, name string) (ReceiptState, error) {
	data, err := root.ReadFile(name + ".outcome")
	if errors.Is(err, fs.ErrNotExist) {
		return Unconfirmed, nil
	}
	if err != nil {
		return "", err
	}
	state := ReceiptState(data)
	if state != Delivered && state != Retryable {
		return "", errors.New("invalid notification receipt outcome")
	}
	return state, nil
}

func publishReceipt(root *os.Root, name, content string) error {
	stage := "stage-" + rand.Text()
	f, err := root.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(stage) }()
	if _, err = f.WriteString(content); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := root.Link(stage, name); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return syncReceiptDirHandle(dir)
}

func syncReceiptDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return syncReceiptDirHandle(dir)
}

func syncReceiptDirHandle(dir *os.File) error {
	if runtime.GOOS == "windows" {
		// Windows supports flushing the staged file but not a directory handle.
		return nil
	}
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync notification receipt directory: %w", err)
	}
	return nil
}
