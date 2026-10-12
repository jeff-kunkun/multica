package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// HumanNeededWorker pushes "this needs a person" to the person's Bot and
// chases it (DENE-1933). It is a separate outbox from the ordinary bot wake: a
// person's decision must not wait behind the 30s window, the hourly limit or
// the chain admission, and it must keep retrying until the Bot takes it.
//
// States: queued -> sending -> sent -> woken -> relayed -> answered, or
// expired / cancelled. Delivery is at-least-once; the Bot dedupes by event_id.
type HumanNeededWorker struct {
	h    *Handler
	wake *BotWakeWorker // only its secure HTTP delivery is reused
	done chan struct{}
}

func NewHumanNeededWorker(h *Handler, wake *BotWakeWorker) *HumanNeededWorker {
	return &HumanNeededWorker{h: h, wake: wake, done: make(chan struct{})}
}

func (w *HumanNeededWorker) Run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	sweep := time.NewTicker(10 * time.Second)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			w.Sweep(ctx)
		case <-ticker.C:
			for n := 0; n < 100; n++ {
				worked, err := w.ProcessNext(ctx)
				if err != nil {
					slog.Error("human needed processing failed", "error", err)
					break
				}
				if !worked {
					break
				}
			}
		}
	}
}

func (w *HumanNeededWorker) WaitWithTimeout(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-w.done:
		return true
	case <-t.C:
		return false
	}
}

