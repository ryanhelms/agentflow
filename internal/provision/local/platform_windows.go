package local

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows permissions are enforced by the DACL, independent of the POSIX
// write bits synthesized by FileMode. Keep the selected image replaceable
// for repair. A running executable can be upgraded under a new digest.
const selectionName = "current.txt"
const exeSuffix = ".exe"
const executableMode = 0o666

func privateMode(fs.FileMode) bool { return true }

func trustedOwner(path string, before fs.FileInfo) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return false
	}
	h := windows.Handle(file.Fd())
	var attrs windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &attrs) != nil || attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	who, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(who.User.Sid) {
		return false
	}
	return restrictedACL(sd, owner)
}

func restrictedACL(sd *windows.SECURITY_DESCRIPTOR, owner *windows.SID) bool {
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return false
	}
	const write = windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE |
		windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | 0x40 // FILE_DELETE_CHILD
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, uint32(i), &ace) != nil {
			return false
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			if ace.Mask&write == 0 {
				continue
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) //nolint:gosec // GetAce returns a validated native ACCESS_ALLOWED_ACE with an inline SID at SidStart.
			if sid.Equals(owner) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
				continue
			}
			if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 && sid.IsWellKnown(windows.WinCreatorOwnerSid) {
				continue
			}
		}
		return false
	}
	return true
}

func openNonblocking(dir *os.Root, name string) (*os.File, error) { return dir.Open(name) }

func readPointer(dir *os.Root) (string, error) {
	f, err := openRegular(dir, selectionName, true)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 256))
	return string(data), err
}

func stagePointer(dir *os.Root, name, stage string) error {
	f, err := dir.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(name)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		_ = dir.Remove(stage)
	}
	return err
}

// Lock a persistent file; closing the handle releases the byte-range lock,
// including after an interrupted installer.
func lockDirectory(ctx context.Context, dir *os.Root) (*os.File, error) {
	f, err := dir.OpenFile(".install.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for ctx.Err() == nil {
		err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	_ = f.Close()
	return nil, ctx.Err()
}
