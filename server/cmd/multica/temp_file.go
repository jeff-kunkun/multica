package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/util"
)

var systemTempFileRoots = func() []string {
	roots := []string{os.TempDir(), os.Getenv("TMPDIR")}
	if os.PathSeparator == '/' {
		roots = append(roots, "/tmp")
	}
	return roots
}

func ownedSystemTempFile(path string) bool {
	abs, err := util.ResolveSymlinksBestEffort(path)
	if err != nil {
		return false
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() || !fileOwnedByCurrentUser(abs, info) {
		return false
	}
	for _, root := range systemTempFileRoots() {
		if root == "" {
			continue
		}
		base, err := util.ResolveSymlinksBestEffort(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(base, abs)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
