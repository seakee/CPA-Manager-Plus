package upgrade

import "golang.org/x/sys/unix"

// publishDatabase atomically moves staging without replacing an arriving target.
func publishDatabase(from, to string) error {
	return unix.RenamexNp(from, to, unix.RENAME_EXCL)
}
