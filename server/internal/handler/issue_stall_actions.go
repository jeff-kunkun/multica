package handler

// The stall action protocol deliberately lives on issue metadata.  It keeps
// the feature backwards compatible with installed clients (unknown metadata
// keys are ignored), while comments and inbox rows remain the durable audit
// trail visible on every surface.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	stallActionKey       = "stall.action"
	stallPreviousKey     = "stall.previous_status"
	stallAnnouncedAtKey  = "stall.announced_at"
	stallReviewUntilKey  = "stall.review_until"
	stallRevertUntilKey  = "stall.revert_until"
	stallReasonKey       = "stall.reason"
	stallCandidateKey    = "stall.candidate"
	stallActionCandidate = "candidate"
	stallActionAnnounced = "announced"
	stallActionKept      = "kept"
	stallActionCancelled = "cancelled"
	stallActionParent    = "parent_completed"
	stallActionRevoked   = "revoked"
	stallQuietAfter      = 36 * time.Hour
	stallAnnouncementFor = 24 * time.Hour
	stallRevertFor       = 7 * 24 * time.Hour
)

type stallIssueRow struct {
	ID          pgtype.UUID
	WorkspaceID pgtype.UUID
	Number      int32
	Title       string
	Status      string
	Metadata    []byte
	LastActive  pgtype.Timestamptz
}

func allChildrenTerminal(children []db.Issue, terminal func(db.Issue) bool) bool {
	if len(children) == 0 {
		return false
	}
	for _, child := range children {
		if !terminal(child) {
			return false
		}
	}
	return true
}

func parentAutoCompletionEligible(status string, paused, activeRun, hasPullRequest bool, childCount int, childrenTerminal bool) bool {
	if status == "done" || status == "cancelled" || status == "backlog" || paused || activeRun || hasPullRequest {
		return false
	}
	return childCount > 0 && childrenTerminal
}

func stallMeta(issue db.Issue) map[string]any { return util.JSONObjectOrEmpty(issue.Metadata) }

func stallString(meta map[string]any, key string) string {
	if v, ok := meta[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func stallBool(meta map[string]any, key string) bool {
	v, _ := meta[key].(bool)
	return v
}

func (h *Handler) setStallMetadata(ctx context.Context, issue db.Issue, values map[string]string) error {
	for key, value := range values {
		raw, _ := json.Marshal(value)
		if _, err := h.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: key, Value: raw}); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) clearCloseMetadata(ctx context.Context, issue db.Issue) {
	for _, key := range closeprotocol.Keys {
		_, _ = h.Queries.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: key})
	}
}

func (h *Handler) stallComment(ctx context.Context, issue db.Issue, body string) pgtype.UUID {
	commentID := dbid.NewV7()
	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID: commentID, IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
		AuthorType: "system", AuthorID: pgtype.UUID{Valid: true}, Content: body,
		Type: "system", ParentID: pgtype.UUID{Valid: false},
	})
	if err != nil {
		return pgtype.UUID{}
	}
	if h.Bus != nil {
		h.Bus.Publish(events.Event{Type: protocol.EventCommentCreated, WorkspaceID: util.UUIDToString(issue.WorkspaceID), ActorType: "system", Payload: map[string]any{
			"comment": commentToResponse(created.Comment(), nil, nil), "issue_title": issue.Title,
			"issue_status": issue.Status, "issue_revision": created.IssueRevision,
		}})
	}
	return commentID
}

