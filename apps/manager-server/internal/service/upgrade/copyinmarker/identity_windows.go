//go:build windows

package copyinmarker

import (
	"errors"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrChangeStampUnavailable is returned when the platform cannot report a
// file's metadata change timestamp (ctime / ChangeTime).
var ErrChangeStampUnavailable = errors.New("file change time is unavailable")

// identity reports the volume serial and file index Windows assigned the file.
// Both survive a rename and an in-place rewrite, which is what the marker
// needs: the migration rewrites the database at the same path, and only a
// swapped-in file changes its file index.
func identity(path string) (FileIdentity, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return FileIdentity{}, err
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return FileIdentity{}, err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return FileIdentity{}, err
	}
	return FileIdentity{
		Device: uint64(information.VolumeSerialNumber),
		Inode:  uint64(information.FileIndexHigh)<<32 | uint64(information.FileIndexLow),
	}, nil
}

// ChangeStamp reports FILE_BASIC_INFO.ChangeTime for a file or directory
// (opened with FILE_FLAG_BACKUP_SEMANTICS), matching
// internal/service/usage/archive_files_windows.go.
func ChangeStamp(path string) (string, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var basic struct {
		CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
		FileAttributes                                          uint32
	}
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil {
		return "", ErrChangeStampUnavailable
	}
	if basic.ChangeTime == 0 {
		return "", ErrChangeStampUnavailable
	}
	return strconv.FormatInt(basic.ChangeTime, 10), nil
}
