package daemon

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/coderesolve"
)

// A machine's default working directory (DENE-2667 PR 8) is where a run goes
// when its project binds no folder and names no repository. The owner sets it
// on the server (`multica runtime update --default-dir`); the claim carries it
// and this daemon checks it is usable before running there.

// defaultDirSyncInterval is how often the daemon tells the server whether the
// default directory exists, so a path set from another device shows its state
// within a minute.
const defaultDirSyncInterval = time.Minute

// defaultDirAssignment is the run's cwd when the task carries a default
// directory and binds no folder. skipped explains a default that was sent but
// not used; the run then gets its usual scratch directory rather than failing,
// because nothing about the task itself named that folder.
func defaultDirAssignment(task Task, provider string) (a *localDirectoryAssignment, skipped string) {
	path := strings.TrimSpace(task.DefaultWorkDir)
	if path == "" || task.IsLeaderTask {
		return nil, ""
	}
	if task.CodeDecision != nil && task.CodeDecision.Kind() != coderesolve.KindSharedScratch {
		return nil, "the run has code to work in"
	}
	// The brief and .multica/ must stay out of the user's folder, which only
	// a runtime that can read them from a sidecar allows.
	if sharedModeBriefDelivery(provider) == sharedBriefUnsupported {
		return nil, "the " + provider + " runtime cannot keep its instructions outside the folder"
	}
	abs, err := normalizeLocalPath(path)
	if err != nil {
		return nil, err.Error()
	}
	if err := validateLocalPath(abs); err != nil {
		return nil, err.Error()
	}
	real, err := resolveRealPath(abs)
	if err != nil {
		return nil, err.Error()
	}
	return &localDirectoryAssignment{
		Ref:        localDirectoryRef{LocalPath: abs},
		AbsPath:    abs,
		RealPath:   real,
		DefaultDir: true,
	}, ""
}

// projectFolder is the assignment when it is the project's own folder, nil
// for the default directory. The brief's Code Source and `multica repo
// checkout` speak about the project's folder; the default one is neither.
func (a *localDirectoryAssignment) projectFolder() *localDirectoryAssignment {
	if a == nil || a.DefaultDir {
		return nil
	}
	return a
}

// defaultDirExists reports whether path is there as a directory: what the
// owner sees as `exists`. Whether a run may use it is a separate question that
// defaultDirAssignment answers before each run, so a folder that is there but
// blocked (the home directory, `/`) reports true and its runs still go to
// scratch. Only a stat: this runs every minute, and the user's folder should
// not see a probe file come and go that often.
func defaultDirExists(path string) bool {
	abs, err := normalizeLocalPath(path)
	if err != nil {
		return false
	}
	info, err := os.Stat(abs)
	return err == nil && info.IsDir()
}

// defaultDirLoop keeps the server's "does it exist" for this machine's default
// directory current. One runtime speaks for the machine: the setting is keyed
// per device, not per runtime.
func (d *Daemon) defaultDirLoop(ctx context.Context) {
	ticker := time.NewTicker(defaultDirSyncInterval)
	defer ticker.Stop()
	var path string
	var exists *bool
	for {
		path, exists = d.syncDefaultDir(ctx, path, exists)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// syncDefaultDir is one round: report what was last seen, and if the setting
// has changed, look at the new path and report it straight away. Errors wait
// for the next round; an older server without the endpoint just stays quiet.
func (d *Daemon) syncDefaultDir(ctx context.Context, path string, exists *bool) (string, *bool) {
	ids := d.allRuntimeIDs()
	if len(ids) == 0 {
		return path, exists
	}
	runtimeID := ids[0]
	if path != "" {
		found := defaultDirExists(path)
		exists = &found
	}
	current, err := d.client.SyncDefaultWorkDir(ctx, runtimeID, path, exists)
	if err != nil {
		d.logger.Debug("default dir sync failed", "error", err)
		return path, exists
	}
	if current == path {
		return path, exists
	}
	if current == "" {
		return "", nil
	}
	found := defaultDirExists(current)
	if _, err := d.client.SyncDefaultWorkDir(ctx, runtimeID, current, &found); err != nil {
		d.logger.Debug("default dir sync failed", "error", err)
	}
	return current, &found
}