func (h *Handler) notifyStallInbox(ctx context.Context, issue db.Issue, title, body string) {
	// The summary belongs to the workspace owner/admins even when the ticket is
	// assigned to an agent. Keep the assigned member as an additional recipient.
	recipients := make(map[string]pgtype.UUID)
	if issue.AssigneeType.Valid && issue.AssigneeType.String == "member" && issue.AssigneeID.Valid {
		recipients[util.UUIDToString(issue.AssigneeID)] = issue.AssigneeID
	}
	rows, err := h.DB.Query(ctx, `SELECT user_id FROM member WHERE workspace_id=$1 AND role IN ('owner','admin')`, issue.WorkspaceID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id pgtype.UUID
			if rows.Scan(&id) == nil && id.Valid {
				recipients[util.UUIDToString(id)] = id
			}
		}
	}
	entry := fmt.Sprintf("%s：票 #%d（%s）\n%s", title, issue.Number, issue.Title, body)
	for _, recipient := range recipients {
		var existingID pgtype.UUID
		var existingBody pgtype.Text
		err := h.DB.QueryRow(ctx, `SELECT id, body FROM inbox_item WHERE workspace_id=$1 AND recipient_type='member' AND recipient_id=$2 AND type='issue_stall_action' AND created_at >= date_trunc('day', now()) ORDER BY created_at LIMIT 1`, issue.WorkspaceID, recipient).Scan(&existingID, &existingBody)
		if err == nil && existingID.Valid {
			_, _ = h.DB.Exec(ctx, `UPDATE inbox_item SET body=$2 WHERE id=$1`, existingID, strings.TrimSpace(existingBody.String+"\n\n"+entry))
			continue
		}
		_, _ = h.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
			ID: dbid.NewV7(), WorkspaceID: issue.WorkspaceID, RecipientType: "member", RecipientID: recipient,
			Type: "issue_stall_action", Severity: "attention", IssueID: issue.ID, Title: "停滞处理每日汇总",
			Body: pgtype.Text{String: entry, Valid: true}, ActorType: pgtype.Text{String: "system", Valid: true},
			ActorID: pgtype.UUID{Valid: true}, Details: []byte(fmt.Sprintf(`{"kind":"stall_action","issue_id":"%s","actions":["keep","undo"]}`, util.UUIDToString(issue.ID))),
		})
	}
}

func (h *Handler) stallArtifactCheck(ctx context.Context, issue db.Issue) string {
	prs, _ := h.Queries.ListPullRequestsByIssue(ctx, issue.ID)
	attachments, _ := h.Queries.ListAttachmentsByIssue(ctx, db.ListAttachmentsByIssueParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	return fmt.Sprintf("已核对关联 PR（%d 个）、附件（%d 个）与其他交付产物", len(prs), len(attachments))
}

// autoCompleteParent is called by the periodic stall patrol after the last
// child status has been quiet for long enough. The update is deterministic: no
// routing model, PR gate, or agent decision is involved. A close record /
// explicit pause keeps the parent contract visible, and the old status plus a
// seven-day expiry makes the action reversible from all clients.
func (h *Handler) autoCompleteParent(ctx context.Context, parent, child db.Issue) error {
	if parent.Status == "done" || parent.Status == "cancelled" {
		return nil
	}
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: parent.ID, Status: "done", WorkspaceID: parent.WorkspaceID})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	body := fmt.Sprintf("所有子任务均已完成（最后完成：%s）。系统按停滞规则将父票从 `%s` 自动收口为 `done`。7 天内可用“撤销停滞处理”恢复。", child.Title, parent.Status)
	commentID := h.stallComment(ctx, updated, body)
	if !commentID.Valid {
		return fmt.Errorf("create auto-close evidence comment")
	}
	if err := h.setStallMetadata(ctx, updated, map[string]string{
		stallActionKey: stallActionParent, stallPreviousKey: parent.Status,
		stallAnnouncedAtKey: now.Format(time.RFC3339), stallRevertUntilKey: now.Add(stallRevertFor).Format(time.RFC3339),
		stallReasonKey: "所有子任务均已完成，系统按规则自动收口；可在 7 天内撤销。",
	}); err != nil {
		return err
	}
	// Automatic closure still records the same close.* contract as a normal
	// delivered close, so the parent review barrier can distinguish an actual
	// close from a bare status mutation.
	rec := closeRecordAfterRelease(updated, stallMeta(updated), util.UUIDToString(commentID), "")
	if rec != nil && h.TxStarter != nil {
		if err := closeprotocol.Validate(rec, updated.Status, body); err == nil {
			if err := h.writeCloseKeysTx(ctx, updated, rec); err != nil {
				return err
			}
		}
	}
	h.notifyStallInbox(ctx, updated, "父票已自动收口", "所有子任务已完成，父票已标记 done；7 天内可撤销。")
	if fresh, err := h.Queries.GetIssue(ctx, updated.ID); err == nil {
		h.publish(protocol.EventIssueUpdated, util.UUIDToString(fresh.WorkspaceID), "system", "", map[string]any{"issue": service.IssueToMapResolved(ctx, h.Queries, fresh, h.getIssuePrefix(ctx, fresh.WorkspaceID))})
	}
	return nil
}

