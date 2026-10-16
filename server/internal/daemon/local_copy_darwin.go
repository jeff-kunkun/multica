//go:build darwin

package daemon

import (
	"errors"

	"golang.org/x/sys/unix"
)

// cloneTree makes a copy-on-write clone of src (a file or a whole directory
// tree) at dst. On APFS this is a single syscall that costs no disk space; any
// other filesystem reports errCloneUnsupported so the caller can run in place.
func cloneTree(src, dst string) error {
	err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EXDEV) {
		return errCloneUnsupported
	}
	return err
}
