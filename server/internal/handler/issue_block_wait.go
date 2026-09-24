package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/ghsnapshot"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const blockPatrolLimit = 50

// gateBlockedStatus rejects an agent move into blocked that does not say what
// the issue is waiting on. Members can still drag a card; the patrol picks an
// unstructured one up. A non-empty rejection is a 400.
func (h *Handler) gateBlockedStatus(r *http.Request, issue db.Issue, req UpdateIssueRequest, actorType string) (blockwait.Record, bool, string) {
	rec, err := blockwait.Merge(parseIssueMetadata(issue.Metadata), blockwait.Input{
		BlockedBy:     deref(req.BlockedBy),
		WakeAt:        deref(req.WakeAt),
		WaitCondition: deref(req.WaitCondition),
		WaitProbe:     deref(req.WaitProbe),
		WaitTimeout:   deref(req.WaitTimeout),
		NeedsHuman:    deref(req.NeedsHuman),
	})
	if err != nil {
		return rec, false, err.Error()
	}
	persist := !blockInput(req).Empty()
	if rec.Structured() {
		return rec, persist, ""
	}
	if actorType != "agent" {
		return rec, false, ""
	}
	bodies := h.recentCommentBodies(r.Context(), issue)
	return rec, false, blockwait.Rejection(blockwait.SuggestFromComments(bodies))
}

func blockInput(req UpdateIssueRequest) blockwait.Input {
	return blockwait.Input{
		BlockedBy:     deref(req.BlockedBy),
		WakeAt:        deref(req.WakeAt),
		WaitCondition: deref(req.WaitCondition),
		WaitProbe:     deref(req.WaitProbe),
		WaitTimeout:   deref(req.WaitTimeout),
		NeedsHuman:    deref(req.NeedsHuman),
	}
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func (h *Handler) recentCommentBodies(ctx context.Context, issue db.Issue) []string {
	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       30,
	})
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(comments))
	for _, c := range comments {
		out = append(out, c.Content)
	}
	return out
}

func (h *Handler) persistBlockRecord(ctx context.Context, issue db.Issue, rec blockwait.Record) {
	for key, value := range rec.Pairs() {
		h.setIssueMetaString(ctx, issue, key, value)
	}
}

func (h *Handler) setIssueMetaString(ctx context.Context, issue db.Issue, key, value string) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	if _, err := h.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         key,
		Value:       raw,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("block wait: metadata write failed", "error", err, "issue_id", uuidToString(issue.ID), "key", key)
	}
}

// listBlockWaiters adds block.blocked_by matches to the close.waiting_on set.
func (h *Handler) listBlockWaiters(ctx context.Context, issue db.Issue, identifier string) []db.Issue {
	seen := map[string]db.Issue{}
	add := func(rows []db.Issue) {
		for _, row := range rows {
			seen[uuidToString(row.ID)] = row
		}
	}
	primary, err := h.Queries.ListIssuesWaitingOn(ctx, db.ListIssuesWaitingOnParams{
		WorkspaceID:         issue.WorkspaceID,
		WaitingOnIdentifier: waitingOnFilter(identifier),
		WaitingOnID:         waitingOnFilter(uuidToString(issue.ID)),
	})
	if err != nil {
		slog.Warn("waiting_on: failed to list waiters", "error", err, "issue_id", uuidToString(issue.ID))
	} else {
		add(primary)
	}
	for _, token := range []string{identifier, uuidToString(issue.ID)} {
		if !blockwaitToken(token) {
			continue
		}
		extra, err := h.Queries.ListIssuesBlockedByToken(ctx, db.ListIssuesBlockedByTokenParams{
			WorkspaceID: issue.WorkspaceID,
			Token:       token,
		})
		if err != nil {
			slog.Warn("block wait: failed to list blocked_by waiters", "error", err, "token", token)
			continue
		}
		add(extra)
	}
	out := make([]db.Issue, 0, len(seen))
	for _, row := range seen {
		out = append(out, row)
	}
	return out
}

func blockwaitToken(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if r == '%' || r == '_' || r == '\\' {
			return false
		}
	}
	return true
}

// noteBlockClearedOnSource leaves the human sentence on the issue that just
// unblocked someone. mention://issue does not start a run.
func (h *Handler) noteBlockClearedOnSource(ctx context.Context, completed, waiter db.Issue, waiterIdentifier string) {
	content := fmt.Sprintf(
		"挡路解除了，已经叫醒等待方 [%s](mention://issue/%s)。",
		waiterIdentifier, uuidToString(waiter.ID),
	)
	h.postBlockComment(ctx, completed, content)
}

