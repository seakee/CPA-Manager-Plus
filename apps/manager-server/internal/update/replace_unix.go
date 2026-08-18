//go:build !windows

package update

import "os"

func replaceRegularFile(source, destination string) error {
	return os.Rename(source, destination)
}
