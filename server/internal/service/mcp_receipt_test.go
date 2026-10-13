package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestOfficeReceiptDoesNotBecomeAnotherWorkspaceEvent(t *testing.T) {
	var ws pgtype.UUID
	_ = ws.Scan("00000000-0000-4000-8000-000000000001")
	// A nil DB proves receipt classification exits before any event lookup or write.
	n, err := RouteWorkspaceWrite(context.Background(), nil, WorkspaceWrite{
		WorkspaceID: ws, Method: "POST", Route: "/api/workspaces/{id}/bots/{botId}/office-events/{eventId}/receipt",
	}, nil)
	if n != 0 || err != nil {
		t.Fatalf("receipts emitted activity: %d %v", n, err)
	}
}
