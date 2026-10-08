package execenv

import (
	"fmt"
	"strconv"
	"strings"
)

// Knowledge sediment rides the delivery (DENE-1661).
//
// A close that claims it wrote AGENTS.md / CONTEXT.md / docs must show those
// files in what it delivers, and a chat that settled something merges it into
// the main line before it reports. The server cannot see the repository, so
// the CLI reads the facts from git here and the server holds the claim against
// them — the same split as the delivery-line merge.

// DeliveredFiles lists the paths HEAD adds or changes relative to the nearest
// main line: the files this delivery ships. bases are tried first, in order
// (a sub-issue's delivery branch); the main lines of the checkout come after.
// The first base HEAD is ahead of wins. nil, "" means no base was found or
// HEAD is already on every one of them, so nothing can be said.
func DeliveredFiles(dir string, bases ...string) ([]string, string, error) {
	gitRoot, err := runGitTrimmed(dir, "rev-parse", "--show-toplevel")
	if err != nil || gitRoot == "" {
		return nil, "", fmt.Errorf("%s is not inside a git checkout", dir)
	}
	head, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, "", fmt.Errorf("resolve HEAD: %w", err)
	}
	pick := func(candidates []string) (string, string) {
		best, bestBase, bestCount := "", "", -1
		for _, ref := range candidates {
			base, err := runGitTrimmed(gitRoot, "merge-base", ref, head)
			if err != nil || base == "" {
				continue
			}
			raw, err := runGitTrimmed(gitRoot, "rev-list", "--count", base+".."+head)
			count, convErr := strconv.Atoi(raw)
			if err != nil || convErr != nil || count == 0 {
				continue
			}
			if bestCount < 0 || count < bestCount {
				best, bestBase, bestCount = ref, base, count
			}
		}
		return best, bestBase
	}
	var ref, base string
	for _, b := range bases {
		if b = strings.TrimSpace(b); b != "" {
			if ref, base = pick([]string{b}); ref != "" {
				break
			}
		}
	}
	if ref == "" {
		ref, base = pick(MainlineCandidates(gitRoot))
	}
	if ref == "" {
		return nil, "", nil
	}
	files, err := changedFiles(gitRoot, base, head)
	return files, ref, err
}

// changedFiles lists the paths to adds or changes against from. Deletions
// are left out: removing a memory file does not write it.
func changedFiles(dir, from, to string) ([]string, error) {
	out, err := runGitStdout(dir, "diff", "--name-only", "--no-renames", "--diff-filter=ACMRT", "-z", from, to)
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	files := []string{}
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			files = append(files, path)
		}
	}
	return files, nil
}

// MainlineCandidates are the refs a task branch may have forked from: the
// branch the user's own checkout is on and its upstream, the remote's default
// branch, and a local main/master. Task branches (agent/…) never count.
func MainlineCandidates(gitRoot string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == "HEAD" || seen[ref] || strings.HasPrefix(ref, "agent/") || strings.Contains(ref, "/agent/") {
			return
		}
		if _, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
			return
		}
		seen[ref] = true
		out = append(out, ref)
	}
	if user := worktreeBranch(mainWorktree(gitRoot)); user != "" {
		add(user)
		upstream, _ := runGitTrimmed(gitRoot, "rev-parse", "--abbrev-ref", user+"@{upstream}")
		add(upstream)
	}
	if def, err := runGitTrimmed(gitRoot, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		add(def)
	}
	add("main")
	add("master")
	return out
}

// Mainline is the branch the user's checkout of the repository is on — the
// line a chat's work has to land on for the project directory to have it.
// A detached user checkout falls back to the remote default, then main/master.
func Mainline(dir string) string {
	gitRoot, err := runGitTrimmed(dir, "rev-parse", "--show-toplevel")
	if err != nil || gitRoot == "" {
		return ""
	}
	if user := worktreeBranch(mainWorktree(gitRoot)); user != "" {
		return user
	}
	if def, err := runGitTrimmed(gitRoot, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if _, name, ok := strings.Cut(def, "/"); ok {
			return name
		}
	}
	for _, name := range []string{"main", "master"} {
		if _, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err == nil {
			return name
		}
	}
	return ""
}