// ProcessNext claims and pushes one due event.
func (w *HumanNeededWorker) ProcessNext(ctx context.Context) (bool, error) {
	ev, err := w.h.Queries.ClaimNextHumanNeededEvent(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	w.push(ctx, ev)
	return true, nil
}

// finish records the push result with the next backoff already chosen. It
// runs on its own context so a shutting-down server still records what the
// Bot was observed to do.
func (w *HumanNeededWorker) finish(ev db.HumanNeededEvent, delivered bool, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	retry := int32(service.HumanNeededBackoff(int(ev.Attempts) + 1).Seconds())
	if err := w.h.Queries.FinishHumanNeededDelivery(ctx, db.FinishHumanNeededDeliveryParams{
		Sequence: ev.Sequence, Delivered: delivered, Reason: reason, RetrySeconds: retry,
	}); err != nil {
		slog.Error("human needed: record delivery failed", "sequence", ev.Sequence, "error", err)
		return
	}
	if delivered {
		if ev.EventType == service.HumanEventNeeded {
			_ = w.h.Queries.ClearMemberBotBindingBroken(ctx, ev.BindingID)
		}
		return
	}
	// Three failed pushes in a row: put it in front of the person in the app
	// and mark the channel broken. Retries go on in the background.
	if ev.Attempts+1 == 3 && ev.EventType == service.HumanEventNeeded {
		w.markChannelBroken(ctx, ev.BindingID, ev.WorkspaceID, ev.TargetUserID)
		service.FallbackHumanNeededInApp(ctx, w.h.Queries, w.h.Bus, ev)
	}
}

// markChannelBroken flags the binding and tells the person once: only the
// healthy -> broken change writes the notice, however many calls or sweeps
// notice the same outage afterwards.
func (w *HumanNeededWorker) markChannelBroken(ctx context.Context, bindingID, workspaceID, userID pgtype.UUID) {
	changed, err := w.h.Queries.SetMemberBotBindingBroken(ctx, bindingID)
	if err != nil {
		slog.Warn("human needed: flag channel broken failed", "error", err)
		return
	}
	if changed > 0 {
		service.NotifyHumanChannelBroken(ctx, w.h.Queries, w.h.Bus, workspaceID, userID)
	}
}

func (w *HumanNeededWorker) cancel(ev db.HumanNeededEvent, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ev.EventType == service.HumanEventNeeded {
		service.FallbackHumanNeededInApp(ctx, w.h.Queries, w.h.Bus, ev)
	}
	if err := w.h.Queries.CancelHumanNeededEvent(ctx, db.CancelHumanNeededEventParams{EventID: ev.EventID, Reason: reason}); err != nil {
		slog.Error("human needed: cancel failed", "sequence", ev.Sequence, "error", err)
	}
}

func (w *HumanNeededWorker) push(ctx context.Context, ev db.HumanNeededEvent) {
	channel, err := w.h.Queries.GetHumanChannel(ctx, db.GetHumanChannelParams{WorkspaceID: ev.WorkspaceID, UserID: ev.TargetUserID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		w.finish(ev, false, "channel lookup failed")
		return
	}
	usable := err == nil && channel.ID == ev.BindingID
	if usable && ev.EventType == service.HumanEventVerify {
		usable = channel.BotActive && channel.WebhookConfigured
	} else if usable {
		usable = service.HumanChannelUsable(channel)
	}
	if !usable {
		w.cancel(ev, "channel unavailable")
		return
	}
	if ev.IssueID.Valid && !w.targetCanSeeIssue(ctx, ev) {
		w.cancel(ev, "access unavailable")
		return
	}
	wake, err := w.h.Queries.GetBotWake(ctx, ev.BotID)
	if err != nil || len(wake.WebhookSecret) == 0 || w.h.BotWakeSecrets == nil {
		w.finish(ev, false, "webhook not configured")
		return
	}
	plain, err := w.h.BotWakeSecrets.Open(wake.WebhookSecret)
	if err != nil {
		w.finish(ev, false, "webhook decryption failed")
		return
	}
	var secret botWakeSecrets
	if json.Unmarshal(plain, &secret) != nil {
		w.finish(ev, false, "invalid stored webhook")
		return
	}
	endpoint, err := validateWakeURL(ctx, secret.URL)
	if err != nil {
		w.finish(ev, false, "webhook endpoint unavailable")
		return
	}
	fields, err := w.h.humanNeededPayloadFields(ctx, ev, channel)
	if err != nil {
		w.finish(ev, false, "payload unavailable")
		return
	}
	payload, err := marshalBotWakeBatch(ev.EventID, []map[string]any{fields})
	if err != nil {
		w.finish(ev, false, "payload unavailable")
		return
	}
	status, reason := w.wake.deliver(ctx, endpoint, secret.Key, payload)
	w.finish(ev, status == "received", reason)
}

func (w *HumanNeededWorker) targetCanSeeIssue(ctx context.Context, ev db.HumanNeededEvent) bool {
	issue, err := w.h.Queries.GetIssue(ctx, ev.IssueID)
	if err != nil || issue.WorkspaceID != ev.WorkspaceID {
		return false
	}
	viewer, err := w.h.visibilityViewerForUser(ctx, ev.WorkspaceID, ev.TargetUserID)
	return err == nil && viewer.canSeeIssue(issue)
}

// humanNeededPayloadFields is the webhook body for one event. It keeps the
// envelope of the ordinary wake (event_id, type, summary, urls, author) and
// adds the human_needed block the Bot acts on.
func (h *Handler) humanNeededPayloadFields(ctx context.Context, ev db.HumanNeededEvent, channel db.GetHumanChannelRow) (map[string]any, error) {
	ws, err := h.Queries.GetWorkspace(ctx, ev.WorkspaceID)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(h.cfg.PublicURL, "/")
	receipts := base + "/api/workspaces/" + uuidToString(ev.WorkspaceID) + "/bots/" + uuidToString(ev.BotID) + "/wake"
	eventURL := receipts + "/events/" + uuidToString(ev.EventID)
	fields := map[string]any{
		"workspace_id": uuidToString(ev.WorkspaceID),
		"event_id":     uuidToString(ev.EventID),
		"sequence":     ev.Sequence,
		"type":         ev.EventType,
		"time":         ev.CreatedAt.Time,
		"woken_url":    eventURL + "/woken",
		"relayed_url":  eventURL + "/relayed",
		"answered_url": eventURL + "/answered",
		"pending_url":  base + "/api/workspaces/" + uuidToString(ev.WorkspaceID) + "/bots/" + uuidToString(ev.BotID) + "/human-needed",
		"ack_url":      eventURL + "/ack",
		"ack_body":     map[string]any{},
		"attachments":  []any{},
		"summary":      "binding.verify",
	}
	if ev.EventType == service.HumanEventVerify {
		fields["summary"] = "确认这个 Bot 能收到要人决定的事"
		fields["human_needed"] = map[string]any{"kind": "verify", "target_user_id": uuidToString(ev.TargetUserID)}
		return fields, nil
	}
	options := []service.HumanNeededOption{}
	_ = json.Unmarshal(ev.Options, &options)
	sourceURL := ""
	title := ""
	webPath := ""
	if ev.IssueID.Valid {
		issue, getErr := h.Queries.GetIssue(ctx, ev.IssueID)
		if getErr != nil {
			return nil, getErr
		}
		title = issue.Title
		webPath = "/" + url.PathEscape(ws.Slug) + "/issues/" + uuidToString(issue.ID)
		fields["issue_id"] = uuidToString(issue.ID)
		fields["issue"] = map[string]any{"id": uuidToString(issue.ID), "identifier": issueIdentifier(ws.IssuePrefix, issue.Number), "title": issue.Title}
		fields["url"] = base + "/api/issues/" + uuidToString(issue.ID)
	} else if ev.ChatSessionID.Valid {
		chat, getErr := h.Queries.GetChatSession(ctx, ev.ChatSessionID)
		if getErr != nil {
			return nil, getErr
		}
		title = chat.Title
		webPath = "/" + url.PathEscape(ws.Slug) + "/chat/" + uuidToString(chat.ID)
		fields["chat_session_id"] = uuidToString(chat.ID)
		fields["url"] = base + "/api/chat/sessions/" + uuidToString(chat.ID)
	}
	if webPath != "" {
		sourceURL = base + webPath
		fields["web_url"] = sourceURL
	}
	if ev.SourceCommentID.Valid {
		fields["source_id"] = uuidToString(ev.SourceCommentID)
	}
	fields["author"] = h.humanNeededAsker(ctx, ev)
	targetName := ""
	if u, getErr := h.Queries.GetUser(ctx, ev.TargetUserID); getErr == nil {
		targetName = u.Name
	}
	block := map[string]any{
		"kind":           ev.Kind,
		"target_user_id": uuidToString(ev.TargetUserID),
		"target_name":    targetName,
		"question":       ev.Question,
		"options":        options,
		"urgency":        ev.Urgency,
		"source_url":     sourceURL,
		"mode":           channel.Mode,
		"escalation":     ev.EventType == service.HumanEventEscalation,
	}
	if ev.Deadline.Valid {
		block["deadline"] = ev.Deadline.Time
	} else {
		block["deadline"] = nil
	}
	if ev.ParentEventID.Valid {
		block["parent_event_id"] = uuidToString(ev.ParentEventID)
	}
	if ev.AskID.Valid {
		if card := h.askCardForPayload(ctx, ev); card != nil {
			block["ask"] = card
		}
	}
	fields["human_needed"] = block
	lead := "要你决定"
	if ev.EventType == service.HumanEventEscalation {
		lead = "还没转达给你，请尽快看"
	}
	summary := fmt.Sprintf("%s：%s", lead, ev.Question)
	if title != "" {
		summary = fmt.Sprintf("%s（%s）", summary, title)
	}
	if runes := []rune(summary); len(runes) > 180 {
		summary = string(runes[:180])
	}
	fields["summary"] = summary
	return fields, nil
}

func (h *Handler) humanNeededAsker(ctx context.Context, ev db.HumanNeededEvent) map[string]any {
	a := map[string]any{"type": ev.CallerType, "id": uuidToString(ev.CallerID)}
	if !ev.CallerID.Valid || ev.CallerType == "system" {
		return map[string]any{"type": ev.CallerType, "id": ""}
	}
	switch ev.CallerType {
	case "member":
		if u, err := h.Queries.GetUser(ctx, ev.CallerID); err == nil {
			a["name"] = u.Name
		}
	case "agent":
		if ag, err := h.Queries.GetAgent(ctx, ev.CallerID); err == nil && ag.WorkspaceID == ev.WorkspaceID {
			a["name"] = ag.Name
		}
	}
	return a
}

// Sweep is the chase: recover interrupted pushes, push again when the Bot
// never woke, escalate when it woke but never relayed, nudge every 30 minutes
// after that, and move people off a channel that went bad.
func (w *HumanNeededWorker) Sweep(ctx context.Context) {
	q := w.h.Queries
	if err := q.RecoverStaleHumanNeededSending(ctx); err != nil {
		slog.Error("human needed: recover failed", "error", err)
	}
	if err := q.ExpireHumanNeeded(ctx); err != nil {
		slog.Error("human needed: expire failed", "error", err)
	}
	if _, err := q.ResendUnwokenHumanNeeded(ctx); err != nil {
		slog.Error("human needed: resend failed", "error", err)
	}
	w.escalate(ctx)
	w.nudge(ctx)
	w.checkChannels(ctx)
}

func (w *HumanNeededWorker) escalate(ctx context.Context) {
	q := w.h.Queries
	due, err := q.ListHumanNeededToEscalate(ctx)
	if err != nil {
		slog.Error("human needed: list escalations failed", "error", err)
		return
	}
	for _, ev := range due {
		// Mark first: a crash after this leaves a missed escalation row rather
		// than a loop of them.
		if err := q.MarkHumanNeededEscalated(ctx, ev.EventID); err != nil {
			slog.Error("human needed: mark escalated failed", "error", err)
			continue
		}
		if _, err := service.CreateHumanNeededFollowUp(ctx, q, ev, "escalation:"+uuidToString(ev.EventID)); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("human needed: escalation write failed", "error", err)
		}
		service.FallbackHumanNeededInApp(ctx, q, w.h.Bus, ev)
		w.remindOwners(ctx, ev)
	}
}

func (w *HumanNeededWorker) nudge(ctx context.Context) {
	q := w.h.Queries
	due, err := q.ListHumanNeededToNudge(ctx)
	if err != nil {
		slog.Error("human needed: list nudges failed", "error", err)
		return
	}
	for _, ev := range due {
		if err := q.TouchHumanNeededNudge(ctx, ev.EventID); err != nil {
			continue
		}
		key := "nudge:" + uuidToString(ev.EventID) + ":" + strconv.FormatInt(time.Now().Unix(), 10)
		if _, err := service.CreateHumanNeededFollowUp(ctx, q, ev, key); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("human needed: nudge write failed", "error", err)
		}
	}
}

