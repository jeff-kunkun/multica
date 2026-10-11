package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// seatStagedParent gives an agent-created parent nobody holds a coordinator
// once it gains a staged sub-issue (DENE-1777). Agents leave the executor
// empty for routing, but routing only seats a root that coordinates
// (in_progress) — a parent left in backlog or todo was never seated, and a
// stage close on it woke nobody. The root moves to in_progress like a plan
// root; with routing on, routing picks the coordinator, otherwise the agent
// that created the sub-issue holds it. No run starts: the stage barrier wakes it.
func (h *Handler) seatStagedParent(r *http.Request, workspaceID string, child db.Issue, creatorType, creatorID string) {
	if creatorType != "agent" || !child.ParentIssueID.Valid || !child.Stage.Valid {
		return
	}
	ctx := r.Context()
	parent, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: child.ParentIssueID, WorkspaceID: child.WorkspaceID})
	if err != nil || parent.ParentIssueID.Valid || parent.AssigneeID.Valid || parent.CreatorType != "agent" {
		return
	}
	routed := h.Routing != nil && h.Routing.Active(ctx, workspaceID)
	params := db.SeatStagedParentParams{ID: parent.ID, WorkspaceID: parent.WorkspaceID}
	if !routed {
		agentID, err := util.ParseUUID(creatorID)
		if err != nil {
			return
		}
		params.AssigneeType = pgtype.Text{String: "agent", Valid: true}
		params.AssigneeID = agentID
	}
	updated, err := h.Queries.SeatStagedParent(ctx, params)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Already coordinating, or somebody seated it meanwhile.
	case err != nil:
		slog.Warn("staged parent: seat failed", "error", err, "parent_id", uuidToString(parent.ID))
		return
	default:
		h.publishWithSource(r.Context(), protocol.EventIssueUpdated, workspaceID, "system", "", RoutingIssueUpdatedPayload(parent, updated))
	}
	if routed {
		h.RouteGroupNodeAsync(r, workspaceID, uuidToString(parent.ID))
	}
}

// wakeUnseatedParent is a closed stage on a parent nobody holds whose next
// stage has work the platform would not start itself (DENE-1777). What the
// stage advance could promote is already promoted; routing gets one chance to
// seat a coordinator for the rest, who is woken like any parent seat.
// Otherwise a person is called to pick one instead of the chain stopping
// silently.
func (h *Handler) wakeUnseatedParent(ctx context.Context, parent db.Issue, comment db.Comment, nextStage int32) {
	fresh, _ := h.seatBeforeWake(ctx, parent)
	if fresh.AssigneeID.Valid {
		h.dispatchParentAssigneeTrigger(ctx, fresh, comment)
		return
	}
	// The stage-close comment already says what is held; the call points at
	// it instead of posting a second comment.
	recipient := h.unseatedRecipient(ctx, parent)
	if !recipient.Valid {
		return
	}
	_, _ = h.summonPerson(ctx, service.SummonInput{
		Issue:      parent,
		Recipient:  recipient,
		CallerType: "system",
		Source:     service.SummonSourceUnseated,
		Reason:     fmt.Sprintf("第 %d 阶段还有子任务要人看过才能开工，总单没有执行人。", nextStage) + unseatedReason,
		CommentID:  comment.ID,
	})
}
