package handler

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Meetings (kun-main office). A meeting is an issue opened from the meeting
// room so one member drafts a plan on it; any issue that already carries a
// plan shows up in the room too.

type MeetingResponse struct {
	Issue IssueResponse `json:"issue"`
	// Plan is the newest plan version, whatever its status; null before the
	// first propose.
	Plan *PlanResponse `json:"plan"`
	// StepsTotal / StepsDone count the newest APPROVED plan's steps and how
	// many of their child issues are done or cancelled; 0/0 until approval.
	StepsTotal int32 `json:"steps_total"`
	StepsDone  int32 `json:"steps_done"`
}

// ListMeetings returns the workspace's meetings, newest activity first.
func (h *Handler) ListMeetings(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	wsUUID := member.WorkspaceID
	rows, err := h.Queries.ListMeetings(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list meetings")
		return
	}

	planIDs := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		if row.LatestPlanID.Valid {
			planIDs = append(planIDs, row.LatestPlanID)
		}
	}
	plans := map[string]db.Plan{}
	if len(planIDs) > 0 {
		list, err := h.Queries.ListPlansByIDs(r.Context(), db.ListPlansByIDsParams{WorkspaceID: wsUUID, Ids: planIDs})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list meetings")
			return
		}
		for _, p := range list {
			plans[uuidToString(p.ID)] = p
		}
	}

	prefix := h.getIssuePrefix(r.Context(), wsUUID)
	fill := h.newStatusCategoryFiller(r.Context(), wsUUID)
	out := make([]MeetingResponse, 0, len(rows))
	for _, row := range rows {
		issue := issueToResponse(row.Issue, prefix)
		fill(&issue)
		m := MeetingResponse{Issue: issue, StepsTotal: row.StepsTotal, StepsDone: row.StepsDone}
		if p, ok := plans[uuidToString(row.LatestPlanID)]; ok && row.LatestPlanID.Valid {
			resp := planToResponse(p)
			m.Plan = &resp
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"meetings": out})
}