// KeepStallAction prevents an announced candidate from being cancelled.
func (h *Handler) KeepStallAction(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	meta := stallMeta(issue)
	if stallString(meta, stallActionKey) != stallActionAnnounced {
		writeError(w, http.StatusConflict, "这张票当前没有待保留的停滞公示")
		return
	}
	if until, err := time.Parse(time.RFC3339, stallString(meta, stallReviewUntilKey)); err != nil || time.Now().UTC().After(until) {
		writeError(w, http.StatusConflict, "公示期已结束")
		return
	}
	if err := h.setStallMetadata(r.Context(), issue, map[string]string{stallActionKey: stallActionKept, stallReasonKey: "Kun 在公示期内选择保留，系统不会自动取消。"}); err != nil {
		writeError(w, 500, "failed to keep stall action")
		return
	}
	h.stallComment(r.Context(), issue, "公示期内收到“保留”操作，这张票继续保留，系统不会自动取消。")
	writeJSON(w, http.StatusOK, map[string]any{"action": stallActionKept, "issue_id": util.UUIDToString(issue.ID)})
}

// UndoStallAction restores the status captured by an automatic close/cancel.
func (h *Handler) UndoStallAction(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	meta := stallMeta(issue)
	action := stallString(meta, stallActionKey)
	if action != stallActionParent && action != stallActionCancelled {
		writeError(w, http.StatusConflict, "这张票没有可撤销的自动停滞处理")
		return
	}
	until, err := time.Parse(time.RFC3339, stallString(meta, stallRevertUntilKey))
	if err != nil || time.Now().UTC().After(until) {
		writeError(w, http.StatusConflict, "撤销期限已过")
		return
	}
	previous := stallString(meta, stallPreviousKey)
	if previous == "" {
		writeError(w, http.StatusConflict, "缺少自动处理前的状态，无法安全撤销")
		return
	}
	updated, err := h.Queries.UpdateIssueStatus(r.Context(), db.UpdateIssueStatusParams{ID: issue.ID, Status: previous, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, 500, "failed to restore issue status")
		return
	}
	h.clearCloseMetadata(r.Context(), updated)
	_ = h.setStallMetadata(r.Context(), updated, map[string]string{stallActionKey: stallActionRevoked, stallReasonKey: "自动停滞处理已撤销。"})
	h.stallComment(r.Context(), updated, fmt.Sprintf("已撤销系统自动停滞处理，票状态恢复为 `%s`。", previous))
	h.notifyStallInbox(r.Context(), updated, "停滞处理已撤销", "自动收口/取消已撤销，票状态已恢复。")
	if fresh, err := h.Queries.GetIssue(r.Context(), updated.ID); err == nil {
		h.publish(protocol.EventIssueUpdated, util.UUIDToString(fresh.WorkspaceID), "member", "", map[string]any{"issue": service.IssueToMapResolved(r.Context(), h.Queries, fresh, h.getIssuePrefix(r.Context(), fresh.WorkspaceID))})
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": stallActionRevoked, "status": updated.Status, "issue_id": util.UUIDToString(updated.ID)})
}

// ReviewStallAction is the AI-to-server handoff. The model (or a trusted
// caller) supplies its reason; the server always appends an artifact check
// before announcing and never allows a reason that omits that check.
func (h *Handler) ReviewStallAction(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Reason) == "" {
		writeError(w, 400, "reason is required")
		return
	}
	if closeprotocol.ExplainedPause(blockwait.MetaString(stallMeta(issue), closeprotocol.KeyConclusion)) {
		writeError(w, 409, "有意暂停的票不会进入停滞处理")
		return
	}
	now := time.Now().UTC()
	reason := strings.TrimSpace(req.Reason) + "；" + h.stallArtifactCheck(r.Context(), issue) + "。"
	if err := h.setStallMetadata(r.Context(), issue, map[string]string{stallActionKey: stallActionAnnounced, stallAnnouncedAtKey: now.Format(time.RFC3339), stallReviewUntilKey: now.Add(stallAnnouncementFor).Format(time.RFC3339), stallReasonKey: reason}); err != nil {
		writeError(w, 500, "failed to announce stall action")
		return
	}
	h.stallComment(r.Context(), issue, fmt.Sprintf("AI 停滞巡检公示：%s\n公示 24 小时内可选择“保留”；无人拦截将自动取消。", reason))
	h.notifyStallInbox(r.Context(), issue, "停滞票进入 24 小时公示", reason)
	writeJSON(w, http.StatusOK, map[string]any{"action": stallActionAnnounced, "review_until": now.Add(stallAnnouncementFor), "reason": reason})
}

