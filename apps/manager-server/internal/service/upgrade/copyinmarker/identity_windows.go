//go:build windows

package copyinmarker

import "golang.org/x/sys/windows"

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
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
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
