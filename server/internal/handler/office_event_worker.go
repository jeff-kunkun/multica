package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dbactor"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// OfficeEventWorker turns the office event stream (DENE-1963) into deliveries.
//
// Triggers write a row in the same transaction as the business write, so what
// committed is in the table even if this process died right after. The worker:
//
//  1. seals new rows: reads what they mean (service.ClassifyOffice), gives
//     each a gap-free stream_seq under one lock, and writes one delivery per
//     Bot that should get it, in the same transaction;
//  2. pushes due deliveries to the Bot's webhook with the event_id the Bot
//     acknowledges by. HTTP 200 is not a receipt: until the Bot posts one the
//     same event is resent with backoff, forever. A Bot that lost permission,
//     or whose channel is gone, is not sent anything: the row is blocked with
//     the reason, and comes back with a retry once fixed;
//  3. puts a result that must reach the person and did not (no "relayed"
//     receipt in time, or blocked) in front of them in the app, once.
type OfficeEventWorker struct {
	h    *Handler
	wake *BotWakeWorker // only its secure HTTP delivery is reused
	done chan struct{}
}

const (
	officeSealBatch     = 200
	officeDeliverBatch  = 20
	officeFallbackGrace = 5 * time.Minute
	// officeOwnerDepth bounds the walk up parent tickets for an owner.
	officeOwnerDepth = 10
)

func NewOfficeEventWorker(h *Handler, wake *BotWakeWorker) *OfficeEventWorker {
	return &OfficeEventWorker{h: h, wake: wake, done: make(chan struct{})}
}

func (w *OfficeEventWorker) Run(ctx context.Context) {
	defer close(w.done)
	ctx = dbactor.System(ctx, "office_event")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	sweep := time.NewTicker(30 * time.Second)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			w.Sweep(ctx)
		case <-ticker.C:
			w.Tick(ctx)
		}
	}
}

func (w *OfficeEventWorker) WaitWithTimeout(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-w.done:
		return true
	case <-t.C:
		return false
	}
}

// Tick seals what is new and pushes what is due.
func (w *OfficeEventWorker) Tick(ctx context.Context) {
	for i := 0; i < 10; i++ {
		n, err := w.Seal(ctx)
		if err != nil {
			slog.Error("office events: seal failed", "error", err)
			break
		}
		if n < officeSealBatch {
			break
		}
	}
	for i := 0; i < 5; i++ {
		if w.DeliverDue(ctx) < officeDeliverBatch {
			break
		}
	}
}

// Sweep recovers claims a crash left behind and raises in-app fallbacks.
func (w *OfficeEventWorker) Sweep(ctx context.Context) {
	if err := w.h.Queries.RequeueStuckOfficeDeliveries(ctx); err != nil {
		slog.Warn("office events: requeue failed", "error", err)
	}
	w.Restore(ctx)
	w.Fallback(ctx)
}

