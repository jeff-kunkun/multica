package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/ghpr"
)

// Swappable for tests; production shells out to gh in the working directory.
var (
	ghListPRs = ghpr.List
	ghMergePR = ghpr.Merge
)

var closeIdentRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-\d+$`)

// refreshIssuePullRequests reports the issue's PR state from the caller's gh
// before the close request, so the done gate reads the real merge state on a
// workspace without a GitHub App (DENE-875). With merge set (a `--verdict pass`
// done), open non-draft PRs naming the issue are squash-merged first — the
// same step the gate would take through the App. Failures only warn: the
// server gate stays fail-closed and blocks the close with a reason.
func refreshIssuePullRequests(ctx context.Context, client *cli.APIClient, issueID, ident string, merge bool) {
	if !closeIdentRe.MatchString(ident) {
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	list := func() ([]ghpr.PR, error) {
		prs, err := ghListPRs(ctx, dir, "--search", ident+" in:title")
		if err != nil {
			return nil, err
		}
		if branch := currentGitBranch(dir); branch != "" && branch != "HEAD" {
			if more, err := ghListPRs(ctx, dir, "--head", branch); err == nil {
				prs = append(prs, more...)
			}
		}
		return namingIssue(prs, ident), nil
	}
	prs, err := list()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ! could not read PR state with gh (%v); the close gate uses what the server already knows\n", err)
		return
	}
	if len(prs) == 0 {
		return
	}
	if merge {
		mergedAny := false
		for _, pr := range prs {
			if pr.State != "open" || pr.IsDraft {
				continue
			}
			if err := ghMergePR(ctx, dir, pr.URL); err != nil {
				fmt.Fprintf(os.Stderr, "  ! %v\n", err)
				continue
			}
			fmt.Fprintf(os.Stderr, "  - merged %s\n", pr.URL)
			mergedAny = true
		}
		if mergedAny {
			if again, err := list(); err == nil {
				prs = again
			}
		}
	}
	if err := client.PostJSON(ctx, "/api/issues/"+issueID+"/pull-requests/report", map[string]any{"pull_requests": prs}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "  ! could not report PR state: %v\n", err)
	}
}

// namingIssue keeps PRs whose title or branch carries the identifier and
// drops duplicates from the two gh queries.
func namingIssue(prs []ghpr.PR, ident string) []ghpr.PR {
	re := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])` + regexp.QuoteMeta(ident) + `($|[^0-9])`)
	seen := map[string]bool{}
	out := prs[:0:0]
	for _, pr := range prs {
		if seen[pr.URL] || !(re.MatchString(pr.Title) || re.MatchString(pr.Branch)) {
			continue
		}
		seen[pr.URL] = true
		out = append(out, pr)
	}
	return out
}

func currentGitBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
