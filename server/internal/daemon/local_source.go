package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/repoident"
)

// This file answers "where does this task's code come from?" — one rule, in one
// place, for the brief and for `multica repo checkout` alike.
//
// The rule (DENE-592/DENE-595): when the project pins a local directory on THIS
// machine, the task uses that directory. A github_repo resource naming the same
// repository is then a second pointer to code the machine already has, not a
// second repository: cloning it produced a duplicate checkout on disk and sent
// the agent to work in the copy the user was not looking at.
//
// Two deliberate asymmetries:
//
//   - Redirecting a checkout requires PROOF the directory holds that repository
//     (a git remote whose identity matches). A name that merely looks alike is
//     never enough to send an agent somewhere it did not ask to go.
//   - Refusing to clone never degrades into cloning anyway. If the directory
//     should have been used and cannot be, the task gets the reason. Falling
//     back to the network is what this change exists to stop.

// gitRemoteLookupTimeout bounds the `git remote` probe. The caller is either
// rendering a brief or serving a localhost checkout request; a wedged git must
// not hold either open.
const gitRemoteLookupTimeout = 5 * time.Second

// localRepoResolution is the verdict for one repo URL against the task's local
// directory assignment.
type localRepoResolution struct {
	// Path is the absolute directory holding the repository, when Covered.
	Path string
	// Covered is true only on a proven match (a git remote identity equal to
	// the requested URL). This is what may redirect a checkout.
	Covered bool
	// Suspected is true when the directory carries the repository's NAME but
	// the match could not be proven — no remotes readable, or the candidate is
	// not a git working tree. It is reported, never acted on.
	Suspected bool
	// Reason explains a Suspected-but-not-Covered verdict in words a user can
	// act on. Empty when Covered.
	Reason string
}

// gitRemotesFunc reads the remote URLs configured in a git working tree.
// Injected so the resolution logic is testable without a git binary.
type gitRemotesFunc func(dir string) []string

// readGitRemotes returns every remote URL configured in dir, or nil when dir is
// not a git working tree. Errors are not distinguished from "no remotes": a
// caller that cannot read remotes must fall back to the weak signal either way.
func readGitRemotes(dir string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), gitRemoteLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "remote", "-v")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var urls []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		url := fields[1]
		if _, ok := seen[url]; ok {
			continue
		}
		seen[url] = struct{}{}
		urls = append(urls, url)
	}
	return urls
}

// localRepoCandidates lists the directories that could hold repoURL for this
// assignment, nearest first.
//
// The directory itself is always a candidate. One level down is too, because
// shared mode exists precisely for an umbrella directory holding several
// repositories side by side — there the repo lives at <dir>/<name>, and a rule
// that only looked at <dir> would clone every one of them.
func localRepoCandidates(absPath, repoURL string) []string {
	candidates := []string{absPath}
	name := string(repoident.NameFromURL(repoURL))
	if name == "" {
		return candidates
	}
	// Match the child case-insensitively: the remote's name and the directory
	// on disk routinely differ in case ("NuvioTV" vs "nuviotv").
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return candidates
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.EqualFold(e.Name(), name) {
			candidates = append(candidates, filepath.Join(absPath, e.Name()))
		}
	}
	return candidates
}

// resolveLocalRepo decides whether the task's local directory already holds
// repoURL. A nil assignment means the project pinned no directory on this
// machine, which is the one case where a remote checkout is the correct answer.
func resolveLocalRepo(assignment *localDirectoryAssignment, repoURL string, remotesOf gitRemotesFunc) localRepoResolution {
	if assignment == nil || strings.TrimSpace(repoURL) == "" {
		return localRepoResolution{}
	}
	if remotesOf == nil {
		remotesOf = readGitRemotes
	}
	var suspected string
	for _, dir := range localRepoCandidates(assignment.AbsPath, repoURL) {
		remotes := remotesOf(dir)
		if repoident.MatchesRemotes(repoURL, remotes) {
			return localRepoResolution{Path: dir, Covered: true}
		}
		if len(remotes) > 0 {
			// It is a git tree, and it is a different repository. Nothing
			// suspicious about that — an umbrella directory holds many.
			continue
		}
		if repoident.LooksLikeSameRepo(repoURL, dir) && suspected == "" {
			suspected = dir
		}
	}
	if suspected != "" {
		return localRepoResolution{
			Path:      suspected,
			Suspected: true,
			Reason: fmt.Sprintf(
				"%q is named after this repository but no git remote there matches %s, so it cannot be proven to be the same checkout",
				suspected, repoURL),
		}
	}
	return localRepoResolution{}
}

