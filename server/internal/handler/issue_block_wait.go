package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
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
	rec, err := blockwait.Accept(parseIssueMetadata(issue.Metadata), blockwait.Input{
		BlockedBy:     deref(req.BlockedBy),
		WakeAt:        deref(req.WakeAt),
		WaitCondition: deref(req.WaitCondition),
		WaitProbe:     deref(req.WaitProbe),
		WaitTimeout:   deref(req.WaitTimeout),
		NeedsHuman:    deref(req.NeedsHuman),
	}, time.Now())
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
	// A new wait record starts a fresh probe lifecycle on every block path
	// (close, status, batch, accept): a result from the previous wait must
	// not suppress this one's wakeup.
	for _, key := range []string{blockwait.KeyProbeStatus, blockwait.KeyProbeAt, blockwait.KeyProbeOutput, blockwait.KeyProbeNotified} {
		h.deleteIssueMeta(ctx, issue, key)
	}
	for key, value := range rec.Pairs() {
		h.setIssueMetaString(ctx, issue, key, value)
	}
}

func (h *Handler) deleteIssueMeta(ctx context.Context, issue db.Issue, key string) {
	if _, err := h.Queries.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         key,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("block wait: metadata delete failed", "error", err, "issue_id", uuidToString(issue.ID), "key", key)
	}
}

