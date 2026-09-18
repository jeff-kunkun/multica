package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestValidateJevStatusSnapshot(t *testing.T) {
	t.Run("nil reports nothing to store", func(t *testing.T) {
		data, err := validateJevStatusSnapshot(nil)
		if err != nil || data != nil {
			t.Fatalf("data=%v err=%v, want nil/nil", data, err)
		}
	})

	t.Run("unknown with observed_at zero is accepted", func(t *testing.T) {
		data, err := validateJevStatusSnapshot(&protocol.JevStatusSnapshot{Status: protocol.JevStatusUnknown})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var snapshot protocol.JevStatusSnapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if snapshot.Status != protocol.JevStatusUnknown || snapshot.ObservedAt != 0 {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	})

	t.Run("unknown status is rejected", func(t *testing.T) {
		if _, err := validateJevStatusSnapshot(&protocol.JevStatusSnapshot{Status: "degraded"}); err == nil {
			t.Fatal("want an error for a status outside the enum")
		}
	})

	t.Run("long free text is truncated, not rejected", func(t *testing.T) {
		data, err := validateJevStatusSnapshot(&protocol.JevStatusSnapshot{
			Status:      protocol.JevStatusFallback,
			Reason:      strings.Repeat("额", maxJevReasonLen+50),
			Model:       strings.Repeat("m", maxJevModelLen+10),
			LastScene:   strings.Repeat("s", maxJevSceneLen+10),
			LastOutcome: strings.Repeat("o", maxJevOutcomeLen+10),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var snapshot protocol.JevStatusSnapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			t.Fatalf("decode: %v", err)
		}
		// Runes, not bytes: byte truncation of Chinese text would emit invalid
		// UTF-8 and Postgres would reject the JSONB payload.
		if len([]rune(snapshot.Reason)) != maxJevReasonLen {
			t.Fatalf("reason runes = %d, want %d", len([]rune(snapshot.Reason)), maxJevReasonLen)
		}
		if !strings.HasPrefix(snapshot.Reason, "额") {
			t.Fatalf("reason lost its leading text: %q", snapshot.Reason[:3])
		}
		if len(snapshot.Model) != maxJevModelLen || len(snapshot.LastScene) != maxJevSceneLen || len(snapshot.LastOutcome) != maxJevOutcomeLen {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	})

	t.Run("negative counters are clamped", func(t *testing.T) {
		data, err := validateJevStatusSnapshot(&protocol.JevStatusSnapshot{
			Status:         protocol.JevStatusActive,
			Failures:       -3,
			DisabledUntil:  -1,
			LastDecisionAt: -1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var snapshot protocol.JevStatusSnapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if snapshot.Failures != 0 || snapshot.DisabledUntil != 0 || snapshot.LastDecisionAt != 0 {
			t.Fatalf("snapshot = %+v, want clamped zeros", snapshot)
		}
	})
}

func TestRuntimeToResponse_ExposesJevStatus(t *testing.T) {
	stored, err := json.Marshal(protocol.JevStatusSnapshot{
		Status:        protocol.JevStatusFallback,
		Model:         "jev-1.13.0",
		Reason:        "额度见底",
		DisabledUntil: 1_800_000_900,
		ObservedAt:    1_800_000_000,
	})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	resp := runtimeToResponse(db.AgentRuntime{JevStatus: stored})
	if resp.Jev == nil || resp.Jev.Status != protocol.JevStatusFallback || resp.Jev.Reason != "额度见底" {
		t.Fatalf("jev = %+v", resp.Jev)
	}

	empty := runtimeToResponse(db.AgentRuntime{})
	if empty.Jev != nil {
		t.Fatalf("NULL column must omit jev, got %+v", empty.Jev)
	}
}

func TestDaemonHeartbeat_StoresJevStatus(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	daemonID := "jev-daemon-" + uuid.New().String()
	runtimeID := dbfx.Runtime(t, "JEV Runtime", testutil.Cols{
		"daemon_id":   daemonID,
		"provider":    "codex",
		"device_info": "JEV status test",
	})

	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/heartbeat", map[string]any{
		"runtime_id": runtimeID,
		"jev": map[string]any{
			"status":           protocol.JevStatusFallback,
			"model":            "jev-1.13.0",
			"manual":           true,
			"disabled_until":   int64(1_800_000_900),
			"failures":         3,
			"reason":           "额度见底",
			"last_scene":       "pr-risk",
			"last_outcome":     "@1",
			"last_decision_at": int64(1_800_000_000),
			"observed_at":      int64(1_799_999_000),
			"api_key":          "must-not-be-stored",
		},
	}, testWorkspaceID, daemonID)
	testutil.Call(t, testHandler.DaemonHeartbeat, req).Want(http.StatusOK)

	var stored []byte
	if err := testPool.QueryRow(context.Background(), `
		SELECT jev_status FROM agent_runtime WHERE id = $1
	`, runtimeID).Scan(&stored); err != nil {
		t.Fatalf("read stored jev status: %v", err)
	}
	if bytes.Contains(stored, []byte("must-not-be-stored")) || bytes.Contains(stored, []byte("api_key")) {
		t.Fatalf("stored jev status contains an unapproved field: %s", stored)
	}
	var snapshot protocol.JevStatusSnapshot
	if err := json.Unmarshal(stored, &snapshot); err != nil {
		t.Fatalf("decode stored jev status: %v", err)
	}
	if snapshot.Status != protocol.JevStatusFallback || snapshot.Reason != "额度见底" ||
		snapshot.Model != "jev-1.13.0" || !snapshot.Manual || snapshot.LastOutcome != "@1" {
		t.Fatalf("stored jev status = %+v", snapshot)
	}

	// A second heartbeat carrying the explicit unknown snapshot must overwrite
	// the stored fallback: that is how a blind-but-online daemon clears a stale
	// status instead of leaving it lit.
	unknownReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/heartbeat", map[string]any{
		"runtime_id": runtimeID,
		"jev": map[string]any{
			"status":      protocol.JevStatusUnknown,
			"observed_at": int64(0),
		},
	}, testWorkspaceID, daemonID)
	testutil.Call(t, testHandler.DaemonHeartbeat, unknownReq).Want(http.StatusOK)

	if err := testPool.QueryRow(context.Background(), `
		SELECT jev_status FROM agent_runtime WHERE id = $1
	`, runtimeID).Scan(&stored); err != nil {
		t.Fatalf("re-read stored jev status: %v", err)
	}
	if err := json.Unmarshal(stored, &snapshot); err != nil {
		t.Fatalf("decode overwritten jev status: %v", err)
	}
	if snapshot.Status != protocol.JevStatusUnknown {
		t.Fatalf("stored jev status = %+v, want unknown", snapshot)
	}

	// An out-of-enum status is rejected and must not touch the stored column.
	badReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/heartbeat", map[string]any{
		"runtime_id": runtimeID,
		"jev":        map[string]any{"status": "degraded", "observed_at": int64(1_800_000_000)},
	}, testWorkspaceID, daemonID)
	testutil.Call(t, testHandler.DaemonHeartbeat, badReq).Want(http.StatusBadRequest)

	if err := testPool.QueryRow(context.Background(), `
		SELECT jev_status FROM agent_runtime WHERE id = $1
	`, runtimeID).Scan(&stored); err != nil {
		t.Fatalf("re-read after rejected heartbeat: %v", err)
	}
	if err := json.Unmarshal(stored, &snapshot); err != nil {
		t.Fatalf("decode after rejected heartbeat: %v", err)
	}
	if snapshot.Status != protocol.JevStatusUnknown {
		t.Fatalf("rejected heartbeat mutated the column: %+v", snapshot)
	}
}
