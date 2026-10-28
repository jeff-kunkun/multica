package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/multica-ai/multica/server/internal/coderesolve"
)

// localDirectoryResourceType is the project_resource discriminator the daemon
// looks for when deciding whether a task should run against an existing
// user directory rather than a fresh checkout. Mirrors the server-side
// constant — keep in sync if the type string is ever renamed.
const localDirectoryResourceType = "local_directory"

// localDirectoryRef mirrors the server-side ref shape for local_directory
// project resources. Defined locally so the daemon does not have to import
// the server handler package.
type localDirectoryRef struct {
	LocalPath string `json:"local_path"`
	DaemonID  string `json:"daemon_id"`
	Label     string `json:"label,omitempty"`
	// RealPath is the directory's identity as the machine reported it at pick
	// time; empty on rows written before it existed. The daemon re-resolves
	// the real path itself (resolveRealPath), so this field is carried for
	// identity/display only.
	RealPath string `json:"real_path,omitempty"`
	// RepoKey is the normalized identity of the repository the directory
	// holds, or "" when it holds none or nobody looked.
	RepoKey string `json:"repo_key,omitempty"`
}

// localDirectoryAssignment is the resolved view of a task's local_directory
// resource: the absolute path the daemon will use as the agent's workdir,
// plus the underlying ref for callers that still need the raw label / daemon
// id (validation log messages, mostly). RealPath is the symlink-resolved
// absolute path; the folder-run registry keys on it so two different routes
// to the same directory count as one folder.
type localDirectoryAssignment struct {
	Ref        localDirectoryRef
	AbsPath    string // user-provided path, cleaned but not symlink-resolved
	RealPath   string // canonical key for the folder-run registry
	ResourceID string // project_resource id, for identity backfill
	ProjectID  string
	// DefaultDir marks the machine's default working directory standing in
	// for a project that binds no folder (DENE-2667 PR 8). It is the run's
	// cwd like a bound folder, but it is not the project's code.
	DefaultDir bool
}

// DisplayName is the human-facing name for this directory, safe to render in
// UI. It is deliberately NOT the absolute path: the wait reason built from it
// is stored server-side and pushed to every client on the session, and a chip
// in a chat transcript ends up in screen shares and screenshots. Absolute
// paths carry the account name, which is why handler.relativeWorkDir exists
// for the sibling work_dir chip; this is the same contract for the same reason.
//
// The resource's own label wins when the user set one — that is the name they
// chose for this directory. Otherwise the basename, which is what distinguishes
// sibling checkouts ("NuvioTV" vs "multica") without naming their parent.
func (a *localDirectoryAssignment) DisplayName() string {
	if a == nil {
		return ""
	}
	if label := strings.TrimSpace(a.Ref.Label); label != "" {
		return label
	}
	return filepath.Base(a.AbsPath)
}

// localDirectoryAssignmentForTask returns the local_directory assignment a task
// should execute inside. Squad-leader tasks are coordinators: they may create
// child issues or comments, but should not bind to the user's directory.
//
// Its result drives the agent's working directory (daemon.runTask plumbs
// AbsPath into execenv.PrepareParams.LocalWorkDir) and the GC-meta stamp that
// exempts a user-owned path from env-root cleanup.
func localDirectoryAssignmentForTask(task Task, daemonID string) (*localDirectoryAssignment, error) {
	if task.IsLeaderTask {
		return nil, nil
	}
	// A task can carry several projects (a multi-project chat, DENE-523) and a
	// run can only occupy one directory. Projects arrive in priority order, so
	// the first one pinned to this daemon wins; the other projects' resources
	// still reach the agent through the brief and resources.json, and their
	// repos remain checkoutable. The per-project uniqueness rule below is
	// unchanged — it is what stops a silent pick between two directories of
	// the SAME project.
	chosen, _, err := localDirectoryPlanForTask(task, daemonID)
	return chosen, err
}

