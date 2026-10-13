package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setRuntimeTestMemberRole(t *testing.T, userID, role string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `
		UPDATE member
		SET role = $3
		WHERE workspace_id = $1 AND user_id = $2
	`, testWorkspaceID, userID, role); err != nil {
		t.Fatalf("set runtime test member role to %s: %v", role, err)
	}
	if testHandler.MembershipCache != nil {
		testHandler.MembershipCache.Invalidate(context.Background(), userID, testWorkspaceID)
	}
}

func promoteRuntimeTestMemberToOwner(t *testing.T, userID string) {
	t.Helper()
	setRuntimeTestMemberRole(t, userID, "owner")
}

func TestInitiateUpdateRequiresRuntimeManager(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	tests := []struct {
		name       string
		actor      string
		wantStatus int
	}{
		{name: "runtime owner", actor: "runtime_owner", wantStatus: http.StatusOK},
		{name: "workspace owner on another private runtime", actor: "workspace_owner", wantStatus: http.StatusNotFound},
		{name: "plain member on another private runtime", actor: "plain_member", wantStatus: http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runtimeID, runtimeOwnerID, plainMemberID := runtimeVisibilityFixture(t)
			actorID := runtimeOwnerID
			switch tc.actor {
			case "workspace_owner":
				promoteRuntimeTestMemberToOwner(t, plainMemberID)
				actorID = plainMemberID
			case "plain_member":
				actorID = plainMemberID
			}

			w := httptest.NewRecorder()
			req := withURLParam(
				newRequestAs(actorID, http.MethodPost, "/api/runtimes/"+runtimeID+"/update", map[string]any{
					"target_version": "v9.9.9",
				}),
				"runtimeId",
				runtimeID,
			)
			testHandler.InitiateUpdate(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tc.wantStatus, w.Code, w.Body.String())
			}

			hasPending, err := testHandler.UpdateStore.HasPending(context.Background(), runtimeID)
			if err != nil {
				t.Fatalf("check pending update request: %v", err)
			}
			wantPending := tc.wantStatus == http.StatusOK
			if hasPending != wantPending {
				t.Fatalf("pending update request = %v; want %v", hasPending, wantPending)
			}
		})
	}
}

func TestGetUpdateRequiresRuntimeManager(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	tests := []struct {
		name       string
		actor      string
		wantStatus int
	}{
		{name: "runtime owner", actor: "runtime_owner", wantStatus: http.StatusOK},
		{name: "workspace owner on another private runtime", actor: "workspace_owner", wantStatus: http.StatusNotFound},
		{name: "plain member on another private runtime", actor: "plain_member", wantStatus: http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runtimeID, runtimeOwnerID, plainMemberID := runtimeVisibilityFixture(t)
			actorID := runtimeOwnerID
			switch tc.actor {
			case "workspace_owner":
				promoteRuntimeTestMemberToOwner(t, plainMemberID)
				actorID = plainMemberID
			case "plain_member":
				actorID = plainMemberID
			}

			update, err := testHandler.UpdateStore.Create(context.Background(), runtimeID, "v9.9.9", runtimeOwnerID)
			if err != nil {
				t.Fatalf("create update request: %v", err)
			}

			w := httptest.NewRecorder()
			req := withURLParams(
				newRequestAs(actorID, http.MethodGet, "/api/runtimes/"+runtimeID+"/update/"+update.ID, nil),
				"runtimeId", runtimeID,
				"updateId", update.ID,
			)
			testHandler.GetUpdate(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tc.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}

func TestGetUpdateAllowsInitiatorAfterOwnerDemotionOnPublicRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID, _, ownerID := runtimeVisibilityFixture(t)
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_runtime
		SET visibility = 'public'
		WHERE id = $1
	`, runtimeID); err != nil {
		t.Fatalf("make runtime public: %v", err)
	}
	promoteRuntimeTestMemberToOwner(t, ownerID)

	initRecorder := httptest.NewRecorder()
	initRequest := withURLParam(
		newRequestAs(ownerID, http.MethodPost, "/api/runtimes/"+runtimeID+"/update", map[string]any{
			"target_version": "v9.9.9",
		}),
		"runtimeId",
		runtimeID,
	)
	testHandler.InitiateUpdate(initRecorder, initRequest)
	if initRecorder.Code != http.StatusOK {
		t.Fatalf("initiate update: expected 200, got %d: %s", initRecorder.Code, initRecorder.Body.String())
	}

	var update UpdateRequest
	if err := json.Unmarshal(initRecorder.Body.Bytes(), &update); err != nil {
		t.Fatalf("decode initiated update: %v", err)
	}
	if update.InitiatorUserID != "" {
		t.Fatalf("initiator user ID leaked in API response: %q", update.InitiatorUserID)
	}
	setRuntimeTestMemberRole(t, ownerID, "member")

	pollRecorder := httptest.NewRecorder()
	pollRequest := withURLParams(
		newRequestAs(ownerID, http.MethodGet, "/api/runtimes/"+runtimeID+"/update/"+update.ID, nil),
		"runtimeId", runtimeID,
		"updateId", update.ID,
	)
	testHandler.GetUpdate(pollRecorder, pollRequest)
	if pollRecorder.Code != http.StatusOK {
		t.Fatalf("poll after owner demotion: expected 200, got %d: %s", pollRecorder.Code, pollRecorder.Body.String())
	}
}