// remindOwners tells the workspace owners that a person's Bot has not relayed
// a call, so someone can reach the person another way. The person asked is
// never reminded here: the in-app fallback already reaches them.
func (w *HumanNeededWorker) remindOwners(ctx context.Context, ev db.HumanNeededEvent) {
	if !ev.IssueID.Valid {
		return
	}
	q := w.h.Queries
	issue, err := q.GetIssue(ctx, ev.IssueID)
	if err != nil {
		return
	}
	owners, err := q.ListWorkspaceOwnerIDs(ctx, ev.WorkspaceID)
	if err != nil {
		return
	}
	name := "对方"
	if u, getErr := q.GetUser(ctx, ev.TargetUserID); getErr == nil && u.Name != "" {
		name = u.Name
	}
	for _, owner := range owners {
		if owner == ev.TargetUserID {
			continue
		}
		item, createErr := q.CreateInboxItem(ctx, db.CreateInboxItemParams{
			ID:            dbid.NewV7(),
			WorkspaceID:   ev.WorkspaceID,
			RecipientType: "member",
			RecipientID:   owner,
			Type:          service.InboxTypeNeedsYou,
			Severity:      "action_required",
			IssueID:       ev.IssueID,
			Title:         issue.Title,
			Body:          pgtype.Text{String: name + " 的 Bot 还没把这件事转达给 " + name + "：" + ev.Question, Valid: true},
			ActorType:     pgtype.Text{String: "system", Valid: true},
			Details:       []byte(`{"human_needed":"bot_not_relayed"}`),
		})
		if createErr != nil {
			slog.Warn("human needed: owner reminder failed", "error", createErr)
			continue
		}
		service.PublishInboxNew(w.h.Bus, item, issue.Status)
	}
}