func (h *Handler) postBlockComment(ctx context.Context, issue db.Issue, content string) db.Comment {
	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     content,
		Type:        "system",
		ParentID:    pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("block wait: create comment failed", "error", err, "issue_id", uuidToString(issue.ID))
		return db.Comment{}
	}
	comment := created.Comment()
	h.publish(protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), "system", "", map[string]any{
		"comment":             commentToResponse(comment, nil, nil),
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      created.IssueRevision,
	})
	return comment
}

// maybeReleaseOnAcceptance runs when a reviewer says the work passed and the
// issue is still in review. DENE-810 only told the reviewer to merge; this
// performs the close, or turns a real merge block into a structured wait.
func (h *Handler) maybeReleaseOnAcceptance(ctx context.Context, issue db.Issue, comment db.Comment) {
	if comment.AuthorType == "system" || comment.Type == "system" {
		return
	}
	if issue.Status != "in_review" {
		return
	}
	if !blockwait.IsAcceptancePass(comment.Content) {
		return
	}
	if !h.authorIsReviewer(issue, comment) {
		return
	}
	meta := parseIssueMetadata(issue.Metadata)
	if blockwait.MetaString(meta, blockwait.KeyReleased) == blockwait.ReleasedPass {
		return
	}
	h.setIssueMetaString(ctx, issue, blockwait.KeyReleased, blockwait.ReleasedPass)
	h.releaseAcceptedIssue(ctx, issue, blockwait.Decision{Reason: "验收已经通过。"})
}

func (h *Handler) authorIsReviewer(issue db.Issue, comment db.Comment) bool {
	if !issue.ReviewerType.Valid || !issue.ReviewerID.Valid || !comment.AuthorID.Valid {
		return false
	}
	return issue.ReviewerType.String == comment.AuthorType && issue.ReviewerID == comment.AuthorID
}

func (h *Handler) releaseAcceptedIssue(ctx context.Context, issue db.Issue, seed blockwait.Decision) {
	prs, err := h.Queries.ListPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		slog.Warn("block wait: list pull requests failed", "error", err, "issue_id", uuidToString(issue.ID))
		return
	}
	snapshots := make([]blockwait.PRSnapshot, 0, len(prs))
	for _, pr := range prs {
		snapshots = append(snapshots, blockwait.PRSnapshot{
			Number:    int(pr.PrNumber),
			State:     pr.State,
			Mergeable: pr.MergeableState.String,
			Checks:    pr.ChecksRollupState.String,
			URL:       pr.HtmlUrl,
		})
	}
	decision := blockwait.DecideRelease(snapshots, time.Now())
	if seed.Reason != "" && decision.Reason != "" {
		decision.Reason = seed.Reason + decision.Reason
	}
	switch decision.Action {
	case blockwait.ReleaseDone:
		h.finishAcceptedIssue(ctx, issue, decision.Reason)
	case blockwait.ReleaseBlock:
		h.blockAcceptedIssue(ctx, issue, decision)
	case blockwait.ReleaseMerge:
		h.mergeAcceptedIssue(ctx, issue, prs, decision)
	}
}

func (h *Handler) finishAcceptedIssue(ctx context.Context, issue db.Issue, reason string) {
	updated, err := h.Queries.CompleteIssueFromReview(ctx, db.CompleteIssueFromReviewParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Statuses:    []string{issue.Status},
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("block wait: close after pass failed", "error", err, "issue_id", uuidToString(issue.ID))
		}
		return
	}
	h.publishBlockStatus(issue, updated)
	h.postBlockComment(ctx, updated, reason)
	h.notifyParentOfChildDone(ctx, issue, updated)
	h.notifyWaitersOfIssueDone(ctx, issue, updated)
}

func (h *Handler) blockAcceptedIssue(ctx context.Context, issue db.Issue, decision blockwait.Decision) {
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Status:      "blocked",
	})
	if err != nil {
		slog.Warn("block wait: block after pass failed", "error", err, "issue_id", uuidToString(issue.ID))
		return
	}
	h.persistBlockRecord(ctx, updated, decision.Record)
	h.publishBlockStatus(issue, updated)
	h.postBlockComment(ctx, updated, decision.Reason)
}