// syncBlockWait drops a wait that no longer belongs to the status the issue
// just entered, and marks a newly blocked or in-review issue as watched so
// the patrol does not walk tickets that were already sitting there.
func (h *Handler) syncBlockWait(ctx context.Context, prev, next db.Issue) {
	if prev.Status == next.Status {
		return
	}
	if prev.Status == "blocked" && next.Status != "blocked" {
		for _, key := range blockwait.WaitKeys() {
			h.deleteIssueMeta(ctx, next, key)
		}
		// block.watched is not a wait key, but a row that left blocked must
		// stop being a patrol candidate: DENE-1002 added in_progress to the
		// sweep, and a plain blocked -> in_progress move would otherwise stay
		// watched forever with nothing to wake for.
		h.deleteIssueMeta(ctx, next, blockwait.KeyWatched)
	}
	if prev.Status == "in_review" && next.Status != "in_review" {
		h.deleteIssueMeta(ctx, next, blockwait.KeyReleased)
		h.deleteIssueMeta(ctx, next, blockwait.KeyReviewNudged)
	}
	if prev.Status == "done" && next.Status != "done" {
		h.deleteIssueMeta(ctx, next, blockwait.KeyReleased)
	}
	if next.Status == "in_review" && prev.Status != "in_review" {
		for _, key := range blockwait.ClockKeys() {
			h.deleteIssueMeta(ctx, next, key)
		}
		h.deleteIssueMeta(ctx, next, blockwait.KeyReleased)
		h.deleteIssueMeta(ctx, next, blockwait.KeyReviewNudged)
		h.setIssueMetaString(ctx, next, blockwait.KeyReviewRound, time.Now().UTC().Format(time.RFC3339))
		h.setIssueMetaString(ctx, next, blockwait.KeyWatched, blockwait.WatchedYes)
	}
	if next.Status == "blocked" && prev.Status != "blocked" {
		h.setIssueMetaString(ctx, next, blockwait.KeyWatched, blockwait.WatchedYes)
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
		ViaBotID: botsource.FromContext(ctx).ID, ViaBotName: pgtype.Text{String: botsource.FromContext(ctx).Name, Valid: botsource.FromContext(ctx).ID.Valid},
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
	h.publishWithSource(ctx, protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), "system", "", map[string]any{
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
// issue is still in review, and closes it.
func (h *Handler) maybeReleaseOnAcceptance(ctx context.Context, issue db.Issue, comment db.Comment) {
	if comment.AuthorType == "system" || comment.Type == "system" {
		return
	}
	if issue.Status != "in_review" {
		return
	}
	if !h.authorIsReviewer(issue, comment) {
		return
	}
	if !blockwait.IsAcceptancePass(comment.Content) {
		if blockwait.LooksLikePassHint(comment.Content) {
			h.postBlockComment(ctx, issue, "这句看起来像验收通过。平台只认单独一行的 verdict: pass，或者评论时带上 --verdict pass。写在句子里的「通过」不会关票。")
		}
		return
	}
	h.releaseOnAcceptance(ctx, issue)
}

// releaseOutcome is where an accepted pass left the issue.
type releaseOutcome struct {
	// Released is false when the pass was ignored: already released once.
	Released bool
	// Status the issue holds afterwards: done, or the status it already had
	// when nothing could be written.
	Status string
	// Note is the sentence left on the issue.
	Note string
}

// releaseOnAcceptance is the once-per-stay half of maybeReleaseOnAcceptance:
// the caller has already checked the author is the reviewer and the body
// carries the pass line.
func (h *Handler) releaseOnAcceptance(ctx context.Context, issue db.Issue) releaseOutcome {
	meta := parseIssueMetadata(issue.Metadata)
	if blockwait.MetaString(meta, blockwait.KeyReleased) == blockwait.ReleasedPass {
		return releaseOutcome{Status: issue.Status}
	}
	h.setIssueMetaString(ctx, issue, blockwait.KeyReleased, blockwait.ReleasedPass)
	out := h.finishAcceptedIssue(ctx, issue, "验收已经通过。")
	out.Released = true
	return out
}

func (h *Handler) authorIsReviewer(issue db.Issue, comment db.Comment) bool {
	if !issue.ReviewerType.Valid || !issue.ReviewerID.Valid || !comment.AuthorID.Valid {
		return false
	}
	return issue.ReviewerType.String == comment.AuthorType && issue.ReviewerID == comment.AuthorID
}

func (h *Handler) finishAcceptedIssue(ctx context.Context, issue db.Issue, reason string) releaseOutcome {
	updated, err := h.Queries.CompleteIssueFromReview(ctx, db.CompleteIssueFromReviewParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Statuses:    []string{issue.Status},
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("block wait: close after pass failed", "error", err, "issue_id", uuidToString(issue.ID))
		}
		return releaseOutcome{Status: issue.Status, Note: reason}
	}
	h.syncBlockWait(ctx, issue, updated)
	h.publishBlockStatus(issue, updated)
	h.postBlockComment(ctx, updated, reason)
	h.notifyParentOfChildDone(ctx, issue, updated)
	h.postSourceChatReceipt(ctx, issue, updated)
	h.notifyWaitersOfIssueDone(ctx, issue, updated)
	return releaseOutcome{Status: updated.Status, Note: reason}
}

func (h *Handler) blockAcceptedIssue(ctx context.Context, issue db.Issue, decision blockwait.Decision) releaseOutcome {
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ActorType: pgtype.Text{String: botsource.ActorFromContext(ctx).Type, Valid: botsource.ActorFromContext(ctx).Type != ""}, ActorID: botsource.ActorFromContext(ctx).ID,
		ViaBotID: botsource.FromContext(ctx).ID, ViaBotName: pgtype.Text{String: botsource.FromContext(ctx).Name, Valid: botsource.FromContext(ctx).ID.Valid},
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Status:      "blocked",
	})
	if err != nil {
		slog.Warn("block wait: block after pass failed", "error", err, "issue_id", uuidToString(issue.ID))
		return releaseOutcome{Status: issue.Status, Note: decision.Reason}
	}
	h.syncBlockWait(ctx, issue, updated)
	h.persistBlockRecord(ctx, updated, decision.Record)
	h.publishBlockStatus(issue, updated)
	h.postBlockComment(ctx, updated, decision.Reason)
	return releaseOutcome{Status: updated.Status, Note: decision.Reason}
}

