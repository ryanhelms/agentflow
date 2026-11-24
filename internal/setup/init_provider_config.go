package setup

import (
	"bytes"
	"errors"
	"os"
)

// SaveInitProviderConfig replaces only the source the guided initializer read.
// Existing permissions are preserved, including on retries after an interrupted init.
func SaveInitProviderConfig(root string, before, after []byte) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	const name = ".agentflow/config.yml"
	return withClientLock(dir, name, func() error {
		return publishAfter(dir, name, after, func() error {
			current, err := readProjectFile(dir, name)
			if err != nil {
				return err
			}
			if !bytes.Equal(current, before) {
				return errors.New("provider configuration changed during init; review the latest selections and retry")
			}
			return nil
		})
	})
}
