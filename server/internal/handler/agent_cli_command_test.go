package handler

import (
	"context"
	"testing"
)

func TestAgentCLICommandStoreKeepsFollowAndClearsOneUpdate(t *testing.T) {
	store := NewInMemoryAgentCLICommandStore()
	ctx := context.Background()
	follow := false
	if err := store.SetFollow(ctx, "rt-1", follow); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUpdate(ctx, "rt-1", "req-1"); err != nil {
		t.Fatal(err)
	}
	cmd, err := store.Peek(ctx, "rt-1")
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || cmd.Follow == nil || *cmd.Follow || !cmd.UpdateNow || cmd.RequestID != "req-1" {
		t.Fatalf("peek = %#v", cmd)
	}
	// A second peek still has the command. Heartbeats must be able to retry.
	again, err := store.Peek(ctx, "rt-1")
	if err != nil || again == nil || again.RequestID != "req-1" {
		t.Fatalf("second peek = %#v, %v", again, err)
	}
	if err := store.ClearUpdate(ctx, "rt-1", "other"); err != nil {
		t.Fatal(err)
	}
	kept, _ := store.Peek(ctx, "rt-1")
	if kept == nil || kept.RequestID != "req-1" {
		t.Fatalf("unrelated clear removed the request: %#v", kept)
	}
	if err := store.ClearUpdate(ctx, "rt-1", "req-1"); err != nil {
		t.Fatal(err)
	}
	left, _ := store.Peek(ctx, "rt-1")
	if left == nil || left.UpdateNow || left.Follow == nil || *left.Follow {
		t.Fatalf("follow should remain after the update is cleared: %#v", left)
	}
}
