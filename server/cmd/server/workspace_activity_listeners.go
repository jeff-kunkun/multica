package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerWorkspaceActivityListeners feeds the all-scope stream (DENE-1936) with
// what happens without anyone's HTTP write: PR mirrors updated by a webhook, a
// run finishing or failing, an autopilot run ending. Writes made through the
// API reach the same stream through the BotWriteAudit middleware, so they are
// deliberately not subscribed to here a second time. A failure is logged and
// dropped; the stream never breaks the thing that produced the event.
func registerWorkspaceActivityListeners(bus *events.Bus, queries *db.Queries) {
	ctx := context.Background()
	visible := handler.NewTargetVisibility(queries)
	route := func(in service.WorkspaceActivityInput) {
		in.Visible = visible
		if _, err := service.RouteWorkspaceActivity(ctx, queries, in); err != nil {
			slog.Warn("workspace activity: route failed", "error", err, "type", in.ActivityType)
		}
	}

	for _, kind := range []struct{ event, activity, verb string }{
		{protocol.EventPullRequestUpdated, service.ActivityPRUpdated, "updated"},
		{protocol.EventPullRequestLinked, service.ActivityPRLinked, "linked"},
		{protocol.EventPullRequestUnlinked, service.ActivityPRUnlinked, "unlinked"},
	} {
		kind := kind
		bus.Subscribe(kind.event, func(e events.Event) {
			// Only a PR linked to tickets is told, and only to people who can
			// open every one of them (and its repository).
			if in, ok := service.PullRequestActivity(parseUUID(e.WorkspaceID), kind.activity, kind.verb, e.Payload); ok {
				route(in)
			}
		})
	}

	for _, kind := range []struct{ event, activity, importance, category string }{
		{protocol.EventTaskCompleted, service.ActivityRunCompleted, service.ImportanceNormal, service.CategoryResult},
		{protocol.EventTaskFailed, service.ActivityRunFailed, service.ImportanceHigh, service.CategoryFailure},
		{protocol.EventTaskCancelled, service.ActivityRunCancelled, service.ImportanceLow, service.CategoryInfo},
	} {
		kind := kind
		bus.Subscribe(kind.event, func(e events.Event) {
			p, ok := e.Payload.(map[string]any)
			if !ok {
				return
			}
			if retry, _ := p["retry_pending"].(bool); retry {
				return // an intermediate attempt, not the run's outcome
			}
			if chat, _ := p["chat_session_id"].(string); chat != "" {
				return // a chat's run is the chat opener's private thread
			}
			taskID, _ := p["task_id"].(string)
			if taskID == "" {
				return
			}
			attrs := map[string]string{"task_id": taskID}
			for _, k := range []string{"agent_id", "issue_id", "status", "failure_reason"} {
				if v, _ := p[k].(string); v != "" {
					attrs[k] = v
				}
			}
			route(service.WorkspaceActivityInput{
				WorkspaceID:  parseUUID(e.WorkspaceID),
				IssueID:      optionalUUID(attrs["issue_id"]),
				ActivityType: kind.activity,
				DedupeKey:    "ws:run:" + taskID + ":" + kind.activity,
				Body: service.WorkspaceEventBody{
					Family: service.FamilyRun, Action: strings.TrimPrefix(kind.activity, "run."), ActorKind: "system", EntityID: taskID, Attrs: attrs,
				},
				ActorType:  "system",
				Importance: kind.importance,
				Categories: []string{kind.category},
				OccurredAt: time.Now(),
			})
		})
	}

	bus.Subscribe(protocol.EventAutopilotRunDone, func(e events.Event) {
		p, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		runID, _ := p["run_id"].(string)
		if runID == "" {
			return
		}
		attrs := map[string]string{"run_id": runID}
		for _, k := range []string{"autopilot_id", "status"} {
			if v, _ := p[k].(string); v != "" {
				attrs[k] = v
			}
		}
		importance, category := service.ImportanceNormal, service.CategoryResult
		if attrs["status"] == "failed" {
			importance, category = service.ImportanceHigh, service.CategoryFailure
		} else if attrs["status"] == "skipped" {
			importance, category = service.ImportanceLow, service.CategoryInfo
		}
		route(service.WorkspaceActivityInput{
			WorkspaceID:  parseUUID(e.WorkspaceID),
			ActivityType: service.ActivityAutopilotRun,
			DedupeKey:    "ws:autopilot-run:" + runID,
			Body: service.WorkspaceEventBody{
				Family: service.FamilyAutopilot, Action: "run_done", ActorKind: "system", EntityID: runID, Attrs: attrs,
			},
			ActorType:  "system",
			Importance: importance,
			Categories: []string{category},
			OccurredAt: time.Now(),
		})
	})
}
