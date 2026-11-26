//go:build windows

package manifest_test

import "errors"

func mkfifo(string) error { return errors.New("no named pipes on Windows") }