func (h *Handler) mergeAcceptedIssue(ctx context.Context, issue db.Issue, prs []db.ListPullRequestsByIssueRow, decision blockwait.Decision) {
	var open *db.ListPullRequestsByIssueRow
	for i := range prs {
		if prs[i].State == "open" {
			open = &prs[i]
			break
		}
	}
	if open == nil {
		h.finishAcceptedIssue(ctx, issue, decision.Reason)
		return
	}
	err := h.mergePullRequest(ctx, open.InstallationID, open.RepoOwner, open.RepoName, int(open.PrNumber))
	if err == nil {
		h.finishAcceptedIssue(ctx, issue, decision.Reason+" PR 已合并。")
		return
	}
	decision.Action = blockwait.ReleaseBlock
	if errors.Is(err, errPullMergeUnavailable) {
		decision.Reason = decision.Reason + " 这台服务没有合并权限，已改成阻塞并叫醒执行人去合并。"
		decision.Record.WaitCondition = "验收已通过，等待执行人合并 " + open.HtmlUrl
	} else if errors.Is(err, errPullNotMergeable) {
		decision.Reason = fmt.Sprintf("验收已经通过，但 %s 现在合不进去。先标成阻塞，到点再看。", open.HtmlUrl)
		decision.Record.WaitCondition = open.HtmlUrl + " 合不进去"
	} else {
		decision.Reason = "验收已经通过，合并没有成功。先标成阻塞，到点再试。"
		decision.Record.WaitCondition = "合并 " + open.HtmlUrl + " 没有成功"
	}
	if !decision.Record.HasWakeAt {
		decision.Record.HasWakeAt = true
		decision.Record.WakeAt = time.Now().Add(blockwait.QuietAfter).UTC()
	}
	h.blockAcceptedIssue(ctx, issue, decision)
	if errors.Is(err, errPullMergeUnavailable) {
		h.wakeIssueOwner(ctx, issue, "验收已经通过。请合并关联的 PR，然后把这张票关了。合不进去就让它停在阻塞上。")
	}
}

var (
	errPullMergeUnavailable = errors.New("pull merge unavailable")
	errPullNotMergeable     = errors.New("pull request is not mergeable")
)

func (h *Handler) mergePullRequest(ctx context.Context, installationID int64, owner, repo string, number int) error {
	if h.PRMerger != nil {
		return h.PRMerger.MergePullRequest(ctx, installationID, owner, repo, number)
	}
	if h.PRRefresh == nil || !h.PRRefresh.Enabled() {
		return errPullMergeUnavailable
	}
	err := h.PRRefresh.MergePullRequest(ctx, installationID, owner, repo, number)
	if err == nil {
		return nil
	}
	if errors.Is(err, ghsnapshot.ErrNotMergeable) {
		return errPullNotMergeable
	}
	return err
}

func (h *Handler) publishBlockStatus(prev, issue db.Issue) {
	if h.Bus == nil {
		return
	}
	h.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		Payload:     RoutingIssueUpdatedPayload(prev, issue),
	})
}

// SweepBlockWaits is the periodic backstop. It wakes a blocked or in-review
// issue whose wait is missing or already due and that has no run in flight.
func (h *Handler) SweepBlockWaits(ctx context.Context) (int, error) {
	if h == nil || h.Queries == nil {
		return 0, nil
	}
	rows, err := h.Queries.ListBlockPatrolCandidates(ctx, db.ListBlockPatrolCandidatesParams{
		QuietBefore: pgtype.Timestamptz{Time: time.Now().Add(-blockwait.QuietAfter), Valid: true},
		RowLimit:    blockPatrolLimit,
	})
	if err != nil {
		return 0, err
	}
	acted := 0
	for _, issue := range rows {
		if ctx.Err() != nil {
			return acted, ctx.Err()
		}
		if h.patrolOne(ctx, issue) {
			acted++
		}
	}
	return acted, nil
}