// wakeExecutor posts note with the executor's mention and starts it. A
// ticket in review would otherwise wake its reviewer, who passed already and
// cannot fix the branch.
func (h *Handler) wakeExecutor(ctx context.Context, issue db.Issue, note string) {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid || (issue.AssigneeType.String != "agent" && issue.AssigneeType.String != "squad") {
		h.postBlockComment(ctx, issue, note)
		return
	}
	mention := h.buildParentAssigneeMention(ctx, issue)
	comment := h.postBlockComment(ctx, issue, mention+note)
	if comment.ID.Valid {
		h.dispatchWaitingOnAssigneeTrigger(ctx, issue, comment.ID)
	}
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
		TodoSince:   pgtype.Timestamptz{Time: time.Now().Add(-undrivenTodoHorizon), Valid: true},
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

// undrivenTodoHorizon bounds the undriven-todo patrol (DENE-1342) to tickets
// that moved in the last week, so a deploy does not rerun every forgotten one.
const undrivenTodoHorizon = 7 * 24 * time.Hour

func (h *Handler) patrolOne(ctx context.Context, issue db.Issue) bool {
	meta := parseIssueMetadata(issue.Metadata)
	watched := blockwait.MetaString(meta, blockwait.KeyWatched) == blockwait.WatchedYes
	if !watched && issue.Status != "todo" {
		return false
	}
	var self blockwait.Driver
	if issue.Status == "todo" {
		driver, ok := h.issueDriver(ctx, issue)
		if !ok {
			return false
		}
		self = driver
	}
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
	round, hasRound := parseMetaTime(blockwait.MetaString(meta, blockwait.KeyReviewRound))
	decision := blockwait.DecidePatrol(blockwait.PatrolInput{
		Status:         issue.Status,
		Quiet:          quiet,
		Record:         rec,
		Blockers:       blockers,
		Now:            time.Now(),
		LastPatrol:     last,
		HasLastPatrol:  hasLast,
		HasPassComment: h.passInThisRound(ctx, issue, round, hasRound),
		ReleasedPass:   hasRound && blockwait.MetaString(meta, blockwait.KeyReleased) == blockwait.ReleasedPass,
		SegmentNudged:  blockwait.MetaString(meta, blockwait.KeySegmentNudged) == "1",
		ReviewNudged:   blockwait.MetaString(meta, blockwait.KeyReviewNudged) == "1",
		ReviewerHuman:  issue.ReviewerType.Valid && issue.ReviewerType.String == "member",
		ReviewerEmpty:  reviewerSlotEmpty(issue),
		Watched:        watched,
		Undriven:       self.Kind == blockwait.DriverNone,
		UndrivenWhy:    self.Reason,
		UndrivenState:  blockwait.ParseUndriven(meta),
	})
	switch decision.Action {
	case blockwait.ActionRelease, blockwait.ActionWake, blockwait.ActionRevive, blockwait.ActionEscalate:
	default:
		return false
	}
	h.applyPatrolFollowUp(ctx, issue, meta, decision)
	switch decision.Action {
	case blockwait.ActionRevive, blockwait.ActionEscalate:
		h.reviveOrEscalate(ctx, issue, decision)
	case blockwait.ActionRelease:
		h.finishAcceptedIssue(ctx, issue, decision.Reason)
	case blockwait.ActionWake:
		h.wakeIssueOwner(ctx, issue, decision.Reason, decision.CommentOnly)
	}
	return true
}

// reviewerSlotEmpty reports an acceptance slot nobody has answered. "none" is
// an answer ("this issue needs no acceptance pass"), so it is not empty.
func reviewerSlotEmpty(issue db.Issue) bool {
	if !issue.ReviewerType.Valid || strings.TrimSpace(issue.ReviewerType.String) == "" {
		return true
	}
	if issue.ReviewerType.String == "none" {
		return false
	}
	return !issue.ReviewerID.Valid
}

// summonPatrol is the patrol calling a person through the summon entry
// (DENE-880). commentID is a block comment already carrying the @; unset
// posts the entry's own. An unanswered call to the same person dedupes.
func (h *Handler) summonPatrol(ctx context.Context, issue db.Issue, recipient pgtype.UUID, reason string, commentID pgtype.UUID, noComment bool) {
	if !recipient.Valid {
		return
	}
	_, _ = h.summonPerson(ctx, service.SummonInput{
		Issue:      issue,
		Recipient:  recipient,
		CallerType: "system",
		Source:     service.SummonSourcePatrol,
		Reason:     reason,
		CommentID:  commentID,
		NoComment:  noComment,
	})
}

func parseMetaTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func (h *Handler) applyPatrolFollowUp(ctx context.Context, issue db.Issue, meta map[string]any, decision blockwait.Decision) {
	set, drop := decision.FollowUp(
		time.Now(),
		blockwait.MetaString(meta, blockwait.KeyBlockedBy),
		blockwait.MetaString(meta, "close.waiting_on"),
		blockwait.MetaString(meta, blockwait.KeyWokenBy),
	)
	for _, key := range drop {
		h.deleteIssueMeta(ctx, issue, key)
	}
	for key, value := range set {
		h.setIssueMetaString(ctx, issue, key, value)
	}
}

func (h *Handler) passInThisRound(ctx context.Context, issue db.Issue, round time.Time, hasRound bool) bool {
	if !hasRound || !issue.ReviewerID.Valid {
		return false
	}
	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Limit: 30,
	})
	if err != nil {
		return false
	}
	for _, c := range comments {
		if c.AuthorID != issue.ReviewerID || !c.CreatedAt.Valid {
			continue
		}
		if issue.ReviewerType.Valid && c.AuthorType != issue.ReviewerType.String {
			continue
		}
		if blockwait.PassInRound(c.Content, c.CreatedAt.Time, round) {
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
			h.undrivenView(ctx, &view, other)
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
// handoff belongs to the disabled-seat path. A ticket nobody holds is seated
// by routing first; when routing cannot, a person is told exactly that.
func (h *Handler) wakeIssueOwner(ctx context.Context, issue db.Issue, reason string, commentOnly bool) {
	if !commentOnly && issue.Status != "in_review" && !issue.AssigneeID.Valid &&
		blockwait.MetaString(parseIssueMetadata(issue.Metadata), blockwait.KeyNeedsHuman) == "" {
		seated, why := h.seatBeforeWake(ctx, issue)
		if !seated.AssigneeID.Valid {
			h.reportUnseatedWake(ctx, issue, reason, why)
			return
		}
		issue = seated
	}
	targetType, targetID := issue.AssigneeType, issue.AssigneeID
	if issue.Status == "in_review" && issue.ReviewerType.Valid && issue.ReviewerID.Valid && issue.ReviewerType.String != "none" {
		targetType, targetID = issue.ReviewerType, issue.ReviewerID
	}
	if issue.Status == "in_review" && reviewerSlotEmpty(issue) {
		// The executor already said it is done; asking it to review its own
		// work only produces "请验收". Leave the sentence, start nobody.
		commentOnly = true
	}
	if blockwait.MetaString(parseIssueMetadata(issue.Metadata), blockwait.KeyNeedsHuman) != "" {
		commentOnly = true
	}
	if !commentOnly && targetType.Valid && targetID.Valid && targetType.String == "agent" {
		var covered bool
		issue, targetID, reason, covered = h.coverDisabledWakeTarget(ctx, issue, targetID, reason)
		if !covered {
			commentOnly = true
		}
	}
	mention := ""
	if targetType.Valid && targetID.Valid && (targetType.String == "agent" || targetType.String == "squad") && !commentOnly {
		mention = h.buildParentAssigneeMention(ctx, db.Issue{AssigneeType: targetType, AssigneeID: targetID, WorkspaceID: issue.WorkspaceID})
	}
	if targetType.Valid && targetID.Valid && targetType.String == "member" {
		commentOnly = true
		mention = h.memberWakeMention(ctx, targetID)
	}
	comment := h.postBlockComment(ctx, issue, mention+reason)
	if targetType.Valid && targetID.Valid && targetType.String == "member" {
		h.summonPatrol(ctx, issue, targetID, reason, comment.ID, !comment.ID.Valid)
	} else if human := blockwait.MetaString(parseIssueMetadata(issue.Metadata), blockwait.KeyNeedsHuman); human != "" {
		h.summonPatrol(ctx, issue, parseUUID(human), reason, pgtype.UUID{}, false)
	}
	if commentOnly || !targetType.Valid || !targetID.Valid || !comment.ID.Valid {
		return
	}
	waker := issue
	waker.AssigneeType = targetType
	waker.AssigneeID = targetID
	h.dispatchWaitingOnAssigneeTrigger(ctx, waker, comment.ID)
}

// seatBeforeWake re-reads a ticket whose wait just ended, so a slot a person
// filled meanwhile counts. why says why the slot is still empty.
func (h *Handler) seatBeforeWake(ctx context.Context, issue db.Issue) (db.Issue, string) {
	why := "执行人栏是空的"
	fresh, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return issue, why
	}
	return fresh, why
}

// reportUnseatedWake is the wake with nobody to start: the comment says
// what ended, that the ticket has no executor, why routing left it empty,
// and the one action that resumes it; the summon puts that same sentence on
// a person's inbox card.
func (h *Handler) reportUnseatedWake(ctx context.Context, issue db.Issue, reason, why string) {
	recipient := h.unseatedRecipient(ctx, issue)
	mention := ""
	if recipient.Valid {
		mention = h.memberWakeMention(ctx, recipient)
	}
	body := strings.TrimSpace(reason) + " 但这张票没有执行人，" + why + "，所以没有叫醒任何人。" + unseatedReason + "选好后平台会接着叫醒它。"
	comment := h.postBlockComment(ctx, issue, mention+body)
	if !recipient.Valid {
		return
	}
	_, _ = h.summonPerson(ctx, service.SummonInput{
		Issue:      issue,
		Recipient:  recipient,
		CallerType: "system",
		Source:     service.SummonSourceUnseated,
		Reason:     unseatedReason,
		CommentID:  comment.ID,
		NoComment:  !comment.ID.Valid,
	})
}

// unseatedRecipient is who decides who holds a ticket nobody holds: the
// person who created it, else a workspace manager — routing's notify rule.
func (h *Handler) unseatedRecipient(ctx context.Context, issue db.Issue) pgtype.UUID {
	if issue.CreatorType == "member" && issue.CreatorID.Valid {
		return issue.CreatorID
	}
	managers, err := h.Queries.ListWorkspaceManagerUserIDs(ctx, issue.WorkspaceID)
	if err != nil {
		slog.Warn("block wait: list managers failed", "error", err, "issue_id", uuidToString(issue.ID))
	}
	for _, m := range managers {
		if m.Valid {
			return m
		}
	}
	return pgtype.UUID{}
}

// coverDisabledWakeTarget keeps the patrol from waking a seat that is not
// taking work (DENE-870): the wake becomes a plain comment and nobody is
// dispatched. covered is false when the wake must not dispatch.
func (h *Handler) coverDisabledWakeTarget(ctx context.Context, issue db.Issue, targetID pgtype.UUID, reason string) (db.Issue, pgtype.UUID, string, bool) {
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: targetID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || agent.ArchivedAt.Valid || agent.WorkEnabled {
		return issue, targetID, reason, true
	}
	isAssignee := issue.AssigneeType.Valid && issue.AssigneeType.String == "agent" && issue.AssigneeID == targetID
	if !isAssignee {
		return issue, targetID, reason + fmt.Sprintf(" 验收人 %s 已停用，平台没有叫醒它，请重新启用或换一位验收人。", agent.Name), false
	}
	return issue, targetID, reason + fmt.Sprintf(" 执行人 %s 已停用，平台没有叫醒它。重新启用或改派后再继续。", agent.Name), false
}

func (h *Handler) memberWakeMention(ctx context.Context, userID pgtype.UUID) string {
	user, err := h.Queries.GetUser(ctx, userID)
	if err != nil {
		return ""
	}
	name := sanitizeMentionLabel(user.Name)
	if name == "" {
		name = "member"
	}
	return fmt.Sprintf("[@%s](mention://member/%s) ", name, uuidToString(userID))
}

// blockAttributionRejection is the 400 an agent gets for moving a ticket to
// blocked without saying what kind of stop it is and what happens next. The
// sub-issue blocker card reads exactly these two fields; without them the row
// is blank and never counts as "needs you" (DENE-1301). A member dragging a
// card is not asked for them.
func blockAttributionRejection(req UpdateIssueRequest, actorType string) string {
	if actorType != "agent" {
		return ""
	}
	kind := strings.TrimSpace(deref(req.BlockKind))
	action := strings.TrimSpace(deref(req.BlockAction))
	const hint = "智能体把票改成 blocked 要写明卡点类型和下一步：--block-kind（decision / permission / external / dependency / capacity）配 --block-action（一句话，80 字以内）。"
	if kind == "" || action == "" {
		return hint
	}
	if !closeprotocol.AllowedBlockKind(kind) {
		return fmt.Sprintf("--block-kind %q 不是允许的值。", kind) + hint
	}
	if len([]rune(action)) > 80 {
		return "--block-action 超过 80 字。" + hint
	}
	return ""
}

// persistBlockAttribution writes the kind and next step a status move into
// blocked carried, so the blocker card has the same two fields a blocked
// close writes.
func (h *Handler) persistBlockAttribution(ctx context.Context, issue db.Issue, req UpdateIssueRequest) {
	kind := strings.TrimSpace(deref(req.BlockKind))
	action := strings.TrimSpace(deref(req.BlockAction))
	if kind == "" || action == "" || !closeprotocol.AllowedBlockKind(kind) {
		return
	}
	h.setIssueMetaString(ctx, issue, closeprotocol.KeyBlockKind, kind)
	h.setIssueMetaString(ctx, issue, closeprotocol.KeyBlockAction, truncateRunes(action, 80))
}

// executorReached reports whether a comment's trigger results include a run
// for the issue's own executor — its agent, or a leader run under its squad.
// A run handed to someone else the comment happened to @ does not count.
func executorReached(issue db.Issue, enqueued map[string]commentEnqueueResult) bool {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return false
	}
	assignee := uuidToString(issue.AssigneeID)
	for agentID, res := range enqueued {
		switch res.status {
		case DispatchQueued, DispatchCoalesced, DispatchSteered:
		default:
			continue
		}
		switch issue.AssigneeType.String {
		case "agent":
			if agentID == assignee {
				return true
			}
		case "squad":
			if res.execSquadID == assignee {
				return true
			}
		}
	}
	return false
}

