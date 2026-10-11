package handler

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// CommentWake answers "who did this comment wake?" on a create response
// (DENE-1774). Woken lists every run the comment queued, folded into, deferred
// to, or steered — whatever caused it (assignee, @agent, @squad, thread or
// conversation continuation), so an implicit assignee trigger is visible too,
// not only the explicit mentions trigger_outcomes covers. NoneWoken is set
// only when nothing was woken AND the comment is left unattended: the issue is
// still open and no run on it is active. A comment is not a dispatch; the
// author has to hand off or @ someone to get work done.
type CommentWake struct {
	Woken     []CommentWokenRun `json:"woken"`
	NoneWoken bool              `json:"none_woken"`
}

type CommentWokenRun struct {
	AgentID   string         `json:"agent_id"`
	AgentName string         `json:"agent_name"`
	Source    string         `json:"source"`
	Status    DispatchStatus `json:"status"`
}

// commentWokenRuns pairs each resolved trigger with its handled dispatch
// result. Blocked results woke no one and are reported by trigger_outcomes.
func commentWokenRuns(triggers []commentAgentTrigger, results map[string]commentEnqueueResult) []CommentWokenRun {
	woken := []CommentWokenRun{}
	for _, trigger := range triggers {
		res, ok := results[uuidToString(trigger.Agent.ID)]
		if !ok {
			continue
		}
		switch res.status {
		case DispatchQueued, DispatchCoalesced, DispatchDeferred, DispatchSteered:
		default:
			continue
		}
		woken = append(woken, CommentWokenRun{
			AgentID:   uuidToString(trigger.Agent.ID),
			AgentName: trigger.Agent.Name,
			Source:    string(trigger.Source),
			Status:    res.status,
		})
	}
	return woken
}

// commentWokenSourceSummonAnswer marks the run a member's reply started by
// answering a call (needs_human / summon) that was waiting on it.
const commentWokenSourceSummonAnswer = "summon_answer"

// summonAnswerWokenRun reports the run answerSummons queued for agentID.
func (h *Handler) summonAnswerWokenRun(ctx context.Context, agentID pgtype.UUID) CommentWokenRun {
	run := CommentWokenRun{
		AgentID: uuidToString(agentID),
		Source:  commentWokenSourceSummonAnswer,
		Status:  DispatchQueued,
	}
	if agent, err := h.Queries.GetAgent(ctx, agentID); err == nil {
		run.AgentName = agent.Name
	}
	return run
}

// commentWake decides NoneWoken from the woken runs and the issue's state
// after the trigger ran. A /note opted out of triggering on purpose, a closed
// issue needs no one, and an active run will pick the comment up on its own.
func (h *Handler) commentWake(ctx context.Context, issue db.Issue, comment db.Comment, woken []CommentWokenRun) *CommentWake {
	wake := &CommentWake{Woken: woken}
	if woken == nil {
		wake.Woken = []CommentWokenRun{}
	}
	if len(wake.Woken) > 0 || isNoteComment(comment.Content) || issueWakeClosed(issue.Status) {
		return wake
	}
	active, err := h.Queries.HasActiveTaskForIssue(ctx, issue.ID)
	if err != nil {
		// Unknown is not "nobody": never claim an unattended comment we could
		// not confirm.
		slog.Warn("comment wake: active task check failed", "issue_id", uuidToString(issue.ID), "error", err)
		return wake
	}
	wake.NoneWoken = !active
	return wake
}

func issueWakeClosed(status string) bool {
	return status == "done" || status == "cancelled"
}