func (h *Handler) patrolOne(ctx context.Context, issue db.Issue) bool {
	meta := parseIssueMetadata(issue.Metadata)
	rec := blockwait.ParseMetadata(meta)
	blockers := h.blockerViews(ctx, issue, rec)
	quiet := time.Since(activityTime(issue))
	var last time.Time
	hasLast := false
	if raw := blockwait.MetaString(meta, blockwait.KeyPatrolAt); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			last = t
			hasLast = true
		}
	}
	bodies := h.recentCommentBodies(ctx, issue)
	pass := false
	for _, body := range bodies {
		if blockwait.IsAcceptancePass(body) {
			pass = true
			break
		}
	}
	decision := blockwait.DecidePatrol(blockwait.PatrolInput{
		Status:         issue.Status,
		Quiet:          quiet,
		Record:         rec,
		Blockers:       blockers,
		Now:            time.Now(),
		LastPatrol:     last,
		HasLastPatrol:  hasLast,
		HasPassComment: pass && h.reviewerWrotePass(ctx, issue, bodies),
		ReleasedPass:   blockwait.MetaString(meta, blockwait.KeyReleased) == blockwait.ReleasedPass,
	})
	switch decision.Action {
	case blockwait.ActionRelease:
		h.setIssueMetaString(ctx, issue, blockwait.KeyPatrolAt, time.Now().UTC().Format(time.RFC3339))
		h.releaseAcceptedIssue(ctx, issue, decision)
		return true
	case blockwait.ActionWake:
		h.setIssueMetaString(ctx, issue, blockwait.KeyPatrolAt, time.Now().UTC().Format(time.RFC3339))
		h.wakeIssueOwner(ctx, issue, decision.Reason)
		return true
	default:
		return false
	}
}

func (h *Handler) reviewerWrotePass(ctx context.Context, issue db.Issue, bodies []string) bool {
	if !issue.ReviewerID.Valid {
		return false
	}
	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Limit: 30,
	})
	if err != nil {
		return false
	}
	for _, c := range comments {
		if c.AuthorID == issue.ReviewerID && blockwait.IsAcceptancePass(c.Content) {
			return true
		}
	}
	return false
}

func (h *Handler) blockerViews(ctx context.Context, issue db.Issue, rec blockwait.Record) []blockwait.BlockerView {
	var out []blockwait.BlockerView
	for _, ref := range rec.BlockedBy {
		view := blockwait.BlockerView{Ref: ref, Status: "unknown"}
		other, ok := h.lookupBlocker(ctx, issue.WorkspaceID, ref)
		if ok {
			view.Status = other.Status
			view.Accepted = blockwait.MetaString(parseIssueMetadata(other.Metadata), blockwait.KeyReleased) == blockwait.ReleasedPass &&
				(other.Status == "done" || other.Status == "in_review")
		}
		out = append(out, view)
	}
	return out
}

func (h *Handler) lookupBlocker(ctx context.Context, workspaceID pgtype.UUID, ref string) (db.Issue, bool) {
	if id, err := util.ParseUUID(ref); err == nil {
		issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: workspaceID})
		return issue, err == nil
	}
	number := identifierNumber(ref)
	if number <= 0 {
		return db.Issue{}, false
	}
	issue, err := h.Queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{WorkspaceID: workspaceID, Number: number})
	return issue, err == nil
}

func identifierNumber(ref string) int32 {
	dash := -1
	for i := len(ref) - 1; i >= 0; i-- {
		if ref[i] == '-' {
			dash = i
			break
		}
	}
	if dash < 0 || dash == len(ref)-1 {
		return 0
	}
	var n int32
	for _, r := range ref[dash+1:] {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int32(r-'0')
	}
	return n
}

func activityTime(issue db.Issue) time.Time {
	if issue.LastActivityAt.Valid {
		return issue.LastActivityAt.Time
	}
	if issue.UpdatedAt.Valid {
		return issue.UpdatedAt.Time
	}
	return time.Now()
}

// wakeIssueOwner starts the assignee (or the reviewer, while in review) and
// leaves a sentence. A disabled seat is named and not replaced here — that
// handoff belongs to the disabled-seat path.
func (h *Handler) wakeIssueOwner(ctx context.Context, issue db.Issue, reason string) {
	targetType, targetID := issue.AssigneeType, issue.AssigneeID
	if issue.Status == "in_review" && issue.ReviewerType.Valid && issue.ReviewerID.Valid && issue.ReviewerType.String != "none" {
		targetType, targetID = issue.ReviewerType, issue.ReviewerID
	}
	mention := ""
	if targetType.Valid && targetID.Valid && (targetType.String == "agent" || targetType.String == "squad") {
		mention = h.buildParentAssigneeMention(ctx, db.Issue{AssigneeType: targetType, AssigneeID: targetID, WorkspaceID: issue.WorkspaceID})
	}
	comment := h.postBlockComment(ctx, issue, mention+reason)
	if !targetType.Valid || !targetID.Valid {
		return
	}
	waker := issue
	waker.AssigneeType = targetType
	waker.AssigneeID = targetID
	if comment.ID.Valid {
		h.dispatchWaitingOnAssigneeTrigger(ctx, waker, comment.ID)
	}
}
