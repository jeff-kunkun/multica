package daemon

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// A local_directory task runs in a private copy-on-write clone of the user's
// folder and its changes are merged back when it ends (DENE-2159). Several
// tasks can then work on one folder at once without editing each other's
// files mid-run, and nobody queues.
//
// Layout, next to the user's folder so the clone stays on the same volume:
//
//	<parent>/.multica-runs/<folder>/<key>/base   snapshot taken at start
//	<parent>/.multica-runs/<folder>/<key>/work   the agent's working directory
//	<parent>/.multica-runs/<folder>/_backup/...  originals replaced on merge-back, kept 7 days
//	<parent>/.multica-runs/<folder>/<key>.conflict-<task>  a copy whose merge-back conflicted, kept
//
// <key> is stable per (issue or chat, agent), so a resumed session finds the
// same working directory path on its next run.
//
// Merge-back never picks a winner: a file changed both in the folder and by
// the run that does not merge line by line keeps the folder's version, the
// run's version is saved next to it as <file>.multica-conflict-<task>, and the
// task stops for a person (see localCopyOutcome).

var errCloneUnsupported = errors.New("copy-on-write clone not supported on this volume")

const (
	// localCopyDisableFileName under ~/.multica switches local_directory tasks
	// back to running directly in the user's folder: every folder when the
	// file is empty, otherwise only the folders it lists, one absolute path per
	// line. Checked per task, so editing it takes effect on the next run
	// without a restart.
	localCopyDisableFileName = "local-directory-direct"
	localCopyRunsDirName     = ".multica-runs"
	localCopyBackupRetention = 7 * 24 * time.Hour
	// localCopyConflictSuffix names the run's version of a file merge-back
	// could not reconcile with the folder's: <file>.multica-conflict-<task>.
	localCopyConflictSuffix = ".multica-conflict-"
	// localCopyOwnerFileName in a copy directory records the task that made it.
	localCopyOwnerFileName = "task-id"
	// Files above this size, or with NUL bytes, are not line-merged.
	localCopyMergeMaxBytes = 8 << 20
)

var (
	localCopyFolderLocks sync.Map // orig path -> *sync.Mutex; serialises snapshot vs merge-back
	localCopyActiveDirs  sync.Map // copy dir -> struct{}; a key in use by a running task
)

type localRunCopy struct {
	Orig     string // the user's folder
	RunsRoot string // <parent>/.multica-runs/<folder>
	Dir      string // RunsRoot/<key>
	Base     string // Dir/base
	Work     string // Dir/work
	TaskID   string
}

// localCopySupported reports whether in-place tasks on orig run in a private
// copy. The answer decides, before the run starts, whether the task skips the
// per-path mutex. A variable so the mutex tests can switch the copy off.
var localCopySupported = probeLocalCopy

// localCopyDisabled reports whether the user turned the private copy off for
// orig. Listing a folder suits one too large to clone quickly: a folder of a
// million files takes minutes to clone and merge back on every run.
func localCopyDisabled(orig string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(home, ".multica", localCopyDisableFileName))
	if err != nil {
		return false
	}
	listed := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		listed = true
		if filepath.Clean(line) == filepath.Clean(orig) {
			return true
		}
	}
	return !listed
}