// Seal handles one batch of new rows. It returns how many it sealed.
func (w *OfficeEventWorker) Seal(ctx context.Context) (int, error) {
	tx, err := w.h.TxStarter.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := w.h.Queries.WithTx(tx)
	if err := q.LockOfficeEventSealer(ctx); err != nil {
		return 0, err
	}
	rows, err := q.ListUnsealedOfficeEvents(ctx, officeSealBatch)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	seq, err := q.NextOfficeEventSeq(ctx)
	if err != nil {
		return 0, err
	}
	for _, ev := range rows {
		facts := w.facts(ctx, ev)
		class := service.ClassifyOffice(facts)
		// A lookup that failed is not "nobody": the whole batch waits for the
		// next tick rather than sealing an event with recipients missing.
		recipients, gap, unreached, err := w.recipients(ctx, ev, class)
		if err != nil {
			return 0, fmt.Errorf("recipients of %s: %w", uuidToString(ev.EventID), err)
		}
		if err := q.SealOfficeEvent(ctx, db.SealOfficeEventParams{
			ID: ev.ID, StreamSeq: pgtype.Int8{Int64: seq, Valid: true},
			Meaning: class.Meaning, Summary: class.Summary, MustRelay: class.MustRelay,
			PendingApproval: class.PendingApproval, BusinessKey: officeFallbackBusinessKey(ev),
			RecipientGap: gap,
		}); err != nil {
			return 0, err
		}
		for _, user := range unreached {
			if err := q.CreateOfficeEventGap(ctx, db.CreateOfficeEventGapParams{
				EventID: ev.EventID, WorkspaceID: ev.WorkspaceID, UserID: user, Reason: officeGapNoBot,
			}); err != nil {
				return 0, err
			}
		}
		seq++
		for _, r := range recipients {
			status := "pending"
			if r.blocked != "" {
				status = "blocked"
			}
			if err := q.CreateOfficeEventDelivery(ctx, db.CreateOfficeEventDeliveryParams{
				EventID: ev.EventID, WorkspaceID: ev.WorkspaceID, BotID: r.binding.BotID,
				BindingID: r.binding.ID, TargetUserID: r.binding.UserID, Reason: r.reason,
				MustRelay: class.MustRelay && r.reason == "owner", Status: status, BlockedReason: r.blocked,
			}); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func officeFallbackBusinessKey(ev db.OfficeEvent) string {
	return "office:" + uuidToString(ev.EventID)
}

func (w *OfficeEventWorker) facts(ctx context.Context, ev db.OfficeEvent) service.OfficeFacts {
	f := service.OfficeFacts{
		Family: ev.Family, EventType: ev.EventType, Changes: service.ParseOfficeChanges(ev.Changes),
		ActorType: ev.ActorType, ActorSource: ev.ActorSource,
	}
	if ev.IssueID.Valid {
		if issue, err := w.h.Queries.GetIssue(ctx, ev.IssueID); err == nil {
			f.IssueTitle, f.IssueDescription = issue.Title, issue.Description.String
			if ws, err := w.h.Queries.GetWorkspace(ctx, issue.WorkspaceID); err == nil {
				f.IssueKey = issueIdentifier(ws.IssuePrefix, issue.Number)
			}
		}
	}
	if ev.ChatSessionID.Valid {
		if chat, err := w.h.Queries.GetChatSession(ctx, ev.ChatSessionID); err == nil {
			f.ChatTitle = chat.Title
		}
	}
	if ev.AttachmentID.Valid {
		if a, err := w.h.Queries.GetAttachment(ctx, db.GetAttachmentParams{ID: ev.AttachmentID, WorkspaceID: ev.WorkspaceID}); err == nil {
			f.UploaderType, f.Filename = a.UploaderType, a.Filename
		}
	}
	return f
}

type officeRecipient struct {
	binding db.MemberBotBinding
	reason  string // owner | stream
	blocked string // set when the owner's Bot cannot take it yet
}

// Recipient gaps, recorded on the event when a result that must reach
// someone cannot reach (all of) them through a Bot. Each owner without a Bot
// also gets an office_event_gap row: reminder in the app, delivery on
// reconnect.
const (
	officeGapNoOwner = "没有可通知的主人：这条结果不属于任何人的任务或聊天"
	officeGapNoBot   = "主人没有连接 Bot，结果只在应用里"
)

// recipients is who the event is for, decided when it is sealed. Owners (the
// person whose chat or ticket it is) get results and run endings whether or
// not they stream everything; a person who streams everything gets what they
// can see. Nobody is told about their own action; an agent's result is never
// treated as the person's own, even when their Bot started the run.
//
// An owner whose Bot is connected but not verified still gets a row, blocked
// with the reason, so the result is not lost: it goes to their inbox and
// comes back once the Bot is verified. Owners of a must-relay result with no
// Bot at all are returned as unreached, one by one, so one owner's Bot never
// hides another's gap. Any lookup that fails returns the error; the caller
// must not seal on a partial answer.
func (w *OfficeEventWorker) recipients(ctx context.Context, ev db.OfficeEvent, class service.OfficeClass) ([]officeRecipient, string, []pgtype.UUID, error) {
	var unreached []pgtype.UUID
	seen := map[string]bool{}
	var out []officeRecipient
	add := func(r officeRecipient) error {
		key := uuidToString(r.binding.BotID)
		if seen[key] || w.ownAction(ev, r.binding.UserID) {
			return nil
		}
		visible, err := w.h.officeEventVisibleErr(ctx, ev, r.binding.UserID)
		if err != nil || !visible {
			return err
		}
		seen[key] = true
		out = append(out, r)
		return nil
	}
	gap := ""
	ownerWorthy := class.MustRelay || class.Meaning == service.OfficeMeaningRoundDone || class.Meaning == service.OfficeMeaningFailed
	if ownerWorthy {
		owners, err := w.owners(ctx, ev)
		if err != nil {
			return nil, "", nil, err
		}
		for _, user := range owners {
			if w.ownAction(ev, user) {
				continue // they did it; nobody is missing
			}
			b, err := w.h.Queries.GetOfficeBindingForUser(ctx, db.GetOfficeBindingForUserParams{WorkspaceID: ev.WorkspaceID, UserID: user})
			if errors.Is(err, pgx.ErrNoRows) {
				if !class.MustRelay {
					continue
				}
				visible, err := w.h.officeEventVisibleErr(ctx, ev, user)
				if err != nil {
					return nil, "", nil, err
				}
				if visible {
					unreached = append(unreached, user)
				}
				continue
			}
			if err != nil {
				return nil, "", nil, err
			}
			r := officeRecipient{binding: b, reason: "owner"}
			if !b.VerifiedAt.Valid {
				r.blocked = officeBlockedChannel
			}
			if err := add(r); err != nil {
				return nil, "", nil, err
			}
		}
		if class.MustRelay {
			switch {
			case len(owners) == 0:
				gap = officeGapNoOwner
			case len(unreached) > 0:
				gap = officeGapNoBot
			}
		}
	}
	stream, err := w.h.Queries.ListOfficeStreamBindings(ctx, ev.WorkspaceID)
	if err != nil {
		return nil, "", nil, err
	}
	for _, b := range stream {
		if err := add(officeRecipient{binding: b, reason: "stream"}); err != nil {
			return nil, "", nil, err
		}
	}
	return out, gap, unreached, nil
}

func (w *OfficeEventWorker) ownAction(ev db.OfficeEvent, user pgtype.UUID) bool {
	return ev.ActorType == "member" && ev.ActorID.Valid && ev.ActorID == user
}

// owners are the people a result belongs to: the chat's opener; the ticket's
// creator, its assignee, and the opener of the chat it came from. A ticket
// that names nobody (an agent split it off and ran it) belongs to whoever owns
// the nearest ticket above it: an agent's sub-ticket is still the person's
// work. Up the chain only people who can open this event's ticket count, so a
// private sub-ticket never reaches a parent owner it is hidden from; the walk
// stops at a missing parent, another workspace, a loop or officeOwnerDepth. A
// row that no longer exists names nobody; any other failure is returned.
func (w *OfficeEventWorker) owners(ctx context.Context, ev db.OfficeEvent) ([]pgtype.UUID, error) {
	var out []pgtype.UUID
	seen := map[pgtype.UUID]bool{}
	var level []pgtype.UUID
	push := func(u pgtype.UUID) {
		if u.Valid && !seen[u] {
			seen[u] = true
			level = append(level, u)
		}
	}
	chatOpener := func(id pgtype.UUID) error {
		chat, err := w.h.Queries.GetChatSession(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err == nil {
			push(chat.CreatorID)
		}
		return err
	}
	if ev.ChatSessionID.Valid {
		if err := chatOpener(ev.ChatSessionID); err != nil {
			return nil, err
		}
	}
	visited := map[pgtype.UUID]bool{}
	next := ev.IssueID
	for depth := 0; next.Valid && !visited[next] && depth <= officeOwnerDepth; depth++ {
		visited[next] = true
		issue, err := w.h.Queries.GetIssue(ctx, next)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		if issue.WorkspaceID != ev.WorkspaceID {
			break
		}
		if issue.CreatorType == "member" {
			push(issue.CreatorID)
		}
		if issue.AssigneeType.String == "member" && issue.AssigneeID.Valid {
			push(issue.AssigneeID)
		}
		if issue.OriginChatSessionID.Valid {
			if err := chatOpener(issue.OriginChatSessionID); err != nil {
				return nil, err
			}
		}
		for _, u := range level {
			if depth > 0 {
				visible, err := w.h.officeEventVisibleErr(ctx, ev, u)
				if err != nil {
					return nil, err
				}
				if !visible {
					continue
				}
			}
			out = append(out, u)
		}
		level = nil
		if len(out) > 0 {
			return out, nil
		}
		next = issue.ParentIssueID
	}
	return append(out, level...), nil
}

// canSee is the one access rule for push and pull: settings and members are
// for owners and admins; a ticket's events for whoever can open the ticket; a
// chat's for its opener. Anything that names neither is nobody's.
func (w *OfficeEventWorker) canSee(ctx context.Context, ev db.OfficeEvent, user pgtype.UUID) bool {
	return w.h.officeEventVisible(ctx, ev, user)
}

func (h *Handler) officeEventVisible(ctx context.Context, ev db.OfficeEvent, user pgtype.UUID) bool {
	ok, err := h.officeEventVisibleErr(ctx, ev, user)
	return err == nil && ok
}

// officeEventVisibleErr tells "cannot see" (false) from "could not check"
// (error), for callers that must not mistake one for the other.
func (h *Handler) officeEventVisibleErr(ctx context.Context, ev db.OfficeEvent, user pgtype.UUID) (bool, error) {
	gone := func(err error) (bool, error) {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: user, WorkspaceID: ev.WorkspaceID})
	if err != nil {
		return gone(err)
	}
	if ev.Family == "config" {
		return member.Role == "owner" || member.Role == "admin", nil
	}
	if ev.IssueID.Valid {
		issue, err := h.Queries.GetIssue(ctx, ev.IssueID)
		if err != nil {
			return gone(err)
		}
		if issue.WorkspaceID != ev.WorkspaceID {
			return false, nil
		}
		viewer, err := h.visibilityViewerForUser(ctx, ev.WorkspaceID, user)
		if err != nil {
			return gone(err)
		}
		return viewer.canSeeIssue(issue), nil
	}
	if ev.ChatSessionID.Valid {
		chat, err := h.Queries.GetChatSession(ctx, ev.ChatSessionID)
		if err != nil {
			return gone(err)
		}
		return chat.WorkspaceID == ev.WorkspaceID && chat.CreatorID == user, nil
	}
	return false, nil
}

// DeliverDue pushes one batch of due deliveries. It returns how many it took.
func (w *OfficeEventWorker) DeliverDue(ctx context.Context) int {
	rows, err := w.h.Queries.ClaimDueOfficeDeliveries(ctx, officeDeliverBatch)
	if err != nil {
		slog.Error("office events: claim failed", "error", err)
		return 0
	}
	for _, d := range rows {
		w.push(ctx, d)
	}
	return len(rows)
}

// Blocked reasons: a short sentence for the person, then what fixes it.
const (
	officeBlockedChannel    = "Bot 连接已断开或未通过验证；重新连接并验证 Bot 后点重试"
	officeBlockedBotKey     = "Bot 的密钥已停用或删除；换一把新密钥重新连接后点重试"
	officeBlockedAccess     = "收件人已经看不到这条内容，按规矩不再发送"
	officeBlockedStreamOff  = "已关闭「我能看到的一切」推送；重新打开后点重试"
	officeBlockedNoWebhook  = "Bot 没有设置接收地址；设好地址后点重试，也可以用拉取接口补收"
	officeBlockedBadWebhook = "Bot 的接收地址不可用（只接受公网 HTTPS）；改好地址后点重试"
	officeBlockedRejected   = "Bot 拒收（%s）；检查 Bot 的接收密钥后点重试"
	officeMovedToNewBot     = "已改送到新连接的 Bot"
)

func (w *OfficeEventWorker) push(ctx context.Context, d db.OfficeEventDelivery) {
	ev, err := w.h.Queries.GetOfficeEvent(ctx, db.GetOfficeEventParams{EventID: d.EventID, WorkspaceID: d.WorkspaceID})
	if err != nil {
		w.retry(d, "event lookup failed")
		return
	}
	channel, err := w.h.Queries.GetHumanChannel(ctx, db.GetHumanChannelParams{WorkspaceID: d.WorkspaceID, UserID: d.TargetUserID})
	switch {
	case errors.Is(err, pgx.ErrNoRows) || (err == nil && (channel.BotID != d.BotID || !channel.VerifiedAt.Valid)):
		w.block(d, officeBlockedChannel, "", false)
		return
	case err != nil:
		w.retry(d, "channel lookup failed")
		return
	case !channel.BotActive:
		w.block(d, officeBlockedBotKey, "", false)
		return
	}
	if !w.canSee(ctx, ev, d.TargetUserID) {
		w.block(d, officeBlockedAccess, "", false)
		return
	}
	if d.Reason == "stream" && !slices.Contains(channel.ActivityScope, "owner_visible_all") {
		w.block(d, officeBlockedStreamOff, "", false)
		return
	}
	endpoint, key, reason := w.endpoint(ctx, d.BotID)
	if reason != "" {
		w.block(d, reason, "", false)
		return
	}
	view, err := w.h.officeEventView(ctx, ev, d.TargetUserID)
	if err != nil {
		w.retry(d, "build payload failed")
		return
	}
	scrubbed, err := service.ScrubPayload(w.h.officeEventPush(view, d))
	if err != nil {
		w.retry(d, "encode payload failed")
		return
	}
	body, err := json.Marshal(scrubbed)
	if err != nil {
		w.retry(d, "encode payload failed")
		return
	}
	outcome, detail := w.wake.deliver(ctx, endpoint, key, body)
	switch outcome {
	case "received":
		next := time.Now().Add(service.OfficeReceiptTimeout(int(d.Attempts)))
		if err := w.h.Queries.MarkOfficeDeliverySent(context.Background(), db.MarkOfficeDeliverySentParams{
			ID: d.ID, NextAttemptAt: pgtype.Timestamptz{Time: next, Valid: true},
		}); err != nil {
			slog.Warn("office events: record sent failed", "error", err, "delivery", d.ID)
		}
	case "failed":
		w.block(d, fmt.Sprintf(officeBlockedRejected, detail), detail, true)
	case "uncertain":
		w.uncertain(d, detail)
	default: // retry: it did not arrive; the same event_id goes again.
		w.retry(d, detail)
	}
}

func (w *OfficeEventWorker) endpoint(ctx context.Context, bot pgtype.UUID) (*url.URL, string, string) {
	wake, err := w.h.Queries.GetBotWake(ctx, bot)
	if err != nil || len(wake.WebhookSecret) == 0 || w.h.BotWakeSecrets == nil {
		return nil, "", officeBlockedNoWebhook
	}
	plain, err := w.h.BotWakeSecrets.Open(wake.WebhookSecret)
	if err != nil {
		return nil, "", officeBlockedNoWebhook
	}
	var secret botWakeSecrets
	if json.Unmarshal(plain, &secret) != nil {
		return nil, "", officeBlockedNoWebhook
	}
	endpoint, err := validateWakeURL(ctx, secret.URL)
	if err != nil {
		return nil, "", officeBlockedBadWebhook
	}
	return endpoint, secret.Key, ""
}

// uncertain: the request went out and the answer was lost, so the Bot may
// already hold it. Not resent at once: a receipt that arrives within the
// receipt timeout closes it; otherwise the next push says reconcile, and the
// Bot answers from its inbox instead of acting on it again.
func (w *OfficeEventWorker) uncertain(d db.OfficeEventDelivery, detail string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	next := time.Now().Add(service.OfficeReceiptTimeout(int(d.Attempts)))
	if err := w.h.Queries.MarkOfficeDeliveryUncertain(ctx, db.MarkOfficeDeliveryUncertainParams{
		ID: d.ID, LastError: "uncertain: " + detail, NextAttemptAt: pgtype.Timestamptz{Time: next, Valid: true},
	}); err != nil {
		slog.Warn("office events: record uncertain failed", "error", err, "delivery", d.ID)
	}
}

func (w *OfficeEventWorker) retry(d db.OfficeEventDelivery, detail string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	next := time.Now().Add(service.OfficeDeliveryBackoff(int(d.Attempts)))
	if err := w.h.Queries.MarkOfficeDeliveryRetry(ctx, db.MarkOfficeDeliveryRetryParams{
		ID: d.ID, LastError: detail, NextAttemptAt: pgtype.Timestamptz{Time: next, Valid: true},
	}); err != nil {
		slog.Warn("office events: record retry failed", "error", err, "delivery", d.ID)
	}
}

func (w *OfficeEventWorker) block(d db.OfficeEventDelivery, reason, detail string, attempted bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.h.Queries.BlockOfficeDelivery(ctx, db.BlockOfficeDeliveryParams{
		ID: d.ID, BlockedReason: reason, LastError: detail, Attempted: attempted,
	}); err != nil {
		slog.Warn("office events: record block failed", "error", err, "delivery", d.ID)
	}
}

// Fallback puts results that must reach a person, and did not, in their
// inbox: once per delivery, and once per person whose Bot was not connected.
func (w *OfficeEventWorker) Fallback(ctx context.Context) int {
	rows, err := w.h.Queries.ListOfficeFallbackDue(ctx, db.ListOfficeFallbackDueParams{
		GraceSeconds: int32(officeFallbackGrace.Seconds()), Lim: 50,
	})
	if err != nil {
		slog.Warn("office events: fallback scan failed", "error", err)
		return 0
	}
	n := 0
	for _, row := range rows {
		d, ev := row.OfficeEventDelivery, row.OfficeEvent
		body := "你的 Bot 还没确认把这条结果告诉你，这里补一条。"
		if d.Status == "blocked" {
			body = "没能送到你的 Bot：" + d.BlockedReason
		}
		visible, err := w.h.officeEventVisibleErr(ctx, ev, d.TargetUserID)
		if err != nil {
			continue
		}
		if visible && !w.remind(ctx, ev, d.TargetUserID, body) {
			continue
		}
		if err := w.h.Queries.MarkOfficeFallback(ctx, d.ID); err != nil {
			slog.Warn("office events: remember fallback failed", "error", err, "delivery", d.ID)
		}
		n++
	}
	gaps, err := w.h.Queries.ListOfficeGapFallbackDue(ctx, 50)
	if err != nil {
		slog.Warn("office events: gap fallback scan failed", "error", err)
		return n
	}
	for _, row := range gaps {
		g, ev := row.OfficeEventGap, row.OfficeEvent
		visible, err := w.h.officeEventVisibleErr(ctx, ev, g.UserID)
		if err != nil {
			continue
		}
		if visible && !w.remind(ctx, ev, g.UserID, "你的 Bot 没有连接，这条结果只在应用里；重新连接并验证 Bot 后会补发给它。") {
			continue
		}
		if err := w.h.Queries.MarkOfficeGapFallback(ctx, g.ID); err != nil {
			slog.Warn("office events: remember gap fallback failed", "error", err, "gap", g.ID)
		}
		n++
	}
	return n
}

// remind writes one in-app notice about an office event. It reports whether
// the notice was written.
func (w *OfficeEventWorker) remind(ctx context.Context, ev db.OfficeEvent, user pgtype.UUID, body string) bool {
	if ev.PendingApproval {
		body += "（系统改成完成，正式发布仍待批准，不代表可以发布。）"
	}
	details, _ := json.Marshal(map[string]string{
		"office_event_id": uuidToString(ev.EventID),
		"chat_session_id": uuidToString(ev.ChatSessionID),
		"meaning":         ev.Meaning,
	})
	item, err := w.h.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		ID:            dbid.NewV7(),
		WorkspaceID:   ev.WorkspaceID,
		RecipientType: "member",
		RecipientID:   user,
		Type:          service.InboxTypeNeedsYou,
		Severity:      "attention",
		IssueID:       ev.IssueID,
		Title:         firstNonEmpty(ev.Summary, ev.EventType),
		Body:          pgtype.Text{String: body, Valid: true},
		ActorType:     pgtype.Text{String: "system", Valid: true},
		Details:       details,
	})
	if err != nil {
		slog.Warn("office events: fallback row failed", "error", err, "event", uuidToString(ev.EventID))
		return false
	}
	status := ""
	if ev.IssueID.Valid {
		if issue, err := w.h.Queries.GetIssue(ctx, ev.IssueID); err == nil {
			status = issue.Status
		}
	}
	service.PublishInboxNew(w.h.Bus, item, status)
	return true
}

// Restore brings results back to a person who connects and verifies a Bot
// again: open gaps get a delivery to that Bot, and owner rows blocked because
// the connection was gone are requeued (same Bot) or moved (new Bot).
// Whatever its age, and only what the person can still see; one that is no
// longer visible is closed without sending. Each sweep takes a bounded batch.
func (w *OfficeEventWorker) Restore(ctx context.Context) int {
	n := 0
	gaps, err := w.h.Queries.ListOfficeGapsRestorable(ctx, 100)
	if err != nil {
		slog.Warn("office events: gap restore scan failed", "error", err)
	}
	for _, row := range gaps {
		g, b := row.OfficeEventGap, row.MemberBotBinding
		if w.redeliver(ctx, g.EventID, g.WorkspaceID, b, row.MustRelay) {
			if err := w.h.Queries.ResolveOfficeGap(ctx, db.ResolveOfficeGapParams{ID: g.ID, BotID: b.BotID}); err != nil {
				slog.Warn("office events: resolve gap failed", "error", err, "gap", g.ID)
				continue
			}
			n++
		}
	}
	blocked, err := w.h.Queries.ListOfficeDeliveriesRestorable(ctx, db.ListOfficeDeliveriesRestorableParams{
		ChannelReason: officeBlockedChannel, Lim: 100,
	})
	if err != nil {
		slog.Warn("office events: delivery restore scan failed", "error", err)
	}
	for _, row := range blocked {
		d, b := row.OfficeEventDelivery, row.MemberBotBinding
		if b.BotID == d.BotID {
			if err := w.h.Queries.RequeueOfficeDelivery(ctx, d.ID); err != nil {
				slog.Warn("office events: requeue failed", "error", err, "delivery", d.ID)
				continue
			}
			n++
			continue
		}
		if w.redeliver(ctx, d.EventID, d.WorkspaceID, b, d.MustRelay) {
			if err := w.h.Queries.MoveOfficeDelivery(ctx, db.MoveOfficeDeliveryParams{ID: d.ID, BlockedReason: officeMovedToNewBot}); err != nil {
				slog.Warn("office events: move delivery failed", "error", err, "delivery", d.ID)
				continue
			}
			n++
		}
	}
	return n
}

// redeliver opens a delivery of one event to a person's current Bot after
// checking they can still see it. It reports whether the event is now owed
// to that Bot (false on a lookup failure: try again next sweep).
func (w *OfficeEventWorker) redeliver(ctx context.Context, eventID, workspaceID pgtype.UUID, b db.MemberBotBinding, mustRelay bool) bool {
	ev, err := w.h.Queries.GetOfficeEvent(ctx, db.GetOfficeEventParams{EventID: eventID, WorkspaceID: workspaceID})
	if err != nil {
		return false
	}
	visible, err := w.h.officeEventVisibleErr(ctx, ev, b.UserID)
	if err != nil {
		return false
	}
	if !visible {
		return true // nothing is owed any more
	}
	if err := w.h.Queries.CreateOfficeEventDelivery(ctx, db.CreateOfficeEventDeliveryParams{
		EventID: ev.EventID, WorkspaceID: ev.WorkspaceID, BotID: b.BotID, BindingID: b.ID,
		TargetUserID: b.UserID, Reason: "owner", MustRelay: mustRelay, Status: "pending",
	}); err != nil {
		slog.Warn("office events: restore delivery failed", "error", err, "event", uuidToString(ev.EventID))
		return false
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
