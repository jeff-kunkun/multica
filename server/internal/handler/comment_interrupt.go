package handler

import (
	"context"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// sendModeInterrupt stops the current step of every woken agent's running
// turn, a running command included, and continues the same session with the
// comment (DENE-2667). A run that cannot stop its step reads the comment at
// its next step instead, so an interrupt is never lost to a run's capability.
// Unlike steer, an interrupt may carry files and may come from an agent.
const sendModeInterrupt = "interrupt"

// interruptDefaultAuthorsEnv lists member or agent ids whose comments without
// an explicit mode interrupt the running turns they wake. Unset keeps every
// comment's existing default.
const interruptDefaultAuthorsEnv = "MULTICA_INTERRUPT_DEFAULT_AUTHORS"

func interruptByDefault(authorID string) bool {
	if authorID == "" {
		return false
	}
	for _, id := range strings.Split(os.Getenv(interruptDefaultAuthorsEnv), ",") {
		if strings.EqualFold(strings.TrimSpace(id), authorID) {
			return true
		}
	}
	return false
}

type commentInterruptKey struct{}

// withCommentInterrupt marks the running turns the comment interrupts; the
// shared trigger path binds them like steered turns, as interrupts.
func withCommentInterrupt(ctx context.Context, taskIDs []pgtype.UUID) context.Context {
	if len(taskIDs) == 0 {
		return ctx
	}
	return context.WithValue(ctx, commentInterruptKey{}, taskIDs)
}

func commentInterruptTaskIDs(ctx context.Context) []pgtype.UUID {
	ids, _ := ctx.Value(commentInterruptKey{}).([]pgtype.UUID)
	return ids
}

func commentInterrupts(ctx context.Context, taskID pgtype.UUID) bool {
	for _, id := range commentInterruptTaskIDs(ctx) {
		if id == taskID {
			return true
		}
	}
	return false
}

// interruptTargets picks the running turns an interrupt comment goes into:
// one per agent the comment wakes, never the author's own. anyRunning reports
// whether a woken agent was running at all; unsupported names the provider
// of a running turn that cannot take a message mid-run.
func (h *Handler) interruptTargets(ctx context.Context, issue db.Issue, triggers []commentAgentTrigger, authorType, authorID string) (ids []pgtype.UUID, anyRunning bool, unsupported string, err error) {
	running, err := h.Queries.ListIssueSteerTargets(ctx, issue.ID)
	if err != nil {
		return nil, false, "", err
	}
	woken := make(map[string]bool, len(triggers))
	for _, trigger := range triggers {
		woken[uuidToString(trigger.Agent.ID)] = true
	}
	seen := map[string]bool{}
	for _, task := range running {
		agentID := uuidToString(task.AgentID)
		if !woken[agentID] || seen[agentID] || (authorType == "agent" && agentID == authorID) {
			continue
		}
		seen[agentID] = true
		anyRunning = true
		if task.Capability != protocol.DaemonCapabilityTaskSupplementV1 {
			unsupported = task.Provider
			continue
		}
		ids = append(ids, task.ID)
	}
	return ids, anyRunning, unsupported, nil
}
