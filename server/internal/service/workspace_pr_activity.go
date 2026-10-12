package service

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

// PullRequestActivity turns one pull_request:* bus event into the workspace
// stream's input (DENE-1936 review S2). A PR mirror has no access of its own: it
// is read through the tickets it is linked to, so the event carries every
// linked ticket and its repository, and the person must be able to open all of
// them. A PR linked to no ticket is not told (ok is false).
func PullRequestActivity(workspaceID pgtype.UUID, activityType, verb string, payload any) (WorkspaceActivityInput, bool) {
	m, _ := payload.(map[string]any)
	pr := prFromPayload(m)
	if pr["id"] == "" {
		return WorkspaceActivityInput{}, false
	}
	var target WriteTarget
	seen := map[[16]byte]bool{}
	for _, raw := range stringList(m["linked_issue_ids"]) {
		id, err := util.ParseUUID(raw)
		if err != nil || !id.Valid || seen[id.Bytes] {
			continue
		}
		seen[id.Bytes] = true
		target.IssueIDs = append(target.IssueIDs, id)
	}
	if len(target.IssueIDs) == 0 {
		return WorkspaceActivityInput{}, false
	}
	target.IssueID = target.IssueIDs[0]
	target.Repo = pr["repo"]
	attrs := map[string]string{}
	for _, k := range []string{"number", "state", "title", "html_url", "repo", "merged_at"} {
		if pr[k] != "" {
			attrs[k] = pr[k]
		}
	}
	return WorkspaceActivityInput{
		WorkspaceID:  workspaceID,
		Target:       target,
		ActivityType: activityType,
		DedupeKey:    fmt.Sprintf("ws:pr:%s:%s:%s:%s:%s", pr["id"], verb, pr["state"], pr["merged_at"], pr["pr_updated_at"]),
		Body: WorkspaceEventBody{
			Family: FamilyPR, Action: verb, ActorKind: "system", EntityID: pr["id"], Attrs: attrs,
		},
		ActorType:  "system",
		Importance: ImportanceNormal,
		Categories: []string{CategoryProgress},
		OccurredAt: time.Now(),
	}, true
}

// stringList reads a list of ids whatever slice type the producer used.
func stringList(v any) []string {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []string
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// prFromPayload flattens the "pull_request" value of a pull_request:* event to
// the handful of plain strings the stream carries. Producers send a typed
// response struct, so it goes through JSON rather than a type switch.
func prFromPayload(m map[string]any) map[string]string {
	out := map[string]string{}
	if m == nil {
		return out
	}
	raw, err := json.Marshal(m["pull_request"])
	if err != nil {
		return out
	}
	var pr map[string]any
	if json.Unmarshal(raw, &pr) != nil {
		return out
	}
	str := func(k string) string {
		switch v := pr[k].(type) {
		case string:
			return v
		case float64:
			return fmt.Sprintf("%d", int64(v))
		}
		return ""
	}
	for _, k := range []string{"id", "number", "state", "title", "html_url", "merged_at", "pr_updated_at"} {
		out[k] = str(k)
	}
	if owner, name := str("repo_owner"), str("repo_name"); owner != "" && name != "" {
		out["repo"] = owner + "/" + name
	}
	return out
}
