package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// scratchRootName is the dot-directory under the workspaces root that holds
// every workspace's shared session folders. The task GC walks only
// non-dot children of the workspaces root and treats each as a workspace of
// task directories, so a session folder placed beside those would be judged
// as an unowned task dir. A dot prefix keeps the existing walk from seeing
// it at all. How long a session folder lives is not decided here.
const scratchRootName = ".scratch"

// SharedScratchRoot is the per-workspace directory a SharedScratch decision's
// relative path is joined onto. The server sends that path relative on
// purpose: which disk the daemon uses is the daemon's to choose.
func SharedScratchRoot(workspacesRoot, workspaceID string) (string, error) {
	if strings.TrimSpace(workspacesRoot) == "" {
		return "", fmt.Errorf("execenv: scratch root needs a workspaces root")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" || strings.Contains(workspaceID, "..") || strings.ContainsAny(workspaceID, `/\`) {
		return "", fmt.Errorf("execenv: workspace id %q is not a scratch-root name", workspaceID)
	}
	return filepath.Join(workspacesRoot, scratchRootName, workspaceID), nil
}

// SharedScratchDir joins a decision's relative session path onto the
// workspace scratch root. An absolute path, or one that climbs out of the
// root, is refused: the decision names a folder inside the scratch root, and
// a path that points somewhere else would make the agent cwd a directory the
// server never chose.
func SharedScratchDir(workspacesRoot, workspaceID, relative string) (string, error) {
	root, err := SharedScratchRoot(workspacesRoot, workspaceID)
	if err != nil {
		return "", err
	}
	rel := filepath.Clean(strings.TrimSpace(relative))
	if rel == "" || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("execenv: scratch path %q is not a relative session path", relative)
	}
	full := filepath.Join(root, rel)
	inside, err := filepath.Rel(root, full)
	if err != nil || !filepath.IsLocal(inside) {
		return "", fmt.Errorf("execenv: scratch path %q escapes the scratch root", relative)
	}
	return full, nil
}
