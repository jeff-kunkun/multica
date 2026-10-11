package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

type BotWakeWorker struct {
	h    *Handler
	done chan struct{}
	// Only tests replace this factory; production always uses the DNS-pinned,
	// no-proxy, no-redirect public HTTPS transport.
	client func(*url.URL) *http.Client
}

func NewBotWakeWorker(h *Handler) *BotWakeWorker {
	return &BotWakeWorker{h: h, done: make(chan struct{}), client: remotemcp.NewSecureHTTPClient}
}
func (w *BotWakeWorker) Run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	maintenance := time.NewTicker(time.Minute)
	defer maintenance.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-maintenance.C:
			if err := w.h.Queries.RecoverBotWakeUncertain(ctx); err != nil {
				slog.Error("bot wake recovery failed")
			}
			if err := w.h.Queries.PruneBotWakeAttempts(ctx); err != nil {
				slog.Error("bot wake attempt cleanup failed")
			}
		case <-ticker.C:
			for n := 0; n < 100; n++ {
				worked, err := w.ProcessNext(ctx)
				if err != nil {
					slog.Error("bot wake processing failed")
					break
				}
				if !worked {
					break
				}
			}
		}
	}
}
func (w *BotWakeWorker) WaitWithTimeout(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-w.done:
		return true
	case <-t.C:
		return false
	}
}
func (w *BotWakeWorker) ProcessNext(ctx context.Context) (bool, error) {
	starter, ok := w.h.DB.(txStarter)
	if !ok {
		return false, errors.New("wake database unavailable")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	q := w.h.Queries.WithTx(tx)
	e, err := q.NextBotWakeEvent(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	c, err := q.LockBotWake(ctx, e.BotID)
	if err != nil {
		return false, err
	}
	bot, err := q.GetWakeBot(ctx, db.GetWakeBotParams{ID: e.BotID, WorkspaceID: e.WorkspaceID})
	if err != nil {
		return false, err
	}
	pending := func(reason string) (bool, error) {
		// Temporary policy blocks are revisited without replaying uncertain sends.
		delay := int32(0)
		if reason == "hourly limit" || reason == "quiet hours" {
			delay = 1
		}
		if err := q.FinishBotWakeEvent(ctx, db.FinishBotWakeEventParams{Sequence: e.Sequence, Status: "pending", Reason: reason, DelaySeconds: delay}); err != nil {
			return true, err
		}
		return true, tx.Commit(ctx)
	}
	if bot.DeletedAt.Valid || bot.Revoked || bot.ExpiresAt.Valid && !bot.ExpiresAt.Time.After(time.Now()) {
		return pending("bot inactive")
	}
	if !w.h.wakeVisible(ctx, e, bot.UserID) {
		return pending("access unavailable")
	}
	alert := e.EventType == "wake_blocked"
	rules := wakeResponse(c)
	// Some issue writes do not set transaction-local actor metadata. Recover
	// an actual run's actor where available; never impersonate an issue owner.
	if !alert && (e.AuthorType == "system" || !e.AuthorID.Valid) && e.SourceTaskID.Valid {
		if task, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: e.SourceTaskID, WorkspaceID: e.WorkspaceID}); err == nil {
			e.AuthorType, e.AuthorID = "agent", task.AgentID
			if err := q.RestoreBotWakeAuthor(ctx, db.RestoreBotWakeAuthorParams{Sequence: e.Sequence, AuthorID: e.AuthorID}); err != nil {
				return true, err
			}
		}
	}
	if !alert && e.AuthorType == "bot" && e.AuthorID == e.BotID {
		return pending("self wake suppressed")
	}
	if e.InheritedOwner {
		if !rules.InheritOwnerFollows {
			return pending("owner inheritance disabled")
		}
		follows, err := q.BotOwnerFollowsIssue(ctx, db.BotOwnerFollowsIssueParams{ID: e.IssueID, AssigneeID: bot.UserID})
		if err != nil {
			return true, err
		}
		if !follows.Bool {
			return pending("owner no longer follows source")
		}
	}
	if !alert && e.AuthorType != "system" && e.AuthorID.Valid {
		rule, known := rules.WakeFrom[e.AuthorType]
		if !known || rule.Mode == "none" || (rule.Mode == "list" && !slices.Contains(rule.IDs, uuidToString(e.AuthorID))) {
			return pending("sender not allowed")
		}
	}

	if !alert && !slices.Contains(rules.Events, e.EventType) {
		return pending("event disabled")
	}
	if !alert && wakeQuiet(rules.QuietHours, time.Now()) {
		return pending("quiet hours")
	}
	count, err := q.CountRecentBotWakeAttempts(ctx, e.BotID)
	if err != nil {
		return true, err
	}
	if !alert && (rules.HourlyLimit == 0 || (e.EventType != "mention" && count >= int64(rules.HourlyLimit))) {
		return pending("hourly limit")
	}
	if len(c.WebhookSecret) == 0 {
		return pending("webhook not configured")
	}
	if w.h.BotWakeSecrets == nil {
		return pending("webhook encryption unavailable")
	}
	plain, err := w.h.BotWakeSecrets.Open(c.WebhookSecret)
	if err != nil {
		return pending("webhook decryption failed")
	}
	var secret botWakeSecrets
	if json.Unmarshal(plain, &secret) != nil {
		return pending("invalid stored webhook")
	}
	endpoint, err := validateWakeURL(ctx, secret.URL)
	if err != nil {
		return pending("webhook endpoint unavailable")
	}
	workspace, err := q.GetWorkspace(ctx, e.WorkspaceID)
	if err != nil {
		return true, err
	}
	var payload []byte
	if !e.RootID.Valid {
		return pending("source lineage unavailable")
	}
	if e.Attempts == 0 && !alert {
		if err = q.EnsureBotWakeChain(ctx, db.EnsureBotWakeChainParams{RootID: e.RootID, WorkspaceID: e.WorkspaceID}); err != nil {
			return true, err
		}
		hop, hopErr := q.ConsumeBotWakeHop(ctx, db.ConsumeBotWakeHopParams{RootID: e.RootID, WorkspaceID: e.WorkspaceID})
		if errors.Is(hopErr, pgx.ErrNoRows) {
			limit := service.BotWakeChainLimit(workspace.Settings)
			reason := fmt.Sprintf("bot wake chain limit (%d)", limit)
			origin := e.ViaBotID
			if !origin.Valid {
				origin = e.BotID
			}
			if err := q.RecordBotWakeDeliveryBlock(ctx, db.RecordBotWakeDeliveryBlockParams{WorkspaceID: e.WorkspaceID, OriginBotID: origin, TargetBotID: e.BotID, RootID: e.RootID, IssueID: e.IssueID, ChatSessionID: e.ChatSessionID, LimitValue: int32(limit), SourceID: e.SourceID, Reason: reason}); err != nil {
				return true, err
			}
			return pending(reason)
		}
		if hopErr != nil {
			return true, hopErr
		}
		e.Hop = hop
	}
	generation := e.WakeGeneration
	if e.Attempts == 0 && !alert {
		generation, err = q.GetBotWakeChainGeneration(ctx, e.RootID)
		if err != nil {
			return true, err
		}
	}
	if !alert {
		if err = q.RememberBotWakeCause(ctx, db.RememberBotWakeCauseParams{BotID: e.BotID, RootID: e.RootID, Hop: e.Hop}); err != nil {
			return true, err
		}
	}
	payload, err = w.h.botWakePayload(ctx, e)
	if err != nil {
		return true, err
	}
	if err = q.MarkBotWakeSending(ctx, db.MarkBotWakeSendingParams{Sequence: e.Sequence, Hop: e.Hop, WakeGeneration: generation}); err != nil {
		return true, err
	}
	// Commit BEFORE the network write. A crash after this point must never
	// replay blindly; recovery changes stale sending to uncertain.
	if err = tx.Commit(ctx); err != nil {
		return true, err
	}
	status, reason := w.deliver(ctx, endpoint, secret.Key, payload)
	delay := int32(0)
	if status == "retry" {
		if e.Attempts+1 >= 3 {
			status = "failed"
			reason += "; retries exhausted"
		} else {
			status = "queued"
			delay = int32(5 * (1 << uint(e.Attempts)))
		}
	}
	// A canceled request/server context must still record the observed result.
	recordCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !alert && status == "failed" && strings.HasPrefix(reason, "connection failed before send") {
		if err := w.h.Queries.RefundBotWakeHop(recordCtx, db.RefundBotWakeHopParams{RootID: e.RootID, Generation: generation}); err != nil {
			return true, err
		}
	}
	return true, w.h.Queries.FinishBotWakeEvent(recordCtx, db.FinishBotWakeEventParams{Sequence: e.Sequence, Status: status, Reason: reason, DelaySeconds: delay})
}
func (w *BotWakeWorker) deliver(ctx context.Context, endpoint *url.URL, key string, payload []byte) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var started atomic.Bool
	// WroteHeaders fires before request bytes may reach the receiver. Any later
	// network error (including timeout) is conservatively uncertain.
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteHeaders: func() { started.Store(true) }})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return "failed", "invalid webhook request"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", key) // exact receiver-provided Authorization value
	var identity struct {
		EventID string `json:"event_id"`
	}
	_ = json.Unmarshal(payload, &identity)
	req.Header.Set("X-Multica-Event-ID", identity.EventID)
	client := w.client(endpoint)
	defer client.CloseIdleConnections()
	// Never let a test/client redirect silently forward credentials either.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := copyClient.Do(req)
	if err != nil {
		if started.Load() {
			return "uncertain", "request sent; receipt unknown"
		}
		return "retry", "connection failed before send"
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode == http.StatusOK {
		return "received", "HTTP 200; execution not confirmed"
	}
	if res.StatusCode == 429 || res.StatusCode == 503 {
		return "retry", fmt.Sprintf("HTTP %d", res.StatusCode)
	}
	// A server error can follow a side effect. Retrying all 5xx risks two wakes.
	if res.StatusCode >= 500 {
		return "uncertain", fmt.Sprintf("HTTP %d; receipt unknown", res.StatusCode)
	}
	return "failed", fmt.Sprintf("HTTP %d", res.StatusCode)
}
func (h *Handler) botWakePayload(ctx context.Context, e db.BotWakeEvent) ([]byte, error) {
	summary := e.EventType
	link := ""
	webPath := ""
	ws, err := h.Queries.GetWorkspace(ctx, e.WorkspaceID)
	if err != nil {
		return nil, err
	}
	attachments := []map[string]any{}
	var files []db.Attachment
	if e.IssueID.Valid {
		issue, getErr := h.Queries.GetIssue(ctx, e.IssueID)
		if getErr != nil {
			return nil, getErr
		}
		summary = e.EventType + ": " + issue.Title
		link = "/api/issues/" + uuidToString(e.IssueID)
		webPath = "/" + url.PathEscape(ws.Slug) + "/issues/" + uuidToString(e.IssueID)
		files, err = h.Queries.ListAttachmentsByIssue(ctx, db.ListAttachmentsByIssueParams{IssueID: e.IssueID, WorkspaceID: e.WorkspaceID})
	} else {
		chat, getErr := h.Queries.GetChatSession(ctx, e.ChatSessionID)
		if getErr != nil {
			return nil, getErr
		}
		summary = e.EventType + ": " + chat.Title
		link = "/api/chat/sessions/" + uuidToString(e.ChatSessionID)
		webPath = "/" + url.PathEscape(ws.Slug) + "/chat/" + uuidToString(e.ChatSessionID)
		files, err = h.Queries.ListAttachmentsByChatMessage(ctx, db.ListAttachmentsByChatMessageParams{ChatMessageID: e.SourceID, WorkspaceID: e.WorkspaceID})
	}
	if err != nil {
		return nil, err
	}
	if e.EventType == "wake_blocked" {
		summary = fmt.Sprintf("%s: %s", summary, e.Reason)
	}
	if runes := []rune(summary); len(runes) > 180 {
		summary = string(runes[:180])
	}
	base := strings.TrimRight(h.cfg.PublicURL, "/")
	for _, a := range files {
		if !a.Sha256.Valid {
			continue
		} // legacy bytes must be verified/backfilled first
		if e.SourceID != e.IssueID && e.IssueID.Valid && a.CommentID != e.SourceID {
			continue
		}
		attachments = append(attachments, map[string]any{"url": base + "/api/attachments/" + uuidToString(a.ID) + "/download", "size_bytes": a.SizeBytes, "sha256": a.Sha256.String})
	}
	receiptBase := base + "/api/workspaces/" + uuidToString(e.WorkspaceID) + "/bots/" + uuidToString(e.BotID) + "/wake"
	payload := map[string]any{"event_id": uuidToString(e.EventID), "sequence": e.Sequence, "source_id": uuidToString(e.SourceID), "parent_id": wakeParentID(e), "ack_url": receiptBase + "/pending/ack", "ack_body": map[string]any{"event_ids": []string{uuidToString(e.EventID)}}, "woken_url": receiptBase + "/events/" + uuidToString(e.EventID) + "/woken", "result_url": receiptBase + "/events/" + uuidToString(e.EventID) + "/result", "root_id": uuidToString(e.RootID), "hop": e.Hop, "source_task_id": uuidToString(e.SourceTaskID), "web_url": base + webPath, "type": e.EventType, "issue_id": uuidToString(e.IssueID), "chat_session_id": uuidToString(e.ChatSessionID), "author": h.wakeAuthor(ctx, e), "time": e.CreatedAt.Time, "summary": summary, "url": base + link, "attachments": attachments}
	if e.EventType == "wake_blocked" {
		payload["reason_code"] = "bot_wake_chain_limit"
		payload["reason"] = e.Reason
		payload["limit"] = e.Hop
		if e.AuthorType == "bot" {
			payload["target_bot_id"] = uuidToString(e.AuthorID)
		} else {
			payload["agent_id"] = uuidToString(e.AuthorID)
		}
		payload["recovery"] = "Wait for an agent reply or completed run, or send a human message. A workspace admin can change the limit."
	}
	return json.Marshal(payload)
}

