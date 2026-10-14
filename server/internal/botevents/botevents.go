// Package botevents keeps the workspace event log project-manager Bots read
// after a disconnect, and pushes that log to each Bot's webhook.
//
// The log is the source of truth: the webhook only delivers what is in it, in
// id order, and a Bot that missed pushes pulls the same events from
// GET /api/bot/events?after=<cursor>. Nothing is lost while the log retains
// it (Retention).
package botevents

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Retention is how long the log keeps events.
const Retention = 7 * 24 * time.Hour

// recordedPrefixes are the event families a project manager acts on. Streaming
// and presence events (task:message, typing, daemon heartbeats) are left out.
var recordedPrefixes = []string{"issue:", "comment:", "project:", "plan:", "member:"}

var recordedTypes = map[string]bool{
	protocol.EventTaskCompleted: true,
	protocol.EventTaskFailed:    true,
	protocol.EventTaskCancelled: true,
}

// ShouldRecord reports whether an event type belongs in the Bot log.
func ShouldRecord(eventType string) bool {
	if recordedTypes[eventType] {
		return true
	}
	for _, p := range recordedPrefixes {
		if strings.HasPrefix(eventType, p) {
			return true
		}
	}
	return false
}

// Register subscribes the recorder to the bus. inbox:new is recorded only
// when it is addressed to a Bot, so an @mention of the Bot reaches it.
func Register(bus *events.Bus, q *db.Queries) {
	bus.SubscribeAll(func(e events.Event) {
		if e.WorkspaceID == "" {
			return
		}
		if e.Type == protocol.EventInboxNew {
			if !inboxForBot(q, e) {
				return
			}
		} else if !ShouldRecord(e.Type) {
			return
		}
		record(q, e)
	})
}

func inboxForBot(q *db.Queries, e events.Event) bool {
	raw, err := json.Marshal(e.Payload)
	if err != nil {
		return false
	}
	var p struct {
		Item struct {
			RecipientID string `json:"recipient_id"`
		} `json:"item"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Item.RecipientID == "" {
		return false
	}
	uid, err := util.ParseUUID(p.Item.RecipientID)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = q.GetWorkspaceBotByUser(ctx, uid)
	return err == nil
}

func record(q *db.Queries, e events.Event) {
	wsID, err := util.ParseUUID(e.WorkspaceID)
	if err != nil {
		return
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil || string(payload) == "null" {
		payload = []byte("{}")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := q.InsertBotEvent(ctx, db.InsertBotEventParams{
		WorkspaceID: wsID,
		Type:        e.Type,
		ActorType:   e.ActorType,
		ActorID:     e.ActorID,
		Payload:     payload,
	}); err != nil {
		slog.Warn("bot event record failed", "type", e.Type, "workspace_id", e.WorkspaceID, "error", err)
	}
}

// SignatureHeader carries hex(HMAC-SHA256(webhook_secret, body)).
const SignatureHeader = "X-Multica-Signature"

// Sign returns the signature header value for body.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// WebhookEvent is one event in a webhook delivery; same shape as the pull feed.
type WebhookEvent struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	ActorType string          `json:"actor_type"`
	ActorID   string          `json:"actor_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// WebhookBody is what a Bot's webhook receives.
type WebhookBody struct {
	BotID       string         `json:"bot_id"`
	WorkspaceID string         `json:"workspace_id"`
	Events      []WebhookEvent `json:"events"`
	NextCursor  int64          `json:"next_cursor"`
}

// Deliverer pushes pending events to Bot webhooks.
type Deliverer struct {
	Queries   *db.Queries
	Client    *http.Client
	Interval  time.Duration
	BatchSize int32
}

func NewDeliverer(q *db.Queries) *Deliverer {
	return &Deliverer{
		Queries:   q,
		Client:    &http.Client{Timeout: 10 * time.Second},
		Interval:  2 * time.Second,
		BatchSize: 50,
	}
}

// Run delivers until ctx ends, pruning the log once an hour.
func (d *Deliverer) Run(ctx context.Context) {
	tick := time.NewTicker(d.Interval)
	defer tick.Stop()
	var lastPrune time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if now.Sub(lastPrune) > time.Hour {
				lastPrune = now
				if err := d.Queries.PruneBotEvents(ctx, pgtype.Timestamptz{Time: now.Add(-Retention), Valid: true}); err != nil {
					slog.Warn("bot event prune failed", "error", err)
				}
			}
			d.DeliverDue(ctx)
		}
	}
}

// DeliverDue sends one batch to every Bot whose webhook has pending events.
func (d *Deliverer) DeliverDue(ctx context.Context) {
	bots, err := d.Queries.ClaimDueBotWebhooks(ctx)
	if err != nil {
		slog.Warn("bot webhook claim failed", "error", err)
		return
	}
	for _, b := range bots {
		d.deliver(ctx, b)
	}
}

func (d *Deliverer) deliver(ctx context.Context, b db.WorkspaceBot) {
	rows, err := d.Queries.ListBotEventsAfter(ctx, db.ListBotEventsAfterParams{
		WorkspaceID: b.WorkspaceID, ID: b.WebhookCursor, Limit: d.BatchSize,
	})
	if err != nil || len(rows) == 0 {
		// Release the lease so the next tick retries.
		_ = d.Queries.MarkBotWebhookDelivered(ctx, db.MarkBotWebhookDeliveredParams{ID: b.ID, Cursor: b.WebhookCursor})
		return
	}
	body := WebhookBody{
		BotID:       util.UUIDToString(b.ID),
		WorkspaceID: util.UUIDToString(b.WorkspaceID),
		Events:      make([]WebhookEvent, 0, len(rows)),
	}
	for _, e := range rows {
		body.Events = append(body.Events, WebhookEvent{
			ID: e.ID, Type: e.Type, ActorType: e.ActorType, ActorID: e.ActorID,
			Payload: json.RawMessage(e.Payload), CreatedAt: e.CreatedAt.Time,
		})
		body.NextCursor = e.ID
	}
	raw, _ := json.Marshal(body)
	if err := d.post(ctx, b, raw); err != nil {
		delay := backoff(b.WebhookFailures + 1)
		msg := err.Error()
		if len(msg) > 300 {
			msg = msg[:300]
		}
		_ = d.Queries.MarkBotWebhookFailed(ctx, db.MarkBotWebhookFailedParams{
			ID:            b.ID,
			NextAttemptAt: pgtype.Timestamptz{Time: time.Now().Add(delay), Valid: true},
			LastError:     msg,
		})
		return
	}
	_ = d.Queries.MarkBotWebhookDelivered(ctx, db.MarkBotWebhookDeliveredParams{ID: b.ID, Cursor: body.NextCursor})
}

func (d *Deliverer) post(ctx context.Context, b db.WorkspaceBot, raw []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.WebhookUrl, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	// The Bot's own headers go first so they cannot replace ours.
	var extra map[string]string
	_ = json.Unmarshal(b.WebhookHeaders, &extra)
	for name, value := range extra {
		req.Header.Set(name, value)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, Sign(b.WebhookSecret, raw))
	resp, err := d.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// backoff grows 2s, 4s, 8s ... capped at 5 minutes.
func backoff(failures int32) time.Duration {
	if failures > 8 {
		return 5 * time.Minute
	}
	d := time.Duration(1<<failures) * time.Second
	if d > 5*time.Minute {
		return 5 * time.Minute
	}
	return d
}
