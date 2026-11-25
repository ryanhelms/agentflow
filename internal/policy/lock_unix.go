//go:build !windows

package policy

import (
	"os"
	"path/filepath"
)

func replaceLedgerFile(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	// Commit the renamed directory entry before allowing the protected effect.
	dir, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
