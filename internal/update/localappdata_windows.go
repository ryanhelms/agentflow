package update

import "golang.org/x/sys/windows"

// localAppData is the account's known local application data folder. Windows
// reads it from the user's profile, not from the LOCALAPPDATA variable, which a
// repository's environment can set.
func localAppData() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
}
