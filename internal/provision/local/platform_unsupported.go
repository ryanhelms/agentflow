//go:build !darwin && !linux

package local

import (
	"context"
	"io/fs"
	"os"
)

// New rejects unsupported platforms before these functions are reached.
func ownedByEffectiveUser(fs.FileInfo) bool { return false }

func openNonblocking(*os.Root, string) (*os.File, error) { return nil, ErrUnsupported }

func lockDirectory(context.Context, *os.Root) (*os.File, error) { return nil, ErrUnsupported }
