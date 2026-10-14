package botevents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestShouldRecord(t *testing.T) {
	for typ, want := range map[string]bool{
		"issue:created":    true,
		"comment:created":  true,
		"project:updated":  true,
		"plan:approved":    true,
		"member:added":     true,
		"task:completed":   true,
		"task:failed":      true,
		"task:message":     false,
		"task:dispatch":    false,
		"daemon:heartbeat": false,
		"inbox:new":        false,
	} {
		if got := ShouldRecord(typ); got != want {
			t.Errorf("ShouldRecord(%q) = %v, want %v", typ, got, want)
		}
	}
}

func TestSign(t *testing.T) {
	// printf '{"a":1}' | openssl dgst -sha256 -hmac secret
	const want = "sha256=aa9e2e3575f5d7098b6caccd790888c36d5fdb63342a73bada2d6a51747a8494"
	if got := Sign("secret", []byte(`{"a":1}`)); got != want {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
}

func TestBackoff(t *testing.T) {
	for failures, want := range map[int32]time.Duration{
		1: 2 * time.Second, 2: 4 * time.Second, 8: 256 * time.Second, 9: 5 * time.Minute, 40: 5 * time.Minute,
	} {
		if got := backoff(failures); got != want {
			t.Errorf("backoff(%d) = %v, want %v", failures, got, want)
		}
	}
}

// TestDeliverer runs against a real database when DATABASE_URL is set.
// workspace_bot and bot_event have no foreign keys, so the test only needs
// its own random workspace id.
func TestDeliverer(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil || pool.Ping(ctx) != nil {
		t.Skip("database not reachable")
	}
	defer pool.Close()
	q := db.New(pool)

	var mu sync.Mutex
	var got []WebhookBody
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Header.Get(SignatureHeader) != Sign("s3cret", raw) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		// The receiver's own key, registered by the Bot.
		if r.Header.Get("Authorization") != "Bearer recv-key" {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if fail {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var body WebhookBody
		_ = json.Unmarshal(raw, &body)
		got = append(got, body)
	}))
	defer srv.Close()

	var wsID, userID string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).Scan(&wsID, &userID); err != nil {
		t.Fatal(err)
	}
	ws := util.MustParseUUID(wsID)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM bot_event WHERE workspace_id = $1`, wsID)
		pool.Exec(context.Background(), `DELETE FROM workspace_bot WHERE workspace_id = $1`, wsID)
	})
	bot, err := q.CreateWorkspaceBot(ctx, db.CreateWorkspaceBotParams{
		WorkspaceID: ws, UserID: util.MustParseUUID(userID), Name: "hook",
		WebhookUrl: srv.URL, WebhookSecret: "s3cret", CreatedBy: util.MustParseUUID(userID),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Bot headers are sent but cannot replace the signature.
	if _, err := pool.Exec(ctx, `UPDATE workspace_bot SET webhook_headers = $2 WHERE id = $1`, bot.ID,
		`{"Authorization": "Bearer recv-key", "X-Multica-Signature": "forged"}`); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"issue:created", "comment:created", "issue:updated"} {
		if err := q.InsertBotEvent(ctx, db.InsertBotEventParams{
			WorkspaceID: ws, Type: typ, ActorType: "member", ActorID: userID, Payload: []byte(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	d := NewDeliverer(q)
	d.BatchSize = 2
	reload := func() db.WorkspaceBot {
		b, err := q.GetWorkspaceBot(ctx, db.GetWorkspaceBotParams{ID: bot.ID, WorkspaceID: ws})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	// A failing endpoint keeps the cursor and schedules a retry.
	d.deliver(ctx, reload())
	if b := reload(); b.WebhookCursor != bot.WebhookCursor || b.WebhookFailures != 1 || b.WebhookLastError == "" {
		t.Fatalf("after failure: cursor=%d failures=%d err=%q", b.WebhookCursor, b.WebhookFailures, b.WebhookLastError)
	}

	// Once it recovers, events arrive in order, in batches, exactly once.
	mu.Lock()
	fail = false
	mu.Unlock()
	d.deliver(ctx, reload())
	d.deliver(ctx, reload())
	d.deliver(ctx, reload())
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || len(got[0].Events) != 2 || len(got[1].Events) != 1 {
		t.Fatalf("deliveries = %+v", got)
	}
	if got[0].Events[0].Type != "issue:created" || got[1].Events[0].Type != "issue:updated" {
		t.Fatalf("order = %+v", got)
	}
	if b := reload(); b.WebhookCursor != got[1].NextCursor || b.WebhookFailures != 0 || b.WebhookLastError != "" {
		t.Fatalf("after success: cursor=%d failures=%d", b.WebhookCursor, b.WebhookFailures)
	}
}

// TestRecordsWhatAPersonWouldRead checks that an inbox item is recorded only
// for a person a Bot is or stands in for, and reaches only that Bot.
func TestRecordsWhatAPersonWouldRead(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil || pool.Ping(ctx) != nil {
		t.Skip("database not reachable")
	}
	defer pool.Close()
	q := db.New(pool)

	ids := make([]string, 6)
	for i := range ids {
		if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	wsID, botA, kun, botB, colleague, stranger := ids[0], ids[1], ids[2], ids[3], ids[4], ids[5]
	ws := util.MustParseUUID(wsID)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM bot_event WHERE workspace_id = $1`, wsID)
		pool.Exec(context.Background(), `DELETE FROM workspace_bot WHERE workspace_id = $1`, wsID)
	})
	bots := map[string]db.WorkspaceBot{}
	for user, creator := range map[string]string{botA: kun, botB: colleague} {
		b, err := q.CreateWorkspaceBot(ctx, db.CreateWorkspaceBotParams{
			WorkspaceID: ws, UserID: util.MustParseUUID(user), Name: "pm",
			CreatedBy: util.MustParseUUID(creator),
		})
		if err != nil {
			t.Fatal(err)
		}
		bots[user] = b
	}

	bus := events.New()
	Register(bus, q)
	inbox := func(recipient string) {
		bus.Publish(events.Event{Type: protocol.EventInboxNew, WorkspaceID: wsID, Payload: map[string]any{
			"item": map[string]any{"recipient_id": recipient, "title": "for " + recipient},
		}})
	}
	inbox(kun)
	inbox(botB)
	inbox(stranger)
	bus.Publish(events.Event{Type: "issue:created", WorkspaceID: wsID, Payload: map[string]any{}})

	seen := func(user string) []string {
		b := bots[user]
		rows, err := q.ListBotEventsAfter(ctx, db.ListBotEventsAfterParams{
			WorkspaceID: ws, After: 0, BotUserID: b.UserID, BotCreatorID: b.CreatedBy, LimitCount: 50,
		})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			var p struct {
				For string `json:"for_user_id"`
			}
			_ = json.Unmarshal(r.Payload, &p)
			out = append(out, r.Type+"/"+p.For)
		}
		return out
	}
	if got, want := seen(botA), []string{"inbox:new/" + kun, "issue:created/"}; !slices.Equal(got, want) {
		t.Fatalf("Kun's Bot sees %v, want %v", got, want)
	}
	if got, want := seen(botB), []string{"inbox:new/" + botB, "issue:created/"}; !slices.Equal(got, want) {
		t.Fatalf("colleague's Bot sees %v, want %v", got, want)
	}
}