// localDirectoryPlanForTask is localDirectoryAssignmentForTask plus the other
// directories this machine holds for the task's projects. Those are read-only
// for the run: one run writes one directory (DENE-617 invariant 1), and the
// brief names the rest so the agent can read them without discovering them by
// accident.
//
// Across several projects (a multi-project chat, DENE-523) the first project
// that pins a directory here decides the writable one; every other local
// directory of every project on this machine — including that project's own
// lower-priority rows — is read-only.
func localDirectoryPlanForTask(task Task, daemonID string) (chosen *localDirectoryAssignment, readOnly []*localDirectoryAssignment, err error) {
	// A decision is the whole answer. Scanning the resource list here would
	// put the daemon back in the business of choosing — first project wins,
	// first row wins — which is the disagreement this field exists to end.
	// No decision means an older server; that claim still has the shape the
	// scanner was written for, and the scanner stays.
	if task.CodeDecision != nil {
		return planFromCodeDecision(task, daemonID)
	}
	if task.IsLeaderTask {
		return nil, nil, nil
	}
	for _, project := range task.projectContexts() {
		picked, rest, err := resolveLocalDirectories(project.Resources, daemonID)
		if err != nil {
			return nil, nil, err
		}
		stampProjectID := func(list []*localDirectoryAssignment) {
			for _, a := range list {
				if a != nil {
					a.ProjectID = project.ID
				}
			}
		}
		stampProjectID([]*localDirectoryAssignment{picked})
		stampProjectID(rest)
		if chosen == nil && picked != nil {
			chosen = picked
			readOnly = append(readOnly, rest...)
			continue
		}
		if picked != nil {
			readOnly = append(readOnly, picked)
		}
		readOnly = append(readOnly, rest...)
	}
	return chosen, readOnly, nil
}

// findLocalDirectoryAssignment picks the local_directory this daemon runs the
// task in when the claim carries no code decision, and reports the project's
// OTHER directories on this machine as read-only context.
//
// A claim that DOES carry a decision never reaches this function. The
// decision names one resource id; planFromCodeDecision loads that row and
// checks it, and a row that does not match is a failure rather than a reason
// to keep scanning.
//
// One run writes one directory (DENE-617 invariant 1). Which one is not a
// race: resources arrive in `position` order (ListProjectResources orders by
// it), so the FIRST local_directory pinned to this daemon is the project's
// default working directory on this machine, and reordering the list in the UI
// is how a user changes it. The rest are still real directories the agent may
// need to read, so they are handed over as read-only rather than hidden.
//
// This replaces an error. Until DENE-617 a second local_directory on the same
// daemon failed the task, because the server enforced one per (project,
// daemon) and two rows could only mean corrupted data — picking one would have
// been a guess. Now several are legal and the order states the answer, so
// there is nothing left to guess.
//
// Returns nil (without error) when no local_directory is pinned to this
// daemon — the task takes the regular github_repo / remote checkout path.
// Errors only on a structurally broken ref (bad JSON, missing daemon_id, a
// path that is not absolute): a directory the daemon cannot even name is not
// something to silently skip past on the way to someone else's directory.
func findLocalDirectoryAssignment(resources []ProjectResourceData, daemonID string) (*localDirectoryAssignment, error) {
	chosen, _, err := resolveLocalDirectories(resources, daemonID)
	return chosen, err
}

// resolveLocalDirectories is findLocalDirectoryAssignment plus the read-only
// remainder, split out so the brief can name the directories the agent may
// read without the assignment path having to care.
func resolveLocalDirectories(resources []ProjectResourceData, daemonID string) (chosen *localDirectoryAssignment, readOnly []*localDirectoryAssignment, err error) {
	for _, r := range resources {
		if r.ResourceType != localDirectoryResourceType {
			continue
		}
		var ref localDirectoryRef
		if err := json.Unmarshal(r.ResourceRef, &ref); err != nil {
			return nil, nil, fmt.Errorf("local_directory: parse resource_ref: %w", err)
		}
		ref.DaemonID = strings.TrimSpace(ref.DaemonID)
		if ref.DaemonID == "" {
			return nil, nil, errors.New("local_directory: resource_ref missing daemon_id")
		}
		if ref.DaemonID != daemonID {
			// A different machine owns this resource. Skip silently; a
			// project carries one set of directories per machine, and the
			// other daemons resolve their own rows.
			continue
		}
		absPath, err := normalizeLocalPath(ref.LocalPath)
		if err != nil {
			return nil, nil, err
		}
		realPath, err := resolveRealPath(absPath)
		if err != nil {
			return nil, nil, err
		}
		entry := &localDirectoryAssignment{Ref: ref, AbsPath: absPath, RealPath: realPath, ResourceID: r.ID}
		if chosen == nil {
			chosen = entry
			continue
		}
		readOnly = append(readOnly, entry)
	}
	return chosen, readOnly, nil
}

