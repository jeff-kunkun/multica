package daemon

import (
 "context"
 "encoding/json"
 "fmt"
 "os/exec"
 "strings"
 "time"
)

type localPR struct { Number int32 `json:"number"`; Title, State, URL, HeadRefName, HeadRefOid string; IsDraft bool; MergedAt *time.Time }

// reportLocalPullRequests mirrors PRs for a completed run without relying on a
// GitHub App. gh is deliberately invoked in the agent checkout so its remote
// and credentials are the same ones used to push the delivery branch.
func (d *Daemon) reportLocalPullRequests(ctx context.Context, task Task, result TaskResult) error {
 branch := strings.TrimSpace(result.BranchName); dir := strings.TrimSpace(result.WorkDir)
 if branch == "" || dir == "" || task.WorkspaceID == "" { return nil }
 cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--head", branch, "--state", "all", "--json", "number,title,state,url,headRefName,headRefOid,isDraft,mergedAt")
 cmd.Dir = dir; out, err := cmd.Output(); if err != nil { return fmt.Errorf("gh pr list: %w", err) }
 var prs []localPR; if err := json.Unmarshal(out, &prs); err != nil { return err }
 type remotePR struct { Owner, Repo string }
 remote := exec.CommandContext(ctx, "git", "-C", dir, "config", "--get", "remote.origin.url"); b, err := remote.Output(); if err != nil { return err }
 s := strings.TrimSpace(string(b)); s = strings.TrimSuffix(s, ".git"); s = strings.TrimPrefix(s, "git@github.com:"); s = strings.TrimPrefix(s, "https://github.com/"); parts := strings.Split(s, "/"); if len(parts)<2 { return fmt.Errorf("invalid github remote") }
 reports := make([]map[string]any, 0, len(prs)); for _, p := range prs { reports = append(reports, map[string]any{"owner":parts[len(parts)-2],"repo":parts[len(parts)-1],"number":p.Number,"title":p.Title,"state":strings.ToLower(p.State),"url":p.URL,"branch":p.HeadRefName,"sha":p.HeadRefOid,"merged_at":p.MergedAt}) }
 return d.client.ReportDaemonPullRequests(ctx, task.WorkspaceID, reports)
}