// RemoteURL is origin's URL, "" for a repository that only lives here.
func RemoteURL(dir string) string {
	out, err := runGitTrimmed(dir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return out
}

// MainlineMergeResult is what MergeIntoMainline did.
type MainlineMergeResult struct {
	Mainline string `json:"mainline"`
	// Source is the branch that was merged; equal to Mainline when the
	// work was committed on the main line directly (a shared directory).
	Source string `json:"source"`
	// Tip is the main line after the merge.
	Tip string `json:"tip"`
	// Commits are the work's commits, oldest first; the merge commit, when
	// one was made, is last.
	Commits []DeliveryMergeCommit `json:"commits"`
	// Files are the paths the work adds or changes.
	Files []string `json:"files"`
}

// MergeIntoMainline lands the current branch's commits on the main line of
// a repository that has no remote to open a PR on (DENE-1661). The merge is
// always a merge commit whose message is message, so the main line says
// where the work came from. A checkout holding the main line is merged in
// place and must be clean; otherwise the branch moves with a compare-and-swap.
// since bounds the commits reported for work already on the main line.
func MergeIntoMainline(dir, message, since string) (MainlineMergeResult, error) {
	var res MainlineMergeResult
	gitRoot, err := runGitTrimmed(dir, "rev-parse", "--show-toplevel")
	if err != nil || gitRoot == "" {
		return res, fmt.Errorf("%w: %s is not inside a git checkout", ErrDeliveryMergeRefused, dir)
	}
	res.Mainline = Mainline(gitRoot)
	if res.Mainline == "" {
		return res, fmt.Errorf("%w: no main line found (the project directory is detached and there is no main/master)", ErrDeliveryMergeRefused)
	}
	res.Source = worktreeBranch(gitRoot)
	if res.Source == "" {
		return res, fmt.Errorf("%w: the checkout is not on a branch; switch back to the chat's branch first", ErrDeliveryMergeRefused)
	}
	if dirty, err := uncommittedWork(gitRoot); err != nil {
		return res, err
	} else if len(dirty) > 0 {
		return res, fmt.Errorf("%w: uncommitted changes (%s); commit them on %s first — only commits are merged", ErrDeliveryMergeRefused, strings.Join(clip(dirty, 5), ", "), res.Source)
	}
	head, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return res, fmt.Errorf("resolve HEAD: %w", err)
	}
	ref := "refs/heads/" + res.Mainline
	tip, _ := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", ref)
	if tip == "" {
		return res, fmt.Errorf("%w: the main line %s does not exist", ErrDeliveryMergeRefused, res.Mainline)
	}

	if isAncestor(gitRoot, head, tip) {
		// Already on the main line: a shared directory works on it directly.
		res.Tip = tip
		args := []string{"--since=" + since}
		if since == "" {
			args = []string{"-n", "20"}
		}
		if upstream, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", res.Mainline+"@{upstream}"); err == nil && upstream != "" {
			args = append(args, "^"+upstream)
		}
		if res.Commits, err = listCommits(gitRoot, head, args...); err != nil {
			return res, err
		}
		res.Files, err = commitFiles(gitRoot, res.Commits)
		return res, err
	}

	base, err := runGitTrimmed(gitRoot, "merge-base", tip, head)
	if err != nil {
		return res, fmt.Errorf("git merge-base: %w", err)
	}
	if res.Files, err = changedFiles(gitRoot, base, head); err != nil {
		return res, err
	}
	if res.Commits, err = listCommits(gitRoot, head, "^"+tip); err != nil {
		return res, err
	}

	if holder := worktreeHolding(gitRoot, res.Mainline); holder != "" {
		if dirty, err := uncommittedWork(holder); err != nil {
			return res, err
		} else if len(dirty) > 0 {
			return res, fmt.Errorf("%w: the project directory %s has uncommitted changes (%s); the merge would mix into them. Commit or stash them there and run again", ErrDeliveryMergeRefused, holder, strings.Join(clip(dirty, 5), ", "))
		}
		if out, err := runGit(holder, "merge", "--no-ff", "--no-edit", "-m", message, head); err != nil {
			_, _ = runGit(holder, "merge", "--abort")
			return res, fmt.Errorf("%w: merging into %s in %s failed, nothing changed: %s", ErrDeliveryMergeRefused, res.Mainline, holder, strings.TrimSpace(out))
		}
		res.Tip, _ = runGitTrimmed(holder, "rev-parse", "HEAD")
	} else {
		tree, conflicts, err := mergeTree(gitRoot, tip, head)
		if err != nil {
			return res, err
		}
		if len(conflicts) > 0 {
			return res, fmt.Errorf("%w: merging into %s conflicts on %s; merge %s into this branch, resolve, commit, and run again", ErrDeliveryMergeRefused, res.Mainline, strings.Join(clip(conflicts, 5), ", "), res.Mainline)
		}
		args := append(commitIdentityArgs(gitRoot), "commit-tree", tree, "-p", tip, "-p", head, "-m", message)
		next, err := runGitTrimmed(gitRoot, args...)
		if err != nil {
			return res, fmt.Errorf("git commit-tree: %w", err)
		}
		if out, err := runGit(gitRoot, "update-ref", ref, next, tip); err != nil {
			return res, fmt.Errorf("move %s: %s: %w (it moved meanwhile; run again)", res.Mainline, strings.TrimSpace(out), err)
		}
		res.Tip = next
	}
	res.Commits = append(res.Commits, DeliveryMergeCommit{SHA: res.Tip, Subject: message})
	return res, nil
}

// commitFiles is the union of the paths the commits add or change.
func commitFiles(dir string, commits []DeliveryMergeCommit) ([]string, error) {
	seen := map[string]bool{}
	files := []string{}
	for _, c := range commits {
		out, err := runGitStdout(dir, "show", "--name-only", "--no-renames", "--diff-filter=ACMRT", "--format=", "-z", c.SHA)
		if err != nil {
			return nil, fmt.Errorf("git show: %w", err)
		}
		for _, path := range strings.Split(out, "\x00") {
			if path = strings.TrimSpace(path); path != "" && !seen[path] {
				seen[path] = true
				files = append(files, path)
			}
		}
	}
	return files, nil
}