// normalizeLocalPath strips whitespace and resolves the path to an absolute
// cleaned form. It does NOT touch the filesystem (no symlink resolution, no
// existence check) — callers do that separately via validateLocalPath.
func normalizeLocalPath(p string) (string, error) {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return "", errors.New("local_directory: local_path is empty")
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("local_directory: local_path must be absolute, got %q", trimmed)
	}
	return filepath.Clean(trimmed), nil
}

// resolveRealPath returns the symlink-resolved absolute form of path. The
// folder-run registry keys on this value so a task on `/Users/u/proj` and
// another on `/private/var/folders/.../proj-symlink → /Users/u/proj` count as
// the same folder. When EvalSymlinks fails (path is missing or not yet a real link),
// fall back to the cleaned absolute form so callers can still proceed to
// the existence-check stage which surfaces a clearer error.
func resolveRealPath(absPath string) (string, error) {
	real, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// validateLocalPath will surface the underlying error with better
		// context; for the registry key the cleaned absolute path is a safe
		// fallback (it just slightly weakens the dedup on broken symlinks).
		return absPath, nil
	}
	return real, nil
}

// validateLocalPath enforces the daemon-side preconditions for running an
// agent against a user-supplied directory:
//
//   - the path is absolute and not in the system blacklist (root, $HOME,
//     /Users, /home, the current user's $HOME — picking one of those would
//     scope the agent to the entire account, which is never what the user
//     intended);
//   - the symlink-resolved target is ALSO not in the blacklist — without
//     this a symlink like /Users/me/proj/home -> /Users/me would slip the
//     literal-equality check above while still routing every daemon write
//     into $HOME;
//   - the path exists, is a directory (not a regular file or device);
//   - the daemon process can read and write inside it (the agent will need
//     both — read for context discovery, write for the issue's edits).
//
// Each failure returns a typed error message so the daemon can forward it
// onto the task's fail comment verbatim.
func validateLocalPath(absPath string) error {
	if absPath == "" {
		return errors.New("local_directory: local_path is empty")
	}
	if !filepath.IsAbs(absPath) {
		return fmt.Errorf("local_directory: local_path must be absolute, got %q", absPath)
	}
	if reason, blocked := isBlacklistedLocalPath(absPath); blocked {
		return fmt.Errorf("local_directory: %s (%q)", reason, absPath)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("local_directory: path does not exist: %q", absPath)
		}
		return fmt.Errorf("local_directory: stat %q: %w", absPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("local_directory: path is not a directory: %q", absPath)
	}
	// Re-check the blacklist after resolving symlinks. Two ways the
	// literal check can be bypassed even when absPath itself is clean:
	//
	//   1. A user-created symlink (or a parent component) routes writes
	//      into a banned target. Example: ~/proj/home-link -> /Users/me.
	//   2. The user directly selects a canonical OS path that aliases a
	//      banned root via an OS-level symlink. Example on macOS: typing
	//      /private/tmp slips past the /tmp entry because the literal
	//      strings don't match, and EvalSymlinks is a no-op since the
	//      input is already canonical. This must be checked
	//      unconditionally — not gated on realPath != absPath — or the
	//      direct-canonical case is silently allowed.
	//
	// EvalSymlinks walks intermediate components too, so a non-symlink
	// absPath whose parent is a symlink also fails closed.
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return fmt.Errorf("local_directory: resolve symlinks for %q: %w", absPath, err)
	}
	realPath = filepath.Clean(realPath)
	if reason, blocked := isBlacklistedRealPath(realPath); blocked {
		if realPath != filepath.Clean(absPath) {
			return fmt.Errorf("local_directory: %s (symlink target of %q is %q)", reason, absPath, realPath)
		}
		return fmt.Errorf("local_directory: %s (canonical path %q)", reason, absPath)
	}
	if err := checkDirReadWrite(absPath); err != nil {
		return fmt.Errorf("local_directory: %w", err)
	}
	return nil
}