func localCopyFolderLock(orig string) *sync.Mutex {
	mu, _ := localCopyFolderLocks.LoadOrStore(orig, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func localCopyKey(task Task) string {
	agent := idTail(task.AgentID)
	switch {
	case task.IssueID != "":
		return "issue-" + task.IssueID + "-" + agent
	case task.ChatSessionID != "":
		return "chat-" + task.ChatSessionID + "-" + agent
	default:
		return "task-" + task.ID
	}
}

// prepareLocalRunCopy clones orig for this task. It returns (nil, nil) when
// the copy is switched off or the volume cannot clone; the caller then runs
// in orig directly, as before.
func prepareLocalRunCopy(task Task, orig string, logger *slog.Logger) (*localRunCopy, error) {
	if localCopyDisabled(orig) {
		return nil, nil
	}
	runsRoot := filepath.Join(filepath.Dir(orig), localCopyRunsDirName, filepath.Base(orig))
	dir := filepath.Join(runsRoot, localCopyKey(task))
	if _, busy := localCopyActiveDirs.LoadOrStore(dir, struct{}{}); busy {
		dir = dir + "-" + task.ID
		localCopyActiveDirs.Store(dir, struct{}{})
	}
	c := &localRunCopy{
		Orig:     orig,
		RunsRoot: runsRoot,
		Dir:      dir,
		Base:     filepath.Join(dir, "base"),
		Work:     filepath.Join(dir, "work"),
		TaskID:   task.ID,
	}
	ok := false
	defer func() {
		if !ok {
			localCopyActiveDirs.Delete(dir)
		}
	}()

	mu := localCopyFolderLock(orig)
	mu.Lock()
	defer mu.Unlock()

	sweepLocalCopyTrash(runsRoot)
	if _, err := os.Stat(dir); err == nil {
		// A previous run on this key did not finish merging back (daemon
		// killed mid-run, or a merge error). Merge it now, before snapshotting
		// the folder again; if that fails, set it aside rather than lose it.
		leftover := &localRunCopy{Orig: orig, RunsRoot: runsRoot, Dir: dir, Base: c.Base, Work: c.Work, TaskID: readLocalCopyOwner(dir)}
		label := leftover.TaskID
		if label == "" {
			label = "leftover"
		}
		report, err := leftover.landLocked(logger, label)
		switch {
		case err != nil:
			stale := dir + ".stale-" + time.Now().Format("20060102-150405")
			logger.Warn("local_directory: leftover copy could not be merged back; set aside", "copy", stale, "error", err)
			if rerr := os.Rename(dir, stale); rerr != nil {
				return nil, fmt.Errorf("set aside leftover copy: %w", rerr)
			}
		case len(report.conflicts) > 0:
			kept, kerr := leftover.keepForConflicts(label)
			logger.Warn("local_directory: leftover copy conflicted with the folder; both versions kept",
				"copy", kept, "conflicts", truncateList(conflictPaths(report.conflicts)), "error", kerr)
			if kerr != nil {
				return nil, fmt.Errorf("set aside conflicted leftover copy: %w", kerr)
			}
		default:
			c.trash()
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// Remember whose copy this is, so a leftover merged by a later run names
	// its conflict files after the task that made the changes.
	_ = os.WriteFile(filepath.Join(dir, localCopyOwnerFileName), []byte(task.ID), 0o644)
	if err := cloneTree(orig, c.Base); err != nil {
		_ = os.RemoveAll(dir)
		if errors.Is(err, errCloneUnsupported) {
			// Leave no empty .multica-runs behind on volumes that cannot clone.
			_ = os.Remove(runsRoot)
			_ = os.Remove(filepath.Dir(runsRoot))
			return nil, nil
		}
		return nil, fmt.Errorf("clone folder: %w", err)
	}
	if err := cloneTree(c.Base, c.Work); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("clone working copy: %w", err)
	}
	ok = true
	return c, nil
}

// localCopyConflict is one path merge-back did not write because the folder
// changed it after the snapshot and the run changed it too, and the two
// versions did not merge cleanly. The folder's version is left as it is.
type localCopyConflict struct {
	Path    string // relative to the folder
	SavedAs string // relative path of the run's version; empty when the run deleted the file
}

// localCopyOutcome is what Finish reports back to the task.
type localCopyOutcome struct {
	Folder    string
	Conflicts []localCopyConflict
	// KeptCopy is the copy directory left on disk because of the conflicts
	// (or because merge-back failed), so the run's full tree stays reachable.
	KeptCopy string
}

// Finish merges the run's changes back into the user's folder and removes the
// copy. A copy that could not be fully merged is kept; the next run on the
// same key retries it. A copy whose changes conflicted with the folder is
// kept under its own name, out of the next run's way, and the conflicts are
// returned so the task can stop for a person instead of reporting success.
func (c *localRunCopy) Finish(logger *slog.Logger) localCopyOutcome {
	defer localCopyActiveDirs.Delete(c.Dir)
	mu := localCopyFolderLock(c.Orig)
	mu.Lock()
	defer mu.Unlock()
	report, err := c.landLocked(logger, c.TaskID)
	out := localCopyOutcome{Folder: c.Orig, Conflicts: report.conflicts}
	if err != nil {
		logger.Warn("local_directory: merge-back incomplete; copy kept for the next run", "copy", c.Dir, "error", err)
		out.KeptCopy = c.Dir
		return out
	}
	if len(report.conflicts) > 0 {
		kept, kerr := c.keepForConflicts(c.TaskID)
		if kerr != nil {
			logger.Warn("local_directory: could not set the conflicted copy aside; left in place", "copy", c.Dir, "error", kerr)
			kept = c.Dir
		}
		logger.Warn("local_directory: merge-back stopped on conflicts; folder versions kept, run versions saved next to them",
			"folder", c.Orig, "conflicts", truncateList(conflictPaths(report.conflicts)), "copy", kept)
		out.KeptCopy = kept
		return out
	}
	c.trash()
	return out
}

// keepForConflicts moves the copy to a name of its own so the next run on the
// same key starts from a fresh snapshot instead of merging it again.
func (c *localRunCopy) keepForConflicts(label string) (string, error) {
	kept := c.Dir + ".conflict-" + idTail(label)
	if _, err := os.Lstat(kept); err == nil {
		kept += "-" + time.Now().Format("20060102-150405")
	}
	if err := os.Rename(c.Dir, kept); err != nil {
		return "", err
	}
	return kept, nil
}

// readLocalCopyOwner returns the task id recorded in a copy directory.
func readLocalCopyOwner(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, localCopyOwnerFileName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func conflictPaths(conflicts []localCopyConflict) []string {
	out := make([]string, 0, len(conflicts))
	for _, cf := range conflicts {
		out = append(out, cf.Path)
	}
	return out
}

// Note is the text the task reports when merge-back stopped on conflicts.
// Paths are relative to the folder and the copy is named relative to the
// folder's parent: absolute paths carry the account name into the issue.
func (o localCopyOutcome) Note() string {
	if len(o.Conflicts) == 0 {
		return ""
	}
	name := filepath.Base(o.Folder)
	var b strings.Builder
	fmt.Fprintf(&b, "本地目录合并冲突：%d 个文件在任务运行期间被别处改动，且与任务的改动无法自动合并。原文件未改动，任务版本已另存，请人工处理。\n\n", len(o.Conflicts))
	fmt.Fprintf(&b, "Merge-back conflict in local directory %q: %d file(s) were changed in the folder while this task ran, and the task's changes to them did not merge cleanly. The folder's version was left unchanged; the task's version needs a person to reconcile.\n", name, len(o.Conflicts))
	for i, cf := range o.Conflicts {
		if i == 20 {
			fmt.Fprintf(&b, "- ... %d more\n", len(o.Conflicts)-20)
			break
		}
		if cf.SavedAs == "" {
			fmt.Fprintf(&b, "- %s (the task deleted it; the folder's changed version was kept)\n", filepath.ToSlash(cf.Path))
			continue
		}
		fmt.Fprintf(&b, "- %s -> task version saved as %s\n", filepath.ToSlash(cf.Path), filepath.ToSlash(cf.SavedAs))
	}
	if o.KeptCopy != "" {
		if rel, err := filepath.Rel(filepath.Dir(o.Folder), o.KeptCopy); err == nil && !strings.HasPrefix(rel, "..") {
			fmt.Fprintf(&b, "The task's full working copy is kept at %s (next to %q).\n", filepath.ToSlash(rel), name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// trash moves the copy out of the way at once and deletes it in the
// background: a large tree takes seconds to delete and nothing waits on it.
func (c *localRunCopy) trash() {
	trashDir := filepath.Join(c.RunsRoot, "_trash")
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		return
	}
	target := filepath.Join(trashDir, randomHex(8))
	if err := os.Rename(c.Dir, target); err != nil {
		return
	}
	go func() { _ = os.RemoveAll(target) }()
}

func sweepLocalCopyTrash(runsRoot string) {
	trashDir := filepath.Join(runsRoot, "_trash")
	entries, err := os.ReadDir(trashDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		go func(p string) { _ = os.RemoveAll(p) }(filepath.Join(trashDir, e.Name()))
	}
}

// entrySig identifies a file system entry well enough to tell whether it
// changed: clones keep size, mtime and mode, so an untouched file compares
// equal to its snapshot without reading it.
type entrySig struct {
	mode  fs.FileMode
	size  int64
	mtime int64
	link  string
}

func (s entrySig) isDir() bool     { return s.mode.IsDir() }
func (s entrySig) isRegular() bool { return s.mode.IsRegular() }

func (s entrySig) equal(o entrySig) bool {
	if s.mode.Type() != o.mode.Type() {
		return false
	}
	if s.isDir() {
		return true
	}
	return s.mode == o.mode && s.size == o.size && s.mtime == o.mtime && s.link == o.link
}

func lstatSig(path string) (entrySig, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return entrySig{}, false, nil
	}
	if err != nil {
		return entrySig{}, false, err
	}
	sig := entrySig{mode: info.Mode(), size: info.Size(), mtime: info.ModTime().UnixNano()}
	if info.Mode()&fs.ModeSymlink != 0 {
		sig.link, _ = os.Readlink(path)
	}
	return sig, true, nil
}

// scanTree records every entry under root except git metadata: the root's
// .git is handled through refs, and nested repositories or worktrees keep
// their own .git untouched. nested lists the directories holding a .git.
func scanTree(root string) (entries map[string]entrySig, nested map[string]bool, err error) {
	entries = map[string]entrySig{}
	nested = map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.Name() == ".git" {
			if parent := filepath.Dir(rel); parent != "." {
				nested[parent] = true
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		sig := entrySig{mode: info.Mode(), size: info.Size(), mtime: info.ModTime().UnixNano()}
		if info.Mode()&fs.ModeSymlink != 0 {
			sig.link, _ = os.Readlink(path)
		}
		entries[rel] = sig
		return nil
	})
	return entries, nested, err
}

// landReport is what one merge-back did, for the log.
type landReport struct {
	label     string
	written   int
	deleted   int
	merged    []string            // both sides changed, lines merged cleanly
	conflicts []localCopyConflict // both sides changed and did not merge; the folder's version kept
	skipped   []string            // new nested repositories / worktrees created inside the copy
	backupDir string
}

// landLocked merges Work back into Orig. Caller holds the folder lock. The
// report is never nil, so conflicts found before an error are still seen.
func (c *localRunCopy) landLocked(logger *slog.Logger, label string) (*landReport, error) {
	report := &landReport{
		label:     label,
		backupDir: filepath.Join(c.RunsRoot, "_backup", time.Now().Format("20060102-150405")+"-"+idTail(label)),
	}
	pruneLocalCopyBackups(filepath.Join(c.RunsRoot, "_backup"))
	baseEntries, baseNested, err := scanTree(c.Base)
	if err != nil {
		return report, fmt.Errorf("scan snapshot: %w", err)
	}
	workEntries, workNested, err := scanTree(c.Work)
	if err != nil {
		return report, fmt.Errorf("scan working copy: %w", err)
	}

	var newNested []string
	for dir := range workNested {
		if !baseNested[dir] {
			newNested = append(newNested, dir)
		}
	}
	sort.Strings(newNested)
	report.skipped = newNested
	underNewNested := func(rel string) bool {
		for _, dir := range newNested {
			if rel == dir || strings.HasPrefix(rel, dir+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}

	var errs []error
	excluded, gitErr := c.landGitRefs(label)
	if gitErr != nil {
		errs = append(errs, gitErr)
	}

	var changed []string
	for rel, w := range workEntries {
		b, inBase := baseEntries[rel]
		if inBase && (w.equal(b) || sameRegularContent(filepath.Join(c.Work, rel), filepath.Join(c.Base, rel), w, b)) {
			continue
		}
		changed = append(changed, rel)
	}
	for rel := range baseEntries {
		if _, inWork := workEntries[rel]; !inWork {
			changed = append(changed, rel)
		}
	}
	sort.Strings(changed)

	var removedDirs []string
	for _, rel := range changed {
		if underNewNested(rel) || excluded[rel] {
			continue
		}
		w, inWork := workEntries[rel]
		b, inBase := baseEntries[rel]
		if !inWork && b.isDir() {
			removedDirs = append(removedDirs, rel)
			continue
		}
		if err := c.landPath(rel, w, inWork, b, inBase, report); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rel, err))
		}
	}
	// Deepest first, and only once empty: a folder the run removed may have
	// gained files from someone else in the meantime.
	sort.Slice(removedDirs, func(i, j int) bool { return len(removedDirs[i]) > len(removedDirs[j]) })
	for _, rel := range removedDirs {
		_ = os.Remove(filepath.Join(c.Orig, rel))
	}

	if report.written+report.deleted > 0 || len(report.conflicts) > 0 || len(report.skipped) > 0 || len(errs) > 0 {
		logger.Info("local_directory: merged run copy back",
			"folder", c.Orig,
			"written", report.written,
			"deleted", report.deleted,
			"merged", truncateList(report.merged),
			"conflicts", truncateList(conflictPaths(report.conflicts)),
			"skipped_nested_repos", truncateList(report.skipped),
			"backup", report.backupDir,
			"errors", len(errs),
		)
	}
	return report, errors.Join(errs...)
}

func (c *localRunCopy) landPath(rel string, w entrySig, inWork bool, b entrySig, inBase bool, report *landReport) error {
	origPath := filepath.Join(c.Orig, rel)
	workPath := filepath.Join(c.Work, rel)
	o, inOrig, err := lstatSig(origPath)
	if err != nil {
		return err
	}
	// Someone else changed this path in the folder while the run worked.
	concurrent := inOrig != inBase || (inOrig && !o.equal(b) && !sameRegularContent(origPath, filepath.Join(c.Base, rel), o, b))

	if !inWork {
		if !inOrig {
			return nil
		}
		if concurrent {
			// The run deleted a file someone else changed meanwhile: their
			// change is not the run's to throw away.
			report.conflicts = append(report.conflicts, localCopyConflict{Path: rel})
			return nil
		}
		if err := c.backup(origPath, rel, report); err != nil {
			return err
		}
		report.deleted++
		return os.RemoveAll(origPath)
	}
	if w.isDir() {
		if inOrig && o.isDir() {
			return nil
		}
		if inOrig {
			if err := c.backup(origPath, rel, report); err != nil {
				return err
			}
			if err := os.RemoveAll(origPath); err != nil {
				return err
			}
		}
		return os.MkdirAll(origPath, w.mode.Perm())
	}

	if concurrent {
		if inOrig && sameEntryContent(workPath, origPath, w, o) {
			// Both sides arrived at the same result.
			return nil
		}
		if inOrig && inBase && w.isRegular() && o.isRegular() && b.isRegular() {
			merged, clean, merr := mergeFileLines(workPath, filepath.Join(c.Base, rel), origPath)
			if merr == nil && clean {
				if err := c.backup(origPath, rel, report); err != nil {
					return err
				}
				if err := writeFileAtomic(origPath, merged, w.mode.Perm()); err != nil {
					return err
				}
				report.merged = append(report.merged, rel)
				report.written++
				return nil
			}
		}
		// Changed on both sides and not mergeable line by line: never pick a
		// winner. The folder keeps its version and the run's is saved next to
		// it for a person to reconcile.
		return c.saveConflict(rel, workPath, report)
	}
	if inOrig {
		if err := c.backup(origPath, rel, report); err != nil {
			return err
		}
		if o.isDir() {
			if err := os.RemoveAll(origPath); err != nil {
				return err
			}
		}
	}
	if err := replaceWithClone(workPath, origPath); err != nil {
		return err
	}
	report.written++
	return nil
}

// saveConflict puts the run's version of rel next to the folder's as
// <rel>.multica-conflict-<task>, leaving the folder's version untouched.
func (c *localRunCopy) saveConflict(rel, workPath string, report *landReport) error {
	saved := rel + localCopyConflictSuffix + idTail(report.label)
	dst := filepath.Join(c.Orig, saved)
	if s, ok, err := lstatSig(dst); err != nil {
		return err
	} else if ok && s.isDir() {
		// A leftover landed again: replace the earlier saved copy.
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
	}
	if err := replaceWithClone(workPath, dst); err != nil {
		return fmt.Errorf("save conflicting version: %w", err)
	}
	report.conflicts = append(report.conflicts, localCopyConflict{Path: rel, SavedAs: saved})
	return nil
}

// sameEntryContent reports whether two non-directory entries hold the same
// thing: identical bytes for files, the same target for symlinks.
func sameEntryContent(a, b string, sa, sb entrySig) bool {
	if sa.mode.Type() != sb.mode.Type() {
		return false
	}
	if sa.mode&fs.ModeSymlink != 0 {
		return sa.link == sb.link
	}
	return sameRegularContent(a, b, sa, sb)
}

// backup keeps the user's current version of rel before merge-back replaces
// or deletes it.
func (c *localRunCopy) backup(origPath, rel string, report *landReport) error {
	dst := filepath.Join(report.backupDir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return nil
	}
	if err := cloneTree(origPath, dst); err != nil {
		return fmt.Errorf("backup before overwrite: %w", err)
	}
	return nil
}

func replaceWithClone(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(dst), ".multica-land-"+randomHex(6))
	if err := cloneTree(src, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func writeFileAtomic(dst string, data []byte, perm fs.FileMode) error {
	tmp := filepath.Join(filepath.Dir(dst), ".multica-land-"+randomHex(6))
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// sameRegularContent catches files rewritten with identical bytes (the run's
// brief is written into CLAUDE.md and restored on exit, bumping its mtime).
func sameRegularContent(a, b string, sa, sb entrySig) bool {
	if !sa.isRegular() || !sb.isRegular() || sa.size != sb.size || sa.size > localCopyMergeMaxBytes {
		return false
	}
	da, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	db, err := os.ReadFile(b)
	if err != nil {
		return false
	}
	return bytes.Equal(da, db)
}

// mergeFileLines three-way merges text files with `git merge-file`. clean is
// false when both sides changed the same lines or the files are not text.
func mergeFileLines(run, base, current string) (merged []byte, clean bool, err error) {
	for _, p := range []string{run, base, current} {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil, false, rerr
		}
		if len(data) > localCopyMergeMaxBytes || bytes.IndexByte(data, 0) >= 0 {
			return nil, false, nil
		}
	}
	out, err := exec.Command("git", "merge-file", "-p", run, base, current).Output()
	if err == nil {
		return out, true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 && exitErr.ExitCode() < 128 {
		return nil, false, nil
	}
	return nil, false, err
}

// landGitRefs carries the run's commits back to the user's repository and
// returns the paths merge-back must leave alone.
//
// Each branch the run created or advanced is fetched into the original under
// the same name when that is a fast-forward and not the checked-out branch;
// otherwise it is kept under refs/multica/runs/<task>/. The user's checked-out
// branch never moves. If the run switched its copy to another branch (the PR
// flow), files that differ only because of that switch belong to the branch,
// not to the user's working tree, so they are excluded from merge-back.
func (c *localRunCopy) landGitRefs(label string) (map[string]bool, error) {
	if info, err := os.Stat(filepath.Join(c.Base, ".git")); err != nil || !info.IsDir() {
		return nil, nil
	}
	baseRefs, err := gitHeads(c.Base)
	if err != nil {
		return nil, err
	}
	workRefs, err := gitHeads(c.Work)
	if err != nil {
		return nil, err
	}
	var errs []error
	for ref, sha := range workRefs {
		if baseRefs[ref] == sha {
			continue
		}
		if _, ferr := gitOutput(c.Orig, "fetch", "--no-tags", "--quiet", c.Work, ref+":"+ref); ferr == nil {
			continue
		}
		keep := "refs/multica/runs/" + idTail(label) + "/" + strings.TrimPrefix(ref, "refs/heads/")
		if _, ferr := gitOutput(c.Orig, "fetch", "--no-tags", "--quiet", c.Work, "+"+ref+":"+keep); ferr != nil {
			errs = append(errs, fmt.Errorf("keep branch %s: %w", ref, ferr))
		}
	}

	baseHead, _ := gitOutput(c.Base, "symbolic-ref", "-q", "HEAD")
	workHead, _ := gitOutput(c.Work, "symbolic-ref", "-q", "HEAD")
	if baseHead == workHead {
		return nil, errors.Join(errs...)
	}
	baseSHA, berr := gitOutput(c.Base, "rev-parse", "HEAD")
	workSHA, werr := gitOutput(c.Work, "rev-parse", "HEAD")
	if berr != nil || werr != nil {
		return nil, errors.Join(append(errs, fmt.Errorf("resolve HEAD after branch switch"))...)
	}
	names, err := gitOutput(c.Work, "diff", "--name-only", "--no-renames", "-z", baseSHA, workSHA)
	if err != nil {
		return nil, errors.Join(append(errs, err)...)
	}
	excluded := map[string]bool{}
	for _, name := range strings.Split(names, "\x00") {
		if name != "" {
			excluded[filepath.FromSlash(name)] = true
		}
	}
	return excluded, errors.Join(errs...)
}

func gitHeads(dir string) (map[string]string, error) {
	out, err := gitOutput(dir, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if sha, ref, ok := strings.Cut(line, " "); ok {
			refs[ref] = sha
		}
	}
	return refs, nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func pruneLocalCopyBackups(backupRoot string) {
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-localCopyBackupRetention)
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(backupRoot, e.Name()))
		}
	}
}

// idTail keeps the random end of a UUIDv7 (its head is a timestamp).
func idTail(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func truncateList(items []string) []string {
	if len(items) > 20 {
		return append(items[:20:20], fmt.Sprintf("... %d more", len(items)-20))
	}
	return items
}
