package upgrade

import "golang.org/x/sys/windows"

// publishDatabase uses same-volume move semantics without replacement or copying.
func publishDatabase(from, to string) error {
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, target, 0)
}