// resumeBlockedOnReply moves a blocked ticket back to in_progress when a
// member's reply has just woken its executor (DENE-1301). Before this the
// ticket kept saying "blocked, needs you" until the executor closed again,
// although the person had already answered and the work was moving. The wait
// keys are dropped as on any move out of blocked, and the blocked close record
// is marked superseded rather than replaced: nobody closed anything.
func (h *Handler) resumeBlockedOnReply(ctx context.Context, issue db.Issue, enqueued map[string]commentEnqueueResult, summonWoke bool) {
	if issue.Status != issuestatus.Blocked || h.TxStarter == nil || !(summonWoke || executorReached(issue, enqueued)) {
		return
	}
	var prev, updated db.Issue
	err := func() error {
		tx, err := h.TxStarter.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		qtx := h.Queries.WithTx(tx)
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, issue.ID, issue.WorkspaceID).Scan(&status); err != nil {
			return err
		}
		if status != issuestatus.Blocked {
			return pgx.ErrNoRows
		}
		prev, err = qtx.GetIssue(ctx, issue.ID)
		if err != nil {
			return err
		}
		updated, err = qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
			ActorType: pgtype.Text{String: botsource.ActorFromContext(ctx).Type, Valid: botsource.ActorFromContext(ctx).Type != ""}, ActorID: botsource.ActorFromContext(ctx).ID,
			ViaBotID: botsource.FromContext(ctx).ID, ViaBotName: pgtype.Text{String: botsource.FromContext(ctx).Name, Valid: botsource.FromContext(ctx).ID.Valid}, ID: issue.ID, WorkspaceID: issue.WorkspaceID, Status: issuestatus.InProgress})
		if err != nil {
			return err
		}
		// §6.1: the blocker fields belong to a blocked ticket only.
		drop := append(blockwait.WaitKeys(), blockwait.KeyWatched, closeprotocol.KeyBlockKind, closeprotocol.KeyBlockAction)
		for _, key := range drop {
			if _, err := qtx.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: key}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if at := blockwait.MetaString(parseIssueMetadata(prev.Metadata), closeprotocol.KeyAt); at != "" {
			if err := setIssueMetaStringTx(ctx, qtx, updated, closeprotocol.KeySuperseded, at); err != nil {
				return err
			}
		}
		if updated, err = qtx.GetIssue(ctx, issue.ID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}()
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("block wait: resume on reply failed", "error", err, "issue_id", uuidToString(issue.ID))
		}
		return
	}
	h.publishBlockStatus(prev, updated)
}

func setIssueMetaStringTx(ctx context.Context, q *db.Queries, issue db.Issue, key, value string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = q.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         key,
		Value:       raw,
	})
	// No row means the key already holds this value (the query skips no-op
	// writes), so a repeated write must not fail as "issue not found".
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}