func wakeParentID(e db.BotWakeEvent) string {
	if e.IssueID.Valid {
		return uuidToString(e.IssueID)
	}
	return uuidToString(e.ChatSessionID)
}

// Names are disclosed only when the receiver can see the named principal.
func (h *Handler) wakeAuthor(ctx context.Context, e db.BotWakeEvent) map[string]any {
	a := map[string]any{"type": e.AuthorType, "id": uuidToString(e.AuthorID)}
	if e.AuthorType == "system" || !e.AuthorID.Valid {
		return map[string]any{"type": "", "id": "", "note": "source attribution unavailable"}
	}
	if e.AuthorType == "bot" {
		a["name"] = e.ViaBotName.String
	} else if e.AuthorType == "member" {
		if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: e.AuthorID, WorkspaceID: e.WorkspaceID}); err == nil {
			if u, err := h.Queries.GetUser(ctx, e.AuthorID); err == nil {
				a["name"] = u.Name
			} else {
				a["id"] = ""
			}
		} else {
			a["id"] = ""
		}
	} else if e.AuthorType == "agent" {
		bot, err := h.Queries.GetWakeBot(ctx, db.GetWakeBotParams{ID: e.BotID, WorkspaceID: e.WorkspaceID})
		agent, agentErr := h.Queries.GetAgent(ctx, e.AuthorID)
		if err == nil && agentErr == nil && agent.WorkspaceID == e.WorkspaceID && h.canAccessPrivateAgent(ctx, agent, "member", uuidToString(bot.UserID), uuidToString(e.WorkspaceID)) {
			a["name"] = agent.Name
		} else {
			a["id"] = ""
		}
	}
	if e.ViaBotID.Valid {
		a["via_bot_id"] = uuidToString(e.ViaBotID)
		a["via_bot_name"] = e.ViaBotName.String
	}
	return a
}