// checkChannels moves people off a Bot that went bad: the open calls fall
// back to the app, and the channel is flagged so the settings page can say so.
// A recovered Bot clears the flag.
func (w *HumanNeededWorker) checkChannels(ctx context.Context) {
	q := w.h.Queries
	rows, err := q.ListHumanChannelHealth(ctx)
	if err != nil {
		slog.Error("human needed: channel health failed", "error", err)
		return
	}
	for _, row := range rows {
		healthy := row.BotActive && row.WebhookConfigured
		if !healthy {
			// Flag and tell the person once, but always move the open calls:
			// three failed pushes may have flagged the channel already, and the
			// calls that were still waiting must not be stranded behind that.
			w.markChannelBroken(ctx, row.ID, row.WorkspaceID, row.UserID)
			open, listErr := q.ListOpenHumanNeededForBinding(ctx, row.ID)
			if listErr != nil {
				continue
			}
			for _, ev := range open {
				w.cancel(ev, "channel broken")
			}
			continue
		}
		if !row.BrokenNotifiedAt.Valid {
			continue
		}
		failing, countErr := q.CountFailingHumanNeededForBinding(ctx, row.ID)
		if countErr == nil && failing == 0 {
			_ = q.ClearMemberBotBindingBroken(ctx, row.ID)
		}
	}
}
