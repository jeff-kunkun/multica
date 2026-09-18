package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// Switching a live alignment conversation's policy or runtime is only worth
// anything if the NEXT reply actually runs the new way, and if the prompt
// version recorded on the draft still names the prompt that reply was handed.
// The unit of evidence is therefore not the handler's response but the queued
// task and the carrier agent it will be claimed for:
//
//	carrier agent row --instructions--> next reply's prompt
//	issue_draft row   --policy_version--> the audit trail
//
// `agent_task_queue` stamps a chat task with the carrier as it is re-read under
// LockChatSessionForRuntimeBind, and the daemon reads instructions off that
// agent at claim time, so these two rows are the whole chain. Asserting them
// together is what makes "the switch took effect" checkable without a daemon.

// draftChatTaskCarrier reads the agent and runtime of the newest queued reply in
// a live alignment conversation.
func draftChatTaskCarrier(t *testing.T, sessionID string) (agentID, runtimeID string) {
	t.Helper()
	dbfx.QueryRow(t, `
		SELECT agent_id::text, COALESCE(runtime_id::text, '')
		FROM agent_task_queue
		WHERE chat_session_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`, sessionID).Scan(&agentID, &runtimeID)
	return agentID, runtimeID
}

// sendDraftChatTurn sends one user turn the way the send endpoint does: the
// agent is loaded here and handed over stale, which is exactly the state a
// switch landing between the two reads produces. The reply that comes back must
// still run on what the session is bound to now.
func sendDraftChatTurn(t *testing.T, sessionID, content string) {
	t.Helper()
	ctx := context.Background()
	session, err := testHandler.Queries.GetChatSession(ctx, parseUUID(sessionID))
	if err != nil {
		t.Fatalf("load chat session: %v", err)
	}
	agent, err := testHandler.Queries.GetAgent(ctx, session.AgentID)
	if err != nil {
		t.Fatalf("load carrier: %v", err)
	}
	if _, err := testHandler.TaskService.SendDirectChatMessage(
		ctx, session, agent, parseUUID(testUserID), content, nil, "member", parseUUID(testUserID),
	); err != nil {
		t.Fatalf("SendDirectChatMessage: %v", err)
	}
	// The task outlives the conversation's agent (agent_task_queue.chat_session_id
	// is ON DELETE SET NULL), so it needs its own cleanup.
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, sessionID)
	})
}

// assertRecordedPromptIsTheNextReplysPrompt is the auditability property in one
// check: the prompt version the draft records must be the prompt the agent
// carrying the next reply actually has. A registry whose version moved without
// its prompt moving — or a switch that wrote one without the other — fails here.
func assertRecordedPromptIsTheNextReplysPrompt(t *testing.T, sessionID, agentID string) {
	t.Helper()
	key, version := carrierPolicyKey(t, sessionID)
	recorded, ok := issueDraftPolicyByKey(key)
	if !ok {
		t.Fatalf("the draft records policy %q, which is not in the registry", key)
	}
	if recorded.Version != version {
		t.Fatalf("the draft records %s@%s, but the registry's prompt for %s is version %s — the audit trail names a prompt nobody can read",
			key, version, key, recorded.Version)
	}
	if got := carrierInstructions(t, agentID); got != defaultInstructions(t, recorded) {
		t.Fatalf("the next reply's carrier does not hold the prompt %s@%s records", key, version)
	}
}

// The guided policy's whole visible effect is the question block, so a switch to
// plain dialogue has to be observable as "the prompt no longer teaches it" —
// asserted on the prompt the next reply gets, not on the registry.
func TestPolicySwitchChangesThePromptOfTheNextReply(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	questionPolicy, ok := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	if !ok {
		t.Fatal("the guided question policy is not registered")
	}
	if got := carrierInstructions(t, session.AgentID); got != defaultInstructions(t, questionPolicy) {
		t.Fatal("the conversation did not open on the guided prompt")
	}

	conversationPolicy, ok := issueDraftPolicyByKey(issueDraftPolicyConversation)
	if !ok {
		t.Fatal("the conversation policy is not registered")
	}
	testutil.Call(t, testHandler.SwitchIssueDraftPolicy,
		switchPolicyRequest(t, session.SessionID, issueDraftPolicyConversation)).
		Want(http.StatusOK)

	// The next turn, sent with the pre-switch agent in hand.
	sendDraftChatTurn(t, session.SessionID, "no more questions, just the draft")

	agentID, _ := draftChatTaskCarrier(t, session.SessionID)
	if agentID != session.AgentID {
		t.Fatalf("the next reply is queued for agent %s, want the conversation's carrier %s", agentID, session.AgentID)
	}
	prompt := carrierInstructions(t, agentID)
	if prompt != defaultInstructions(t, conversationPolicy) {
		t.Fatal("the next reply's carrier still holds the prompt the switch replaced")
	}
	if strings.Contains(prompt, "<issue_draft_question>") {
		t.Fatal("the unguided prompt still teaches the question block; an answer chip would keep appearing after the user closed the guidance")
	}
	assertRecordedPromptIsTheNextReplysPrompt(t, session.SessionID, agentID)
}

