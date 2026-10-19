//go:build darwin

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

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

// localCopyProbes caches, per parent directory, whether a clone made next to a
// folder there succeeds.
var localCopyProbes sync.Map // parent dir -> bool

// probeLocalCopy answers localCopySupported with a real clone of a scratch
// file where the copies live (orig's parent directory).
func probeLocalCopy(orig string) bool {
	if localCopyDisabled(orig) {
		return false
	}
	parent := filepath.Dir(orig)
	if v, ok := localCopyProbes.Load(parent); ok {
		return v.(bool)
	}
	ok := false
	if f, err := os.CreateTemp(parent, ".multica-clone-probe-"); err == nil {
		src := f.Name()
		f.Close()
		dst := src + "-c"
		ok = cloneTree(src, dst) == nil
		_ = os.Remove(dst)
		_ = os.Remove(src)
	}
	localCopyProbes.Store(parent, ok)
	return ok
}