// taskCodeSource is the resolved, user-facing answer to "where is this task's
// code, and why there?". It is rendered into the brief and reported to the UI
// so the choice stops being invisible (DENE-595).
type taskCodeSource struct {
	// Kind is "local_directory" when the project pinned a directory on this
	// machine, "remote_checkout" otherwise.
	Kind string
	// LocalPath, ExecutionMode, DisplayName describe the pinned directory.
	LocalPath     string
	ExecutionMode string
	DisplayName   string
	// CoveredRepos are project repo URLs proven to already live in the
	// directory. The agent must not check these out.
	CoveredRepos []string
	// UnprovenRepos are repo URLs the directory is NAMED after but could not be
	// proven to hold, each with the reason. Surfaced rather than guessed at.
	UnprovenRepos []codeSourceWarning
	// RemoteRepos are the repo URLs genuinely absent from the directory; these
	// still use `multica repo checkout`.
	RemoteRepos []string
}

type codeSourceWarning struct {
	URL    string
	Reason string
}

const (
	codeSourceKindLocalDirectory = "local_directory"
	codeSourceKindRemoteCheckout = "remote_checkout"
)

// resolveTaskCodeSource classifies every repo attached to the task against the
// local directory the project pinned on this machine.
func resolveTaskCodeSource(assignment *localDirectoryAssignment, workDir string, repoURLs []string, remotesOf gitRemotesFunc) taskCodeSource {
	if assignment == nil {
		return taskCodeSource{Kind: codeSourceKindRemoteCheckout, RemoteRepos: append([]string(nil), repoURLs...)}
	}
	mode := strings.TrimSpace(assignment.Ref.ExecutionMode)
	if mode == "" {
		mode = localDirectoryModeInPlace
	}
	src := taskCodeSource{
		Kind: codeSourceKindLocalDirectory,
		// The directory the AGENT is in, which is the user's own path in
		// in_place and shared and the task's private worktree in worktree
		// mode. Naming assignment.AbsPath here would contradict the execution
		// mode line rendered directly below it in the brief.
		LocalPath:     taskLocalRoot(assignment, workDir),
		ExecutionMode: mode,
		DisplayName:   assignment.DisplayName(),
	}
	for _, url := range repoURLs {
		res := resolveLocalRepo(assignment, url, remotesOf)
		switch {
		case res.Covered:
			if _, ok := taskDirFor(assignment, workDir, res.Path, url, remotesOf); !ok {
				src.UnprovenRepos = append(src.UnprovenRepos, codeSourceWarning{
					URL:    url,
					Reason: worktreeUnreachableReason(assignment, res.Path, url),
				})
				continue
			}
			src.CoveredRepos = append(src.CoveredRepos, url)
		case res.Suspected:
			src.UnprovenRepos = append(src.UnprovenRepos, codeSourceWarning{URL: url, Reason: res.Reason})
		default:
			src.RemoteRepos = append(src.RemoteRepos, url)
		}
	}
	return src
}

// taskLocalRoot is the directory this task actually runs in. It differs from
// the pinned path only in worktree mode, where execenv gives the task its own
// checkout under the env root (execenv.PrepareLocalWorktree).
func taskLocalRoot(assignment *localDirectoryAssignment, workDir string) string {
	if assignment.UsesWorktree() && strings.TrimSpace(workDir) != "" {
		return workDir
	}
	return assignment.AbsPath
}