// A rebind is a different kind of switch: it changes where the reply runs and
// must leave the prompt — and the version recorded for it — alone.
func TestRuntimeSwitchStampsTheNextReplyWithTheNewRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	target := newTestRuntime(t, "Issue Draft Switch Target", "online")

	var switched SwitchIssueDraftRuntimeResponse
	testutil.Call(t, testHandler.SwitchIssueDraftRuntime, withURLParam(
		newRequest(http.MethodPatch, "/api/issue-drafts/"+session.SessionID+"/runtime", map[string]any{
			"runtime_id": target,
		}), "sessionId", session.SessionID,
	)).Want(http.StatusOK).JSON(&switched)
	if switched.RuntimeID != target {
		t.Fatalf("switch reported runtime %q, want %q", switched.RuntimeID, target)
	}

	// The carrier is what stamps a task's runtime, so this row — not the echo in
	// the response — is the switch.
	boundRuntime, boundMode := carrierRuntime(t, session.AgentID)
	if boundRuntime != target {
		t.Fatalf("carrier runtime = %q, want %q", boundRuntime, target)
	}
	if boundMode != "cloud" {
		t.Fatalf("carrier runtime_mode = %q, want cloud", boundMode)
	}
	// Deliberately stale: the daemon only resumes a stored provider session when
	// this pointer matches the claiming task's runtime.
	var sessionRuntime string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM chat_session WHERE id = $1`, session.SessionID).Scan(&sessionRuntime)
	if sessionRuntime != testRuntimeID {
		t.Fatalf("chat_session.runtime_id = %q, want the original %q", sessionRuntime, testRuntimeID)
	}

	sendDraftChatTurn(t, session.SessionID, "carry on")

	agentID, taskRuntime := draftChatTaskCarrier(t, session.SessionID)
	if taskRuntime != target {
		t.Fatalf("the next reply is queued for runtime %q, want the switched-to %q", taskRuntime, target)
	}
	if agentID != session.AgentID {
		t.Fatalf("the next reply is queued for agent %s, want the conversation's carrier %s", agentID, session.AgentID)
	}
	// A runtime switch is not a policy switch: the prompt that reply runs under
	// must still be the one the draft records.
	assertRecordedPromptIsTheNextReplysPrompt(t, session.SessionID, agentID)
}

// carrierRuntime reads what the daemon will claim the carrier for.
func carrierRuntime(t *testing.T, agentID string) (runtimeID, runtimeMode string) {
	t.Helper()
	dbfx.QueryRow(t, `
		SELECT COALESCE(runtime_id::text, ''), runtime_mode FROM agent WHERE id = $1
	`, agentID).Scan(&runtimeID, &runtimeMode)
	return runtimeID, runtimeMode
}

// A reply already in flight was claimed on the old runtime. Rebinding under it
// would make the switch look like it did not take, so the send is refused.
func TestSwitchIssueDraftRuntimeRefusesWhileTurnIsPending(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	target := newTestRuntime(t, "Issue Draft Switch Pending Target", "online")
	dbfx.Task(t, session.AgentID, testutil.Cols{
		"chat_session_id": session.SessionID,
		"runtime_id":      testRuntimeID,
		"status":          "running",
	})

	testutil.Call(t, testHandler.SwitchIssueDraftRuntime, withURLParam(
		newRequest(http.MethodPatch, "/api/issue-drafts/"+session.SessionID+"/runtime", map[string]any{
			"runtime_id": target,
		}), "sessionId", session.SessionID,
	)).Want(http.StatusConflict)

	if bound, _ := carrierRuntime(t, session.AgentID); bound != testRuntimeID {
		t.Fatalf("a refused runtime switch rebound the carrier to %q", bound)
	}
}

// The daemon can only run a reply on a machine that is online, and the target
// has to live in this workspace.
func TestSwitchIssueDraftRuntimeRejectsOfflineAndForeignTargets(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	offline := newTestRuntime(t, "Issue Draft Switch Offline", "offline")

	testutil.Call(t, testHandler.SwitchIssueDraftRuntime, withURLParam(
		newRequest(http.MethodPatch, "/api/issue-drafts/"+session.SessionID+"/runtime", map[string]any{
			"runtime_id": offline,
		}), "sessionId", session.SessionID,
	)).Want(http.StatusConflict)

	otherWorkspace := dbfx.Workspace(t, "Issue Draft Switch Other WS", "issue-draft-switch-other-ws")
	var foreignRuntime string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, NULL, 'Issue Draft Switch Foreign', 'cloud', 'claude', 'online', 'foreign', '{}'::jsonb, $2, now())
		RETURNING id
	`, otherWorkspace, testUserID).Scan(&foreignRuntime); err != nil {
		t.Fatalf("create foreign runtime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, foreignRuntime)
	})

	testutil.Call(t, testHandler.SwitchIssueDraftRuntime, withURLParam(
		newRequest(http.MethodPatch, "/api/issue-drafts/"+session.SessionID+"/runtime", map[string]any{
			"runtime_id": foreignRuntime,
		}), "sessionId", session.SessionID,
	)).Want(http.StatusBadRequest)

	if bound, _ := carrierRuntime(t, session.AgentID); bound != testRuntimeID {
		t.Fatalf("a refused runtime switch rebound the carrier to %q", bound)
	}
}
