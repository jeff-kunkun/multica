package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/stagegate"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// stagePlan is what closing a stage should do besides the existing comment.
type stagePlan struct {
	note      string
	skipWake  bool
	parentOff bool
	relay     *db.Agent
	// needsCall: the next stage has work the platform would not start on
	// its own (held, or blocked by a cancelled sibling).
	needsCall bool
}

// disabledParentSeat reports the parent seat that would be woken and cannot
// take work. A squad parent is its leader. Archived seats are not this case.
func (h *Handler) disabledParentSeat(ctx context.Context, parent db.Issue) (db.Agent, bool) {
	if !parent.AssigneeType.Valid || !parent.AssigneeID.Valid {
		return db.Agent{}, false
	}
	var agentID pgtype.UUID
	switch parent.AssigneeType.String {
	case "agent":
		agentID = parent.AssigneeID
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID: parent.AssigneeID, WorkspaceID: parent.WorkspaceID,
		})
		if err != nil {
			return db.Agent{}, false
		}
		agentID = squad.LeaderID
	default:
		return db.Agent{}, false
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: agentID, WorkspaceID: parent.WorkspaceID,
	})
	if err != nil || agent.ArchivedAt.Valid || agent.WorkEnabled {
		return db.Agent{}, false
	}
	return agent, true
}

// planStageAdvance promotes next-stage backlog items whose descriptions state
// no extra dependency, and notes a parent seat that cannot receive the wake.
func (h *Handler) planStageAdvance(ctx context.Context, parent db.Issue, children []db.Issue, nextStage int32, statuses resolvedChildStatuses, blockedByCancel bool) stagePlan {
	off, parentOff := h.disabledParentSeat(ctx, parent)
	var plan stagePlan
	plan.parentOff = parentOff
	plan.needsCall = nextStage > 0 && blockedByCancel
	if nextStage > 0 && !blockedByCancel {
		promoted, started, held := h.promoteClearNextStage(ctx, parent, children, nextStage, statuses)
		plan.note = stagePromotionNote(promoted, started, held)
		plan.needsCall = len(held) > 0
		if len(promoted) > 0 && len(held) == 0 {
			plan.skipWake = true
			if parentOff {
				plan.note += fmt.Sprintf(" 父票执行人 %s 已停用，这一阶段没有要人拍板的依赖，所以没有再叫人。", off.Name)
			}
			return plan
		}
	}
	if !parentOff {
		return plan
	}
	plan.skipWake = true
	plan.note += fmt.Sprintf(" 父票执行人 %s 已停用，阶段没有人推进。", off.Name)
	return plan
}

func (h *Handler) promoteClearNextStage(ctx context.Context, parent db.Issue, children []db.Issue, nextStage int32, statuses resolvedChildStatuses) (promoted []string, started int, held []stagegate.Hold) {
	prefix := h.getIssuePrefix(ctx, parent.WorkspaceID)
	items := make([]stagegate.Item, 0, len(children))
	byID := make(map[string]db.Issue, len(children))
	for _, child := range children {
		id := uuidToString(child.ID)
		byID[id] = child
		desc := ""
		if child.Description.Valid {
			desc = child.Description.String
		}
		items = append(items, stagegate.Item{
			ID:          id,
			Title:       child.Title,
			Description: desc,
			Identifier:  prefix + "-" + fmt.Sprint(child.Number),
			Stage:       child.Stage.Int32,
			HasStage:    child.Stage.Valid,
			Status:      statuses.status(child),
		})
	}
	ready, held := stagegate.Classify(nextStage, items)
	for _, item := range ready {
		child := byID[item.ID]
		label := item.Identifier
		if label == "" {
			label = item.Title
		}
		_, ran, err := h.promoteChildToTodo(ctx, child, "system", "")
		if errors.Is(err, pgx.ErrNoRows) {
			// Another close already moved this child out of backlog.
			continue
		}
		if err != nil {
			slog.Warn("stage advance: promote failed", "error", err, "child_id", item.ID)
			held = append(held, stagegate.Hold{ID: item.ID, Identifier: item.Identifier, Title: item.Title, Reason: "提到待办失败"})
			continue
		}
		if ran {
			started++
			promoted = append(promoted, label)
			continue
		}
		promoted = append(promoted, label+"（还没开跑）")
	}
	return promoted, started, held
}

func stagePromotionNote(promoted []string, started int, held []stagegate.Hold) string {
	if len(promoted) == 0 && len(held) == 0 {
		return ""
	}
	var b strings.Builder
	if len(promoted) > 0 {
		b.WriteString(" 平台已把下一阶段里没有额外依赖的子票提到待办：")
		b.WriteString(strings.Join(promoted, "、"))
		b.WriteString("。")
		if started > 0 {
			b.WriteString("能接活的执行席已经开跑。")
		}
	}
	if len(held) > 0 {
		b.WriteString(" 这些还留在待办，因为依赖没说清或和阶段安排冲突：")
		parts := make([]string, 0, len(held))
		for _, item := range held {
			label := item.Identifier
			if label == "" {
				label = item.Title
			}
			parts = append(parts, label+"（"+item.Reason+"）")
		}
		b.WriteString(strings.Join(parts, "、"))
		b.WriteString("。")
	}
	return b.String()
}

func (h *Handler) wakeStageRelay(ctx context.Context, parent db.Issue, commentID pgtype.UUID, relay *db.Agent) {
	if relay == nil {
		return
	}
	if _, err := h.TaskService.EnqueueTaskForMention(ctx, parent, relay.ID, commentID, service.OriginDerived); err != nil {
		slog.Warn("stage advance: relay wake failed",
			"error", err,
			"parent_id", uuidToString(parent.ID),
			"agent_id", uuidToString(relay.ID))
	}
}