// taskDirFor maps a directory proven to hold repoURL inside the user's pinned
// directory onto the directory THIS task may write in.
//
// in_place and shared run in the user's own directory, so the two are the same
// and the mapping is the identity. worktree does not: the agent's cwd is a
// private worktree of the repository, and handing back the user's path there
// would have the agent commit into the working copy the mode exists to keep it
// out of — the DENE-595 accident pointed the other way.
//
// ok=false means the proven directory has no counterpart the task may write
// in. That happens when the match was a nested repository under the pinned
// path (the umbrella layout shared mode exists for), which worktree mode does
// not replay: the caller must refuse rather than offer the user's copy.
func taskDirFor(assignment *localDirectoryAssignment, workDir, resolved, repoURL string, remotesOf gitRemotesFunc) (string, bool) {
	if !assignment.UsesWorktree() {
		return resolved, true
	}
	if strings.TrimSpace(workDir) == "" {
		return "", false
	}
	rel, err := filepath.Rel(assignment.AbsPath, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	candidate := filepath.Join(workDir, rel)
	if remotesOf == nil {
		remotesOf = readGitRemotes
	}
	// Prove the counterpart the same way the original match was proven. A
	// submodule replayed into the worktree passes; a sibling repository that
	// only ever lived beside the pinned path does not.
	if !repoident.MatchesRemotes(repoURL, remotesOf(candidate)) {
		return "", false
	}
	return candidate, true
}

// worktreeUnreachableReason explains a repository that is on this machine but
// not inside the task's worktree.
func worktreeUnreachableReason(assignment *localDirectoryAssignment, resolved, repoURL string) string {
	return fmt.Sprintf(
		"%q holds %s, but this task runs in its own git worktree of %q and that directory has no counterpart there, "+
			"so it cannot be reached without writing into the user's working copy",
		resolved, repoURL, assignment.AbsPath)
}

// localCheckoutOutcome is what serveLocalDirectoryCheckout decided, split out
// from the HTTP plumbing so the rule can be tested without a server.
type localCheckoutOutcome struct {
	// Path is the directory to hand back instead of a clone. Set only when the
	// local directory was proven to hold the repository.
	Path string
	// Refusal is a user-facing explanation of why this checkout cannot proceed.
	// Set when the project pinned a directory that should hold this repository
	// but the claim could not be verified. NEVER falls back to cloning: that
	// silent fallback is the bug (DENE-595).
	Refusal string
	// ExecutionMode is the pinned resource's mode, carried onto the response so
	// the CLI can describe the path: "the user's own checkout" is true in
	// in_place and shared and false in worktree.
	ExecutionMode string
}

// decideLocalCheckout applies the source rule to one `multica repo checkout`
// request.
//
// Three outcomes, and the third is the important one:
//
//	proven match      → return the local path; no network, no second copy.
//	no local claim    → zero value; the caller clones as before. A project with
//	                    a local directory can still legitimately reference OTHER
//	                    repositories, and those must keep working.
//	unproven claim    → a refusal naming what to fix. The directory is named
//	                    after this repository but nothing there proves it, so
//	                    neither answer is safe to give silently.
func decideLocalCheckout(assignment *localDirectoryAssignment, workDir, repoURL string, remotesOf gitRemotesFunc) localCheckoutOutcome {
	res := resolveLocalRepo(assignment, repoURL, remotesOf)
	mode := ""
	if assignment != nil {
		mode = strings.TrimSpace(assignment.Ref.ExecutionMode)
	}
	switch {
	case res.Covered:
		// Worktree mode must hand back the task's own checkout, never the
		// user's. When no counterpart exists the answer is a refusal, for the
		// same reason an unproven claim is: the alternatives left are a second
		// clone and someone else's working copy, and neither may be picked
		// silently.
		dir, ok := taskDirFor(assignment, workDir, res.Path, repoURL, remotesOf)
		if !ok {
			return localCheckoutOutcome{ExecutionMode: mode, Refusal: fmt.Sprintf(
				"This project is pinned to the local directory %q and runs this task in its own git worktree, "+
					"so %s must not be checked out into the user's copy. %s. "+
					"Switch the resource's execution mode to in_place or shared if the task needs that directory directly, "+
					"or configure this repository as its own local directory resource.",
				assignment.AbsPath, repoURL, worktreeUnreachableReason(assignment, res.Path, repoURL))}
		}
		return localCheckoutOutcome{Path: dir, ExecutionMode: mode}
	case res.Suspected:
		return localCheckoutOutcome{Refusal: fmt.Sprintf(
			"This project is configured to use the local directory %q on this machine, so %s is not cloned. "+
				"%s. Fix the directory (check out the repository there, or add the matching git remote), "+
				"or remove the local directory resource from the project if this repository really is a separate checkout. "+
				"Refusing to clone a second copy instead, which is what pinning a local directory asks to prevent.",
			assignment.AbsPath, repoURL, res.Reason), ExecutionMode: mode}
	default:
		return localCheckoutOutcome{}
	}
}

// currentGitBranch reports the branch dir is on, or "" when it is not a git
// working tree or is on a detached HEAD. Best effort: the branch is display
// information on the checkout response, and the agent can always ask git
// itself.
func currentGitBranch(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitRemoteLookupTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		return ""
	}
	return branch
}

// repoURLsOf flattens the task's repo list to URLs.
func repoURLsOf(repos []RepoData) []string {
	urls := make([]string, 0, len(repos))
	for _, r := range repos {
		if u := strings.TrimSpace(r.URL); u != "" {
			urls = append(urls, u)
		}
	}
	return urls
}

// codeSourceForEnv converts the daemon's verdict into the brief's shape.
func codeSourceForEnv(src taskCodeSource) execenv.CodeSourceForEnv {
	out := execenv.CodeSourceForEnv{
		Kind:          src.Kind,
		LocalPath:     src.LocalPath,
		ExecutionMode: src.ExecutionMode,
		DisplayName:   src.DisplayName,
	}
	for _, url := range src.CoveredRepos {
		// Re-resolving would hit the filesystem a second time; the covered
		// path is the directory itself unless a child matched, so report the
		// directory and let the agent's own `git -C` be the finer answer.
		out.CoveredRepos = append(out.CoveredRepos, execenv.CodeSourceRepoForEnv{URL: url})
	}
	for _, w := range src.UnprovenRepos {
		out.UnprovenRepos = append(out.UnprovenRepos, execenv.CodeSourceRepoForEnv{URL: w.URL, Detail: w.Reason})
	}
	for _, url := range src.RemoteRepos {
		out.RemoteRepos = append(out.RemoteRepos, execenv.CodeSourceRepoForEnv{URL: url})
	}
	return out
}
