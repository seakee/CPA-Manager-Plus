//go:build !windows

package update

func restrictPrivatePath(_ string, _ bool) error {
	return nil
}
