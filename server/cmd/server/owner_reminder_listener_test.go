package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type recordingWaker struct {
	mu  sync.Mutex
	ids []string
	hit chan struct{}
}

func (r *recordingWaker) WakeOnStatus(_ context.Context, id string) error {
	r.mu.Lock()
	r.ids = append(r.ids, id)
	r.mu.Unlock()
	r.hit <- struct{}{}
	return nil
}

func TestOwnerReminderListenerWakesOnDeliveredBlockedOrDoneInWatchedWorkspace(t *testing.T) {
	const ws = "11111111-1111-1111-1111-111111111111"
	bus := events.New()
	waker := &recordingWaker{hit: make(chan struct{}, 8)}
	registerOwnerReminderListener(bus, ws, waker)
	publish := func(workspace string, payload map[string]any) {
		bus.Publish(events.Event{Type: protocol.EventIssueUpdated, WorkspaceID: workspace, Payload: payload})
	}
	// Handler payload shape, task-service payload shape: both wake.
	publish(ws, map[string]any{"status_changed": true, "issue": handler.IssueResponse{ID: "handler-review", Status: "in_review"}})
	publish(ws, map[string]any{"status_changed": true, "issue": map[string]any{"id": "service-blocked", "status": "blocked"}})
	publish(ws, map[string]any{"status_changed": true, "issue": handler.IssueResponse{ID: "closed", Status: "done"}})
	// None of these wake.
	publish(ws, map[string]any{"status_changed": false, "issue": handler.IssueResponse{ID: "title-edit", Status: "in_review"}})
	publish(ws, map[string]any{"status_changed": true, "issue": handler.IssueResponse{ID: "working", Status: "in_progress"}})
	publish("22222222-2222-2222-2222-222222222222", map[string]any{"status_changed": true, "issue": handler.IssueResponse{ID: "other-ws", Status: "blocked"}})
	for range 3 {
		select {
		case <-waker.hit:
		case <-time.After(5 * time.Second):
			t.Fatal("listener did not wake")
		}
	}
	select {
	case <-waker.hit:
		t.Fatalf("unexpected extra wake: %v", waker.ids)
	case <-time.After(200 * time.Millisecond):
	}
	waker.mu.Lock()
	defer waker.mu.Unlock()
	got := map[string]bool{}
	for _, id := range waker.ids {
		got[id] = true
	}
	if len(got) != 3 || !got["handler-review"] || !got["service-blocked"] || !got["closed"] {
		t.Fatalf("woke %v", waker.ids)
	}
}