// ListStallActions is consumed by the CLI and the inbox/detail surfaces.
func (h *Handler) ListStallActions(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, 400, "workspace_id is required")
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT id, workspace_id, number, title, status, metadata, last_activity_at FROM issue WHERE workspace_id=$1 AND metadata ? 'stall.action' ORDER BY updated_at DESC LIMIT 500`, ws)
	if err != nil {
		writeError(w, 500, "failed to list stall actions")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var x stallIssueRow
		if err := rows.Scan(&x.ID, &x.WorkspaceID, &x.Number, &x.Title, &x.Status, &x.Metadata, &x.LastActive); err != nil {
			continue
		}
		meta := stallMeta(db.Issue{Metadata: x.Metadata})
		items = append(items, map[string]any{"issue_id": util.UUIDToString(x.ID), "number": x.Number, "title": x.Title, "status": x.Status, "action": stallString(meta, stallActionKey), "reason": stallString(meta, stallReasonKey), "review_until": stallString(meta, stallReviewUntilKey), "revert_until": stallString(meta, stallRevertUntilKey)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// SweepStallActions is called by the DB scheduler every 30 minutes. Candidates
// are marked by the AI review handoff; this job only owns the clock and the
// deterministic expiry/cancel transition.
func (h *Handler) SweepStallActions(ctx context.Context) (int64, error) {
	var affected int64
	parentRows, err := h.DB.Query(ctx, `SELECT id, workspace_id, number, title, status, metadata, last_activity_at FROM issue WHERE status IN ('todo','in_progress','blocked') AND metadata->>'stall.action' IS DISTINCT FROM 'revoked' AND last_activity_at < now() - interval '36 hours' AND EXISTS (SELECT 1 FROM issue child WHERE child.parent_issue_id=issue.id) LIMIT 500`)
	if err != nil {
		return 0, err
	}
	for parentRows.Next() {
		var x stallIssueRow
		if parentRows.Scan(&x.ID, &x.WorkspaceID, &x.Number, &x.Title, &x.Status, &x.Metadata, &x.LastActive) != nil {
			continue
		}
		issue, err := h.Queries.GetIssue(ctx, x.ID)
		if err != nil {
			continue
		}
		meta := stallMeta(issue)
		if closeprotocol.ExplainedPause(blockwait.MetaString(meta, closeprotocol.KeyConclusion)) {
			continue
		}
		if h.parentReadyForAutoCompletion(ctx, issue) {
			children, _ := h.Queries.ListChildIssues(ctx, issue.ID)
			if len(children) > 0 {
				if err := h.autoCompleteParent(ctx, issue, children[len(children)-1]); err == nil {
					affected++
				}
			}
		}
	}
	parentRows.Close()

	// The announcement clock is independent from issue activity: posting the
	// public notice itself must not restart the 24-hour window.
	rows, err := h.DB.Query(ctx, `SELECT id, workspace_id, number, title, status, metadata, last_activity_at FROM issue WHERE status NOT IN ('done','cancelled') AND metadata->>'stall.action'='announced' LIMIT 500`)
	if err != nil {
		return affected, err
	}
	defer rows.Close()
	for rows.Next() {
		var x stallIssueRow
		if rows.Scan(&x.ID, &x.WorkspaceID, &x.Number, &x.Title, &x.Status, &x.Metadata, &x.LastActive) != nil {
			continue
		}
		issue, err := h.Queries.GetIssue(ctx, x.ID)
		if err != nil {
			continue
		}
		meta := stallMeta(issue)
		if closeprotocol.ExplainedPause(blockwait.MetaString(meta, closeprotocol.KeyConclusion)) {
			continue
		}
		action := stallString(meta, stallActionKey)
		if action == stallActionAnnounced {
			until, err := time.Parse(time.RFC3339, stallString(meta, stallReviewUntilKey))
			if err == nil && !time.Now().UTC().Before(until) {
				if err := h.cancelStallIssue(ctx, issue); err == nil {
					affected++
				}
			}
		}
	}
	return affected, nil
}

// parentReadyForAutoCompletion is the quiet-parent rule. Child completion
// notifications remain synchronous and only wake the parent; this check is
// intentionally owned by the periodic stall patrol so an active run or any
// linked delivery can finish the parent before the rule fires.
func (h *Handler) parentReadyForAutoCompletion(ctx context.Context, parent db.Issue) bool {
	meta := stallMeta(parent)
	paused := closeprotocol.ExplainedPause(blockwait.MetaString(meta, closeprotocol.KeyConclusion))
	active, err := h.Queries.HasActiveTaskForIssue(ctx, parent.ID)
	if err != nil {
		return false
	}
	prs, err := h.Queries.ListPullRequestsByIssue(ctx, parent.ID)
	if err != nil {
		return false
	}
	children, err := h.Queries.ListChildIssues(ctx, parent.ID)
	if err != nil || len(children) == 0 {
		return false
	}
	effective := h.childStatusResolver(ctx)
	statuses, err := resolveChildStatuses(children, effective)
	if err != nil {
		return false
	}
	return parentAutoCompletionEligible(parent.Status, paused, active, len(prs) > 0, len(children), allChildrenTerminal(children, h.realChildTerminalPredicate(ctx, statuses)))
}

func (h *Handler) ReviewStallActionInternal(ctx context.Context, issue db.Issue, reason string) error {
	now := time.Now().UTC()
	reason = strings.TrimSpace(reason) + "；" + h.stallArtifactCheck(ctx, issue) + "。"
	if err := h.setStallMetadata(ctx, issue, map[string]string{stallActionKey: stallActionAnnounced, stallAnnouncedAtKey: now.Format(time.RFC3339), stallReviewUntilKey: now.Add(stallAnnouncementFor).Format(time.RFC3339), stallReasonKey: reason}); err != nil {
		return err
	}
	h.stallComment(ctx, issue, "AI 停滞巡检公示："+reason+"\n公示 24 小时内可选择“保留”；无人拦截将自动取消。")
	h.notifyStallInbox(ctx, issue, "停滞票进入 24 小时公示", reason)
	return nil
}

func (h *Handler) cancelStallIssue(ctx context.Context, issue db.Issue) error {
	meta := stallMeta(issue)
	now := time.Now().UTC()
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: issue.ID, Status: "cancelled", WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return err
	}
	reason := stallString(meta, stallReasonKey) + " 公示到期无人保留，系统自动取消。"
	if err = h.setStallMetadata(ctx, updated, map[string]string{stallActionKey: stallActionCancelled, stallPreviousKey: issue.Status, stallRevertUntilKey: now.Add(stallRevertFor).Format(time.RFC3339), stallReasonKey: reason}); err != nil {
		return err
	}
	h.stallComment(ctx, updated, "AI 停滞巡检："+reason+" 7 天内可撤销。")
	h.notifyStallInbox(ctx, updated, "停滞票已自动取消", reason)
	if fresh, err := h.Queries.GetIssue(ctx, updated.ID); err == nil {
		h.publish(protocol.EventIssueUpdated, util.UUIDToString(fresh.WorkspaceID), "system", "", map[string]any{"issue": service.IssueToMapResolved(ctx, h.Queries, fresh, h.getIssuePrefix(ctx, fresh.WorkspaceID))})
	}
	return nil
}
