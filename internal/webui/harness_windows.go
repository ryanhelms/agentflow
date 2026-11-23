package webui

import (
	"context"
	"os"

	"golang.org/x/sys/windows"
)

func InteractiveHarness(_ context.Context, client string, _ bool) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(os.Getppid()))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var path [windows.MAX_LONG_PATH]uint16
	size := uint32(len(path))
	if windows.QueryFullProcessImageName(handle, 0, &path[0], &size) != nil {
		return false
	}
	return desktopHost(client, windows.UTF16ToString(path[:size]))
}
