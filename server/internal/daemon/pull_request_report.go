package daemon

import (
	"context"
	"strings"

	"github.com/multica-ai/multica/server/internal/ghpr"
)

// reportLocalPullRequests mirrors PRs for a completed run without relying on a
// GitHub App. gh is deliberately invoked in the agent checkout so its
// credentials are the same ones used to push the delivery branch.
func (d *Daemon) reportLocalPullRequests(ctx context.Context, task Task, result TaskResult) error {
	branch := strings.TrimSpace(result.BranchName)
	dir := strings.TrimSpace(result.WorkDir)
	if branch == "" || dir == "" || task.WorkspaceID == "" {
		return nil
	}
	prs, err := ghpr.List(ctx, dir, "--head", branch)
	if err != nil || len(prs) == 0 {
		return err
	}
	return d.client.ReportDaemonPullRequests(ctx, task.WorkspaceID, prs)
}
