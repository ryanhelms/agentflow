//go:build !windows

package local

import (
	"io/fs"
	"os"
)

const selectionName = "current"
const exeSuffix = ""
const executableMode = 0o500

func readPointer(dir *os.Root) (string, error)           { return dir.Readlink(selectionName) }
func stagePointer(dir *os.Root, name, link string) error { return dir.Symlink(name, link) }
func privateMode(mode fs.FileMode) bool                  { return mode.Perm()&0o022 == 0 }
