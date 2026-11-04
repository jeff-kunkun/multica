package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/issuewatchdog"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// statusWakeTimeout bounds one event-path enqueue; the next scan retries.
const statusWakeTimeout = 15 * time.Second

// statusWaker is the part of the watchdog the listener calls.
type statusWaker interface {
	WakeOnStatus(ctx context.Context, issueID string) error
}

// registerOwnerReminderListener queues the owner's status reminder the moment
// an issue in the watched workspace is delivered, blocked or done (DENE-2967,
// DENE-2981); the watchdog drops moves made by members. It
// only writes the outbox row; delivery stays with the transport worker, and
// the minute scan repeats the check for anything this path missed.
func registerOwnerReminderListener(bus *events.Bus, workspaceID string, waker statusWaker) {
	bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		issueID, ok := statusWakeTarget(e, workspaceID)
		if !ok {
			return
		}
		// The bus is synchronous; never hold the request that changed status.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), statusWakeTimeout)
			defer cancel()
			if err := waker.WakeOnStatus(ctx, issueID); err != nil {
				slog.Warn("owner status reminder enqueue failed; the scan retries", "issue_id", issueID)
			}
		}()
	})
}

func statusWakeTarget(e events.Event, workspaceID string) (string, bool) {
	if e.WorkspaceID != workspaceID {
		return "", false
	}
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		return "", false
	}
	if changed, _ := payload["status_changed"].(bool); !changed {
		return "", false
	}
	// Handlers publish an IssueResponse; the task service publishes a map.
	var id, status string
	switch issue := payload["issue"].(type) {
	case handler.IssueResponse:
		id, status = issue.ID, issue.Status
	case map[string]any:
		id, _ = issue["id"].(string)
		status, _ = issue["status"].(string)
	}
	if id == "" || !issuewatchdog.WakesOnStatus(status) {
		return "", false
	}
	return id, true
}
