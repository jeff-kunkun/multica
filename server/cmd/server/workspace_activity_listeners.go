package main

import (
	"context"
	"encoding/json"
	"fmt"
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
			pr := prFromPayload(e.Payload)
			if pr["id"] == "" {
				return
			}
			attrs := map[string]string{}
			for _, k := range []string{"number", "state", "title", "html_url", "repo", "merged_at"} {
				if pr[k] != "" {
					attrs[k] = pr[k]
				}
			}
			route(service.WorkspaceActivityInput{
				WorkspaceID:  parseUUID(e.WorkspaceID),
				ActivityType: kind.activity,
				DedupeKey:    fmt.Sprintf("ws:pr:%s:%s:%s:%s:%s", pr["id"], kind.verb, pr["state"], pr["merged_at"], pr["pr_updated_at"]),
				Body: service.WorkspaceEventBody{
					Family: service.FamilyPR, Action: kind.verb, ActorKind: "system", EntityID: pr["id"], Attrs: attrs,
				},
				ActorType:  "system",
				Importance: service.ImportanceNormal,
				Categories: []string{service.CategoryProgress},
				OccurredAt: time.Now(),
			})
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

// prFromPayload flattens the "pull_request" value of a pull_request:* event to
// the handful of plain strings the stream carries. Producers send a typed
// response struct, so it goes through JSON rather than a type switch.
func prFromPayload(payload any) map[string]string {
	out := map[string]string{}
	m, ok := payload.(map[string]any)
	if !ok {
		return out
	}
	raw, err := json.Marshal(m["pull_request"])
	if err != nil {
		return out
	}
	var pr map[string]any
	if json.Unmarshal(raw, &pr) != nil {
		return out
	}
	str := func(k string) string {
		switch v := pr[k].(type) {
		case string:
			return v
		case float64:
			return fmt.Sprintf("%d", int64(v))
		}
		return ""
	}
	for _, k := range []string{"id", "number", "state", "title", "html_url", "merged_at", "pr_updated_at"} {
		out[k] = str(k)
	}
	if owner, name := str("repo_owner"), str("repo_name"); owner != "" && name != "" {
		out["repo"] = owner + "/" + name
	}
	return out
}
