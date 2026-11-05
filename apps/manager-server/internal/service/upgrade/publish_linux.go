package upgrade

import "golang.org/x/sys/unix"

// publishDatabase atomically moves staging without replacing an arriving target.
// Filesystems without no-replace rename support fail without a fallback.
func publishDatabase(from, to string) error {
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
}
