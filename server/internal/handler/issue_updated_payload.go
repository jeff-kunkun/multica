package handler

import db "github.com/multica-ai/multica/server/pkg/db/generated"

const unseatedReason = "缺执行人：在执行人栏选一个智能体即可（命令行 `multica issue assign <票号> --to <名字>`）。"

// RoutingIssueUpdatedPayload builds the issue:updated payload for a
// server-side issue write. Exported so the listener test in cmd/server can
// drive the REAL payload through the REAL activity listener.
//
// The types matter as much as the keys. The listeners read the previous pair
// with `payload["prev_assignee_type"].(*string)`, so a plain string here would
// type-assert to nil and silently drop the "from" half of the record.
func RoutingIssueUpdatedPayload(prev, issue db.Issue) map[string]any {
	assigneeChanged := prev.AssigneeType.String != issue.AssigneeType.String ||
		uuidToString(prev.AssigneeID) != uuidToString(issue.AssigneeID)
	return map[string]any{
		"issue":              issueToResponse(issue, ""),
		"assignee_changed":   assigneeChanged,
		"status_changed":     prev.Status != issue.Status,
		"prev_status":        prev.Status,
		"prev_assignee_type": textToPtr(prev.AssigneeType),
		"prev_assignee_id":   uuidToPtr(prev.AssigneeID),
		"creator_type":       issue.CreatorType,
		"creator_id":         uuidToString(issue.CreatorID),
	}
}
