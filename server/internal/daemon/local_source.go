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
	// ReadOnlyDirs are the project's other local directories on this machine.
	// The run may read them; only LocalPath is writable (DENE-617).
	ReadOnlyDirs []readOnlyLocalDir
}

// readOnlyLocalDir is one local directory the run may read but not write.
type readOnlyLocalDir struct {
	Path string
	Name string
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
func resolveTaskCodeSource(assignment *localDirectoryAssignment, readOnly []*localDirectoryAssignment, repoURLs []string, remotesOf gitRemotesFunc) taskCodeSource {
	if assignment == nil {
		return taskCodeSource{Kind: codeSourceKindRemoteCheckout, RemoteRepos: append([]string(nil), repoURLs...)}
	}
	mode := strings.TrimSpace(assignment.Ref.ExecutionMode)
	if mode == "" {
		mode = localDirectoryModeInPlace
	}
	src := taskCodeSource{
		Kind:          codeSourceKindLocalDirectory,
		LocalPath:     assignment.AbsPath,
		ExecutionMode: mode,
		DisplayName:   assignment.DisplayName(),
	}
	for _, dir := range readOnly {
		if dir == nil {
			continue
		}
		src.ReadOnlyDirs = append(src.ReadOnlyDirs, readOnlyLocalDir{Path: dir.AbsPath, Name: dir.DisplayName()})
	}
	for _, url := range repoURLs {
		res := resolveLocalRepo(assignment, url, remotesOf)
		switch {
		case res.Covered:
			src.CoveredRepos = append(src.CoveredRepos, url)
		case res.Suspected:
			src.UnprovenRepos = append(src.UnprovenRepos, codeSourceWarning{URL: url, Reason: res.Reason})
		default:
			src.RemoteRepos = append(src.RemoteRepos, url)
		}
	}
	return src
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
func decideLocalCheckout(assignment *localDirectoryAssignment, repoURL string, remotesOf gitRemotesFunc) localCheckoutOutcome {
	res := resolveLocalRepo(assignment, repoURL, remotesOf)
	switch {
	case res.Covered:
		return localCheckoutOutcome{Path: res.Path}
	case res.Suspected:
		return localCheckoutOutcome{Refusal: fmt.Sprintf(
			"This project is configured to use the local directory %q on this machine, so %s is not cloned. "+
				"%s. Fix the directory (check out the repository there, or add the matching git remote), "+
				"or remove the local directory resource from the project if this repository really is a separate checkout. "+
				"Refusing to clone a second copy instead, which is what pinning a local directory asks to prevent.",
			assignment.AbsPath, repoURL, res.Reason)}
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
	for _, d := range src.ReadOnlyDirs {
		out.ReadOnlyDirs = append(out.ReadOnlyDirs, execenv.CodeSourceDirForEnv{Path: d.Path, Name: d.Name})
	}
	return out
}
