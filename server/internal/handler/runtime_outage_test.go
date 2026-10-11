package handler

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Real Postgres plus a real HTTP webhook: machine aggregation, debounce,
// persistent dedupe, recovery, preferences and owner isolation in one path.
func TestRuntimeOutageLifecycle(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	host := "outage-" + uuid.NewString()
	first := dbfx.Runtime(t, "Grok ("+host+")", testutil.Cols{"device_info": host + " · 1.0", "daemon_id": uuid.NewString()})
	second := dbfx.Runtime(t, "Codex ("+host+")", testutil.Cols{"device_info": host + " · 2.0", "daemon_id": uuid.NewString()})
	// Separate daemon ids on the same host must still produce a single notice.
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM runtime_machine_watch WHERE machine_key=$1`, "cloud:"+host)
		testPool.Exec(ctx, `DELETE FROM runtime_outage WHERE machine_key=$1`, "cloud:"+host)
		testPool.Exec(ctx, `DELETE FROM inbox_item WHERE details->>'machine_name'=$1`, host)
	})
	oldBox := testHandler.BotWakeSecrets
	testHandler.BotWakeSecrets, _ = NewBotWakeSecretBox("outage-local-test")
	defer func() { testHandler.BotWakeSecrets = oldBox }()
	_, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "runtime outage receiver", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	defer testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE id=$1`, bot.ID)
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		detail, ok := payload["runtime_outage"].(map[string]any)
		if !ok || detail["machine_name"] != host {
			t.Errorf("missing machine details: %v", payload)
		}
		if len(detail["runtime_ids"].([]any)) != 2 {
			t.Error("missing grouped runtimes")
		}
		expected := "runtime.offline"
		if calls.Add(1) == 2 {
			expected = "runtime.online"
		}
		if payload["type"] != expected {
			t.Errorf("type %v want %s", payload["type"], expected)
		}
		if r.Header.Get("X-Multica-Event-ID") == "" {
			t.Error("missing dedupe identity")
		}
		w.WriteHeader(200)
	}))
	defer receiver.Close()
	t.Setenv("MULTICA_PLUGIN_DEV_ORIGINS", receiver.URL)
	secret, _ := json.Marshal(botWakeSecrets{URL: receiver.URL, Key: "Bearer test"})
	sealed, _ := testHandler.BotWakeSecrets.Seal(secret)
	dbfx.Exec(t, `UPDATE bot_wake SET webhook_secret=$2,quiet_hours='{"enabled":false,"start":"23:00","end":"08:00","timezone":"UTC"}',wake_from='{"member":{"mode":"none","ids":[]},"agent":{"mode":"none","ids":[]},"bot":{"mode":"none","ids":[]}}' WHERE bot_id=$1`, bot.ID, sealed)
	worker := NewBotWakeWorker(testHandler)
	worker.client = func(*url.URL) *http.Client { return receiver.Client() }
	base := time.Now()
	sweep := func(at time.Time) {
		t.Helper()
		if err := testHandler.SweepRuntimeAlerts(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	counts := func(want int) {
		t.Helper()
		var n int
		err := testPool.QueryRow(ctx, `SELECT count(*) FROM inbox_item WHERE details->>'machine_name'=$1`, host).Scan(&n)
		if err != nil || n != want {
			t.Fatalf("inbox count %d want %d err=%v", n, want, err)
		}
	}
	process := func() {
		t.Helper()
		for i := 0; i < 50; i++ {
			worked, err := worker.ProcessNext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !worked {
				return
			}
		}
		t.Fatal("webhook queue failed to drain")
	}
	sweep(base)
	dbfx.Exec(t, `UPDATE agent_runtime SET status='offline' WHERE id=$1`, first)
	sweep(base.Add(time.Second))
	sweep(base.Add(30 * time.Second))
	counts(0) // partial daemon outage, not yet over three minutes
	dbfx.Exec(t, `UPDATE agent_runtime SET status='offline' WHERE id=$1`, second)
	sweep(base.Add(time.Minute))
	sweep(base.Add(time.Second + 3*time.Minute))
	counts(0) // strict >3m
	sweep(base.Add(3*time.Minute + 2*time.Second))
	sweep(base.Add(9 * time.Minute))
	counts(1)
	process()
	if calls.Load() != 1 {
		t.Fatalf("offline webhook count %d", calls.Load())
	}
	// Reload handler wiring to model a server restart: all dedupe state is in DB.
	hCopy := *testHandler
	if err := hCopy.SweepRuntimeAlerts(ctx, base.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	counts(1)
	dbfx.Exec(t, `UPDATE agent_runtime SET status='online' WHERE id=$1`, first)
	sweep(base.Add(11 * time.Minute))
	counts(1)
	dbfx.Exec(t, `UPDATE agent_runtime SET status='online' WHERE id=$1`, second)
	sweep(base.Add(11*time.Minute + time.Second))
	sweep(base.Add(12 * time.Minute))
	counts(2)
	process()
	if calls.Load() != 2 {
		t.Fatalf("recovery webhook count %d", calls.Load())
	}
	rows, err := testHandler.Queries.ListRuntimeOutages(ctx, db.ListRuntimeOutagesParams{WorkspaceID: parseUUID(testWorkspaceID), OwnerID: parseUUID(testUserID)})
	if err != nil {
		t.Fatal(err)
	}
	var outage db.RuntimeOutage
	for _, r := range rows {
		if r.MachineName == host {
			outage = r
		}
	}
	if !outage.EndedAt.Valid || !outage.AlertedAt.Valid {
		t.Fatal("missing start/end/alerted record")
	}
	e := db.BotWakeEvent{WorkspaceID: outage.WorkspaceID, RuntimeOutageID: outage.ID, AuthorType: "system", AuthorID: outage.OwnerID}
	if !testHandler.wakeVisible(ctx, e, outage.OwnerID) || testHandler.wakeVisible(ctx, e, parseUUID(uuid.NewString())) {
		t.Fatal("owner boundary failed")
	}
	// A brief normal restart has history but no offline or recovery notification.
	dbfx.Exec(t, `UPDATE agent_runtime SET status='offline' WHERE id=$1`, first)
	sweep(base.Add(13 * time.Minute))
	dbfx.Exec(t, `UPDATE agent_runtime SET status='online' WHERE id=$1`, first)
	sweep(base.Add(14 * time.Minute))
	counts(2)
	// A muted owner still gets history but neither channel receives new alerts.
	oldPref, oldErr := testHandler.Queries.GetNotificationPreference(ctx, db.GetNotificationPreferenceParams{WorkspaceID: outage.WorkspaceID, UserID: outage.OwnerID})
	defer func() {
		if oldErr == nil {
			testHandler.Queries.UpsertNotificationPreference(ctx, db.UpsertNotificationPreferenceParams{WorkspaceID: outage.WorkspaceID, UserID: outage.OwnerID, Preferences: oldPref.Preferences})
		} else {
			testPool.Exec(ctx, `DELETE FROM notification_preference WHERE workspace_id=$1 AND user_id=$2`, outage.WorkspaceID, outage.OwnerID)
		}
	}()
	_, err = testHandler.Queries.PatchNotificationPreference(ctx, db.PatchNotificationPreferenceParams{WorkspaceID: outage.WorkspaceID, UserID: outage.OwnerID, Preferences: []byte(`{"runtime_alerts":"muted"}`)})
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, `UPDATE agent_runtime SET status='offline' WHERE id=$1`, first)
	sweep(base.Add(15 * time.Minute))
	sweep(base.Add(19 * time.Minute))
	counts(2)
	dbfx.Exec(t, `UPDATE agent_runtime SET status='online' WHERE id=$1`, first)
	sweep(base.Add(20 * time.Minute))
	counts(2)
	t.Log("PASS: one host / two daemons -> 1 offline webhook + 1 inbox; recovery -> 1 webhook + 1 inbox; >180s gate, restart, mute and owner checks passed")
}
