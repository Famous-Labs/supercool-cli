//go:build windows

package download

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Windows has no O_NOFOLLOW. Fresh writes use O_EXCL (CREATE_NEW never
// follows a link); appends open the reparse point itself (no following) and
// refuse it if it is one.
const oNoFollow = 0

// openAppend opens an existing partial for appending without following a
// symlink or junction: FILE_FLAG_OPEN_REPARSE_POINT opens the link itself,
// and a handle whose attributes say reparse point is refused before any
// byte is written.
func openAppend(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.FILE_APPEND_DATA|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(h)
		return nil, errors.New("refusing to write through a symlink")
	}
	return os.NewFile(uintptr(h), path), nil
}
