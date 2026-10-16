//go:build !darwin

package daemon

// cloneTree is APFS-only for now; elsewhere local_directory tasks keep running
// directly in the user's folder.
func cloneTree(src, dst string) error {
	return errCloneUnsupported
}
