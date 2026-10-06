//go:build windows

package hotswap

import (
	"errors"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readFileShared reads path with FILE_SHARE_DELETE so a concurrent
// WriteHandoffState in another process can still replace the file by
// rename. os.ReadFile omits FILE_SHARE_DELETE on Windows, which makes that
// rename fail with "Access is denied" for as long as the file is open.
func readFileShared(path string) ([]byte, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	return io.ReadAll(f)
}

// fileRenameInfo mirrors FILE_RENAME_INFO (Flags variant) so field offsets
// match the C layout on every architecture.
type fileRenameInfo struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

const (
	fileRenameInfoEx              = 22 // FILE_INFO_BY_HANDLE_CLASS FileRenameInfoEx
	fileRenameFlagReplaceIfExists = 0x1
	fileRenameFlagPOSIXSemantics  = 0x2
)

// replaceFile renames src over dst. It uses POSIX rename semantics, which
// replace dst even while readers have it open with FILE_SHARE_DELETE (see
// readFileShared); plain MoveFileEx (os.Rename) fails with "Access is
// denied" in that case. Falls back to os.Rename where FileRenameInfoEx is
// unsupported (pre-1709 Windows, some filesystems).
func replaceFile(src, dst string) error {
	if err := posixRename(src, dst); err == nil ||
		!(errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
			errors.Is(err, windows.ERROR_INVALID_FUNCTION)) {
		return err
	}
	return os.Rename(src, dst)
}

func posixRename(src, dst string) error {
	srcp, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	name, err := windows.UTF16FromString(dst)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	h, err := windows.CreateFile(srcp, windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	defer windows.CloseHandle(h)

	nameOff := unsafe.Offsetof(fileRenameInfo{}.FileName)
	buf := make([]byte, nameOff+uintptr(len(name))*2) // name includes its NUL
	info := (*fileRenameInfo)(unsafe.Pointer(&buf[0]))
	info.Flags = fileRenameFlagReplaceIfExists | fileRenameFlagPOSIXSemantics
	info.FileNameLength = uint32((len(name) - 1) * 2) // bytes, without NUL
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[nameOff])), len(name)), name)

	if err := windows.SetFileInformationByHandle(h, fileRenameInfoEx, &buf[0], uint32(len(buf))); err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	return nil
}

// isRenameContention reports whether a rename failed only because another
// handle has the target open (a reader without FILE_SHARE_DELETE, such as
// an older llamactl or an antivirus scan) and is worth retrying.
func isRenameContention(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
