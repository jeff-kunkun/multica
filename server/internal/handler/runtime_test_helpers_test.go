package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newRequestAsUser(userID, method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	return req
}

func createOnlineTestRuntime(t *testing.T, ownerID string) string {
	t.Helper()

	runtimeName := fmt.Sprintf("runtime-online-%d", time.Now().UnixNano())
	daemonID := fmt.Sprintf("runtime-online-daemon-%d", time.Now().UnixNano())

	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, $2, $3, 'local', 'claude', 'online', 'Online Runtime Test', '{}'::jsonb, $4, now())
		RETURNING id
	`, testWorkspaceID, daemonID, runtimeName, ownerID).Scan(&runtimeID); err != nil {
		t.Fatalf("create local runtime: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})

	return runtimeID
}

func ptr[T any](value T) *T {
	return &value
}
