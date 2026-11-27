//go:build !windows

package update

import "errors"

// localAppData exists only on Windows.
func localAppData() (string, error) {
	return "", errors.New("no local application data folder outside Windows")
}