// isBlacklistedLocalPath rejects paths that map to the whole machine or an
// entire user profile. The intent is to keep the daemon from accidentally
// stamping context files (.agent_context/, .claude/skills/, .multica/) at
// the root of a user's account or the OS — a misconfiguration on the UI
// side should fail fast rather than litter the user's home.
//
// The check is by literal equality after Clean(), not prefix containment:
// a legitimate project under /Users/<user>/code/proj should pass.
func isBlacklistedLocalPath(absPath string) (reason string, blocked bool) {
	cleaned := filepath.Clean(absPath)
	if isDriveRoot(cleaned) {
		return fmt.Sprintf("path is a drive root %q", cleaned), true
	}
	for _, banned := range systemRootBlacklist() {
		if cleaned == banned {
			return fmt.Sprintf("path is a protected system root %q", banned), true
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if cleaned == filepath.Clean(home) {
			return "path is the user's home directory", true
		}
	}
	return "", false
}

// isBlacklistedRealPath is the canonical-aware variant of
// isBlacklistedLocalPath. It compares the symlink-resolved realPath against
// the symlink-resolved form of each blacklist entry so OS-level redirects
// (notably macOS's /etc -> /private/etc, /tmp -> /private/tmp, /var ->
// /private/var) cannot be used to slip a candidate past the literal
// blacklist — whether the redirect is reached via a user-created symlink
// (~/proj/home-link -> /Users/me) or by directly typing the canonical form
// (/private/tmp), which is identical to the OS view of /tmp.
func isBlacklistedRealPath(realPath string) (reason string, blocked bool) {
	realClean := filepath.Clean(realPath)
	if isDriveRoot(realClean) {
		return fmt.Sprintf("path is a drive root %q", realClean), true
	}
	for _, banned := range systemRootBlacklist() {
		bannedClean := filepath.Clean(banned)
		if realClean == bannedClean {
			return fmt.Sprintf("path is a protected system root %q", banned), true
		}
		if r, err := filepath.EvalSymlinks(banned); err == nil {
			if filepath.Clean(r) == realClean {
				return fmt.Sprintf("path is a protected system root %q", banned), true
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		homeClean := filepath.Clean(home)
		if realClean == homeClean {
			return "path is the user's home directory", true
		}
		if r, err := filepath.EvalSymlinks(home); err == nil {
			if filepath.Clean(r) == realClean {
				return "path is the user's home directory", true
			}
		}
	}
	return "", false
}

// isDriveRoot reports whether absPath is the root of a Windows volume — any
// of `C:\`, `D:\`, ..., `Z:\`, plus less common cases like `\\server\share`
// (filepath.VolumeName treats UNC roots as volumes too). On non-Windows
// this is always false because POSIX has no concept of drive letters and
// `/` is covered by systemRootBlacklist.
//
// We rely on filepath.VolumeName rather than enumerating drive letters
// statically: removable / network drives can be mounted at any letter
// (`G:\`, `H:\`, ...), and Windows installs are increasingly happy to put
// the user profile on a non-C drive. A static list (C..F) would miss them
// all.
func isDriveRoot(absPath string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	vol := filepath.VolumeName(absPath)
	if vol == "" {
		return false
	}
	// VolumeName returns the volume without trailing separator (`C:` or
	// `\\srv\share`). A drive root is volume + one separator (or, after
	// filepath.Clean, just the volume on bare-volume input).
	rest := absPath[len(vol):]
	return rest == "" || rest == `\` || rest == "/"
}

// systemRootBlacklist returns the per-OS list of paths the daemon never
// allows as a local_directory root. POSIX systems get `/`, `/Users`, `/home`
// (and macOS's `/Users/Shared` for good measure); Windows gets the
// well-known account / shared trees under C:. Drive roots themselves are
// handled by isDriveRoot so we don't have to enumerate G:\, H:\, etc.
// The list is intentionally conservative — it errs on the side of
// rejecting more, since the desktop UI is expected to surface a friendly
// picker that never produces these values.
func systemRootBlacklist() []string {
	if runtime.GOOS == "windows" {
		return []string{`C:\Users`, `C:\ProgramData`, `C:\Program Files`, `C:\Program Files (x86)`, `C:\Windows`}
	}
	return []string{"/", "/Users", "/Users/Shared", "/home", "/root", "/var", "/etc", "/tmp", "/usr", "/opt"}
}

// checkDirReadWrite verifies the daemon process can both read directory
// contents and create/remove a probe file inside dir. The probe filename is
// long, hidden, and unlikely to clash with user files; we delete it
// immediately and ignore the delete error (best-effort cleanup is fine —
// the worst case is leaving a 0-byte file the user can ignore).
func checkDirReadWrite(dir string) error {
	if _, err := os.ReadDir(dir); err != nil {
		return fmt.Errorf("read %q: %w", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".multica-rwcheck-*")
	if err != nil {
		return fmt.Errorf("write %q: %w", dir, err)
	}
	probePath := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probePath)
	return nil
}

// isGitWorkTree reports whether path is the working tree of a git repo.
// Returns false on any error (git not on PATH,
// path not in a repo, exec failure) so the caller can treat "not a git
// tree" and "can't tell" the same way: skip the git-specific path.
func isGitWorkTree(ctx context.Context, path string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// folderRunRegistry records which tasks this daemon is running in each bound
// folder, so a task's brief can name the others. It never makes a task wait:
// one folder is one checked-out branch, and the agents working in it share it
// the way people share a checkout.
type folderRunRegistry struct {
	mu   sync.Mutex
	runs map[string]map[string]folderRun
}

// folderRun is what a brief says about another task in the same folder.
type folderRun struct {
	Identifier string
	Title      string
	Branch     string
}

// Enter registers taskID as running in realPath and returns the tasks already
// running there, in a stable order, plus the func that removes the entry.
func (r *folderRunRegistry) Enter(realPath, taskID string, run folderRun) ([]folderRun, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs == nil {
		r.runs = make(map[string]map[string]folderRun)
	}
	inFolder := r.runs[realPath]
	if inFolder == nil {
		inFolder = make(map[string]folderRun)
		r.runs[realPath] = inFolder
	}
	others := make([]folderRun, 0, len(inFolder))
	for _, other := range inFolder {
		others = append(others, other)
	}
	sort.Slice(others, func(i, j int) bool {
		if others[i].Identifier != others[j].Identifier {
			return others[i].Identifier < others[j].Identifier
		}
		return others[i].Title < others[j].Title
	})
	inFolder[taskID] = run
	var once sync.Once
	return others, func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			delete(r.runs[realPath], taskID)
			if len(r.runs[realPath]) == 0 {
				delete(r.runs, realPath)
			}
		})
	}
}

// folderRunForTask describes task for the briefs of the other tasks in its
// folder. A chat turn has no issue, so it is named as a chat.
func folderRunForTask(task Task, branch string) folderRun {
	run := folderRun{
		Identifier: strings.TrimSpace(task.IssueIdentifier),
		Title:      strings.TrimSpace(task.IssueTitle),
		Branch:     branch,
	}
	if run.Identifier == "" && run.Title == "" {
		if task.ChatSessionID != "" {
			run.Title = "a chat"
		} else {
			run.Title = "an untitled task"
		}
	}
	return run
}

// codePlacementError is a refusal to place a run. Code is what the server
// records as the failure reason; the text is what the person fixing the
// binding reads. Returning one of these is the end of placement: the caller
// fails the task and does not try another directory or a remote checkout.
type codePlacementError struct {
	Code string
	Err  error
}

func (e *codePlacementError) Error() string {
	if e == nil || e.Err == nil {
		return "code placement failed"
	}
	return e.Err.Error()
}

func (e *codePlacementError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// placementCode is the failure reason a placement error carries. Anything
// that is not a placement error keeps the historical local_directory_error
// bucket, so a claim with no decision still fails the way it always has.
func placementCode(err error) string {
	var placed *codePlacementError
	if errors.As(err, &placed) && placed.Code != "" {
		return placed.Code
	}
	return "local_directory_error"
}

// planFromCodeDecision loads the one directory the decision names and checks
// it. It does not walk the resource list looking for a better candidate: a
// missing id, a path that disagrees, or a directory that is not there is a
// failure with a code.
func planFromCodeDecision(task Task, daemonID string) (*localDirectoryAssignment, []*localDirectoryAssignment, error) {
	decision := *task.CodeDecision
	switch decision.Kind() {
	case coderesolve.KindUnresolvable:
		failure, _ := decision.Failure()
		code := string(failure.Code)
		if code == "" {
			code = string(coderesolve.CodeUnset)
		}
		reason := strings.TrimSpace(failure.Reason)
		if reason == "" {
			reason = "this run has no usable code source"
		}
		return nil, nil, &codePlacementError{Code: code, Err: errors.New(reason)}
	case coderesolve.KindRemoteCache, coderesolve.KindSharedScratch:
		// No directory to use as cwd: the run gets its own workdir under the
		// workspaces root. Read-only entries, if the decision named any, stay
		// context for the brief and nothing else.
		return nil, readOnlyAssignments(decision), nil
	case coderesolve.KindLocalInPlace:
		chosen, err := lookupDecidedDirectory(task, daemonID, decision)
		if err != nil {
			return nil, nil, err
		}
		return chosen, readOnlyAssignments(decision), nil
	default:
		return nil, nil, &codePlacementError{
			Code: string(coderesolve.CodeUnknownExecutionMode),
			Err:  fmt.Errorf("code source decision kind %q is not one this daemon can execute", decision.Kind()),
		}
	}
}

// readOnlyAssignments lists the directories the decision says this run may
// read. They are not checked for writability and they are not a fallback
// cwd: a missing read-only path is omitted rather than failed, because
// failing it would change where the run writes.
func readOnlyAssignments(decision coderesolve.Decision) []*localDirectoryAssignment {
	local, _ := decision.Local()
	var out []*localDirectoryAssignment
	for _, dir := range decision.ReadOnly() {
		if dir.ResourceID != "" && dir.ResourceID == local.ResourceID {
			continue
		}
		abs, err := normalizeLocalPath(dir.Path)
		if err != nil {
			continue
		}
		real, _ := resolveRealPath(abs)
		out = append(out, &localDirectoryAssignment{
			AbsPath:    abs,
			RealPath:   real,
			ResourceID: dir.ResourceID,
			ProjectID:  dir.ProjectID,
			Ref:        localDirectoryRef{LocalPath: abs, Label: dir.Name},
		})
	}
	return out
}

func lookupDecidedDirectory(task Task, daemonID string, decision coderesolve.Decision) (*localDirectoryAssignment, error) {
	target, ok := decision.Local()
	if !ok {
		return nil, &codePlacementError{Code: "decision_mismatch", Err: errors.New("local code decision carried no directory")}
	}
	id := strings.TrimSpace(target.ResourceID)
	if id == "" {
		return nil, &codePlacementError{
			Code: string(coderesolve.CodePinnedResourceNotFound),
			Err:  errors.New("code decision names no local directory"),
		}
	}
	res, projectID, found := findResourceByID(task, id)
	if !found {
		return nil, &codePlacementError{
			Code: string(coderesolve.CodePinnedResourceNotFound),
			Err:  fmt.Errorf("code decision names local directory %s, which is not in this task", id),
		}
	}
	if res.ResourceType != localDirectoryResourceType {
		return nil, &codePlacementError{
			Code: string(coderesolve.CodePinnedResourceUnusable),
			Err:  fmt.Errorf("code decision resource %s is %s, not a local directory", id, res.ResourceType),
		}
	}
	var ref localDirectoryRef
	if err := json.Unmarshal(res.ResourceRef, &ref); err != nil {
		return nil, &codePlacementError{
			Code: string(coderesolve.CodeMalformedResource),
			Err:  fmt.Errorf("local_directory: parse resource_ref: %w", err),
		}
	}
	ref.DaemonID = strings.TrimSpace(ref.DaemonID)
	if ref.DaemonID == "" {
		return nil, &codePlacementError{
			Code: string(coderesolve.CodeMalformedResource),
			Err:  errors.New("local_directory: resource_ref missing daemon_id"),
		}
	}
	if ref.DaemonID != daemonID {
		return nil, &codePlacementError{
			Code: string(coderesolve.CodePinnedResourceOtherMachine),
			Err:  fmt.Errorf("local directory %s is bound to a different runtime", id),
		}
	}
	absPath, err := normalizeLocalPath(ref.LocalPath)
	if err != nil {
		return nil, &codePlacementError{Code: string(coderesolve.CodeMalformedResource), Err: err}
	}
	wantPath := target.Path
	if strings.TrimSpace(wantPath) == "" || !localPathsAgree(absPath, wantPath) {
		return nil, &codePlacementError{
			Code: "decision_mismatch",
			Err:  fmt.Errorf("local directory %s is %q, but the decision says %q", id, absPath, wantPath),
		}
	}
	if strings.TrimSpace(ref.Label) == "" {
		ref.Label = strings.TrimSpace(target.DisplayName)
	}
	realPath, _ := resolveRealPath(absPath)
	assignment := &localDirectoryAssignment{
		Ref:        ref,
		AbsPath:    absPath,
		RealPath:   realPath,
		ResourceID: res.ID,
		ProjectID:  projectID,
	}
	if err := validateLocalPath(absPath); err != nil {
		return nil, &codePlacementError{Code: classifyPathFailure(err), Err: err}
	}
	return assignment, nil
}

func findResourceByID(task Task, id string) (ProjectResourceData, string, bool) {
	for _, project := range task.projectContexts() {
		for _, res := range project.Resources {
			if res.ID == id {
				return res, project.ID, true
			}
		}
	}
	return ProjectResourceData{}, "", false
}

// localPathsAgree reports whether the directory on the resource and the
// directory in the decision are the same place. A symlink and its target
// agree; two different directories do not, even when one of them also
// happens to be bound to this task.
func localPathsAgree(resourcePath, decisionPath string) bool {
	resourcePath = filepath.Clean(resourcePath)
	decisionPath = filepath.Clean(strings.TrimSpace(decisionPath))
	if resourcePath == decisionPath {
		return true
	}
	resourceReal, resourceErr := filepath.EvalSymlinks(resourcePath)
	decisionReal, decisionErr := filepath.EvalSymlinks(decisionPath)
	if resourceErr != nil || decisionErr != nil {
		return false
	}
	return filepath.Clean(resourceReal) == filepath.Clean(decisionReal)
}

func classifyPathFailure(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "does not exist"):
		return "path_not_found"
	case strings.Contains(msg, "not a directory"):
		return "path_not_directory"
	case strings.Contains(msg, "protected system root"), strings.Contains(msg, "home directory"), strings.Contains(msg, "drive root"):
		return "path_forbidden"
	case strings.Contains(msg, "write "), strings.Contains(msg, "read "):
		return "path_not_writable"
	default:
		return "path_invalid"
	}
}

func (d *Daemon) resolveLocalDirectoryAssignment(task Task) (*localDirectoryAssignment, error) {
	assignment, _, err := d.resolveLocalDirectoryPlan(task)
	return assignment, err
}

// resolveLocalDirectoryPlan is resolveLocalDirectoryAssignment plus the
// project's other local directories on this machine, which the run may read
// but not write (DENE-617 invariant 1).
//
// Resolving is a pure read: the identity backfill is NOT fired from here.
// runTask fires it once the task is actually committed to the directory.
func (d *Daemon) resolveLocalDirectoryPlan(task Task) (*localDirectoryAssignment, []*localDirectoryAssignment, error) {
	return localDirectoryPlanForTask(task, d.cfg.DaemonID)
}
