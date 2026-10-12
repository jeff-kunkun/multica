package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Human-needed routing (DENE-1933). A person who connected a Bot gets every
// "this needs you" pushed to that Bot; a person without a verified binding is
// untouched. Producers call RouteHumanNeeded with the person they want; the
// answer says whether the Bot took it, and the producer decides what happens
// to its own in-app row.

// Binding modes. bot_first keeps the in-app row silently; mirror also alerts.
const (
	HumanBindBotFirst  = "bot_first"
	HumanBindMirror    = "mirror"
	HumanUrgencyNormal = "normal"
	HumanUrgencyUrgent = "urgent"
)

// Event types on the wire.
const (
	HumanEventNeeded     = "human_needed"
	HumanEventEscalation = "human_needed.escalation"
	HumanEventVerify     = "binding.verify"
)

// Open states: the Bot still owes the person something.
var HumanNeededOpenStatuses = []string{"queued", "sending", "sent", "woken", "relayed"}

type HumanNeededOption struct {
	ID    string `json:"id,omitempty"`
	Label string `json:"label"`
}

type HumanNeededInput struct {
	WorkspaceID   pgtype.UUID
	Target        pgtype.UUID
	Kind          string // the summon source, "mention", "ask_human" ...
	DedupeKey     string
	IssueID       pgtype.UUID
	ChatSessionID pgtype.UUID
	SummonID      pgtype.UUID
	InboxItemID   pgtype.UUID
	CommentID     pgtype.UUID
	AskID         pgtype.UUID // the question card this call carries
	CallerType    string
	CallerID      pgtype.UUID
	Question      string
	Options       []HumanNeededOption
	Urgent        bool
	Deadline      time.Time
}

type HumanNeededResult struct {
	// Routed is true when the person's Bot owns this call. A duplicate is still
	// routed: an earlier write already handed it over.
	Routed    bool
	Duplicate bool
	Mode      string
	Event     db.HumanNeededEvent
}

// Silent reports whether the producer should keep its in-app row quiet
// (stored read) because the Bot is the channel.
func (r HumanNeededResult) Silent() bool {
	return r.Routed && r.Mode == HumanBindBotFirst
}

// HumanChannelUsable is the one definition of "this person's Bot can be
// pushed to": verified, alive, theirs, and a webhook behind it.
func HumanChannelUsable(c db.GetHumanChannelRow) bool {
	return c.VerifiedAt.Valid && !c.RevokedAt.Valid && c.BotActive && c.WebhookConfigured
}

// RouteHumanNeeded hands a call to the target's Bot when they have a usable
// binding. It never errors for "no binding": that is the unrouted path.
func RouteHumanNeeded(ctx context.Context, q *db.Queries, in HumanNeededInput) (HumanNeededResult, error) {
	var out HumanNeededResult
	if !in.WorkspaceID.Valid || !in.Target.Valid || strings.TrimSpace(in.DedupeKey) == "" {
		return out, nil
	}
	channel, err := q.GetHumanChannel(ctx, db.GetHumanChannelParams{WorkspaceID: in.WorkspaceID, UserID: in.Target})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if !HumanChannelUsable(channel) {
		return out, nil
	}
	options := in.Options
	if options == nil {
		options = []HumanNeededOption{}
	}
	rawOptions, err := json.Marshal(options)
	if err != nil {
		return out, err
	}
	urgency := HumanUrgencyNormal
	if in.Urgent {
		urgency = HumanUrgencyUrgent
	}
	callerType := in.CallerType
	if callerType == "" {
		callerType = "system"
	}
	deadline := pgtype.Timestamptz{}
	if !in.Deadline.IsZero() {
		deadline = pgtype.Timestamptz{Time: in.Deadline, Valid: true}
	}
	ev, err := q.CreateHumanNeededEvent(ctx, db.CreateHumanNeededEventParams{
		WorkspaceID:     in.WorkspaceID,
		BotID:           channel.BotID,
		BindingID:       channel.ID,
		TargetUserID:    in.Target,
		EventType:       HumanEventNeeded,
		Kind:            in.Kind,
		DedupeKey:       in.DedupeKey,
		IssueID:         in.IssueID,
		ChatSessionID:   in.ChatSessionID,
		SummonID:        in.SummonID,
		InboxItemID:     in.InboxItemID,
		SourceCommentID: in.CommentID,
		AskID:           in.AskID,
		CallerType:      callerType,
		CallerID:        in.CallerID,
		Question:        truncateRunesForHuman(strings.TrimSpace(in.Question), 1000),
		Options:         rawOptions,
		Urgency:         urgency,
		Deadline:        deadline,
	})
	out.Routed = true
	out.Mode = channel.Mode
	if errors.Is(err, pgx.ErrNoRows) {
		out.Duplicate = true
		return out, nil
	}
	if err != nil {
		return HumanNeededResult{}, err
	}
	out.Event = ev
	return out, nil
}

func truncateRunesForHuman(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// HumanNeededBackoff is the retry delay after the given number of failed
// pushes: 5s, 15s, 45s, 2m, 5m, then doubling up to 30m.
func HumanNeededBackoff(failures int) time.Duration {
	steps := []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 20 * time.Minute}
	if failures < 1 {
		failures = 1
	}
	if failures <= len(steps) {
		return steps[failures-1]
	}
	return 30 * time.Minute
}

// SettleHumanNeededForSummons ends the Bot's chase for calls that ended in
// the app (answered there, or the ticket moved on).
func SettleHumanNeededForSummons(ctx context.Context, q *db.Queries, summons []db.IssueSummon) {
	if len(summons) == 0 {
		return
	}
	ids := make([]pgtype.UUID, 0, len(summons))
	for _, s := range summons {
		ids = append(ids, s.ID)
	}
	if err := q.SettleHumanNeededBySummons(ctx, ids); err != nil {
		slog.Warn("human needed: settle by summons failed", "error", err)
	}
}

// PublishInboxNew broadcasts an inbox row to its recipient's clients.
func PublishInboxNew(bus *events.Bus, item db.InboxItem, issueStatus string) {
	if bus == nil {
		return
	}
	bus.Publish(events.Event{
		Type:        protocol.EventInboxNew,
		WorkspaceID: util.UUIDToString(item.WorkspaceID),
		ActorType:   item.ActorType.String,
		ActorID:     util.UUIDToString(item.ActorID),
		Payload: map[string]any{"item": map[string]any{
			"id":             util.UUIDToString(item.ID),
			"workspace_id":   util.UUIDToString(item.WorkspaceID),
			"recipient_type": item.RecipientType,
			"recipient_id":   util.UUIDToString(item.RecipientID),
			"type":           item.Type,
			"severity":       item.Severity,
			"issue_id":       util.UUIDToPtr(item.IssueID),
			"title":          item.Title,
			"body":           util.TextToPtr(item.Body),
			"read":           item.Read,
			"archived":       item.Archived,
			"created_at":     util.TimestampToString(item.CreatedAt),
			"actor_type":     util.TextToPtr(item.ActorType),
			"actor_id":       util.UUIDToPtr(item.ActorID),
			"details":        json.RawMessage(item.Details),
			"issue_status":   issueStatus,
		}},
	})
}

// FallbackHumanNeededInApp puts a call back in front of the person in the app
// (DENE-1933): the Bot channel failed or the Bot did not relay in time. A call
// that kept an in-app row has it made unread again; one that had none (a chat
// question) gets a row now, so the person is never left with nothing to see.
func FallbackHumanNeededInApp(ctx context.Context, q *db.Queries, bus *events.Bus, ev db.HumanNeededEvent) {
	status := ""
	title := "要你决定"
	if ev.IssueID.Valid {
		if issue, getErr := q.GetIssue(ctx, ev.IssueID); getErr == nil {
			status = issue.Status
			title = issue.Title
		}
	} else if ev.ChatSessionID.Valid {
		if chat, getErr := q.GetChatSession(ctx, ev.ChatSessionID); getErr == nil && chat.Title != "" {
			title = chat.Title
		}
	}
	if ev.InboxItemID.Valid {
		item, err := q.MarkInboxUnread(ctx, ev.InboxItemID)
		if err == nil {
			PublishInboxNew(bus, item, status)
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("human needed: in-app fallback failed", "error", err, "event_id", util.UUIDToString(ev.EventID))
			return
		}
	}
	details := map[string]string{"human_needed": "fallback"}
	if ev.AskID.Valid {
		details["ask_id"] = util.UUIDToString(ev.AskID)
	}
	if ev.ChatSessionID.Valid {
		details["chat_session_id"] = util.UUIDToString(ev.ChatSessionID)
	}
	raw, _ := json.Marshal(details)
	item, err := q.CreateInboxItem(ctx, db.CreateInboxItemParams{
		ID:            dbid.NewV7(),
		WorkspaceID:   ev.WorkspaceID,
		RecipientType: "member",
		RecipientID:   ev.TargetUserID,
		Type:          InboxTypeNeedsYou,
		Severity:      "action_required",
		IssueID:       ev.IssueID,
		Title:         title,
		Body:          pgtype.Text{String: ev.Question, Valid: ev.Question != ""},
		ActorType:     pgtype.Text{String: ev.CallerType, Valid: true},
		ActorID:       ev.CallerID,
		Details:       raw,
	})
	if err != nil {
		slog.Warn("human needed: in-app fallback row failed", "error", err, "event_id", util.UUIDToString(ev.EventID))
		return
	}
	if err := q.AttachHumanNeededInboxItem(ctx, db.AttachHumanNeededInboxItemParams{EventID: ev.EventID, InboxItemID: item.ID}); err != nil {
		slog.Warn("human needed: remember fallback row failed", "error", err, "event_id", util.UUIDToString(ev.EventID))
	}
	PublishInboxNew(bus, item, status)
}

// NotifyHumanChannelBroken tells a person in the app that their Bot can no
// longer take calls, once per outage: callers gate it on the binding's
// healthy -> broken change.
func NotifyHumanChannelBroken(ctx context.Context, q *db.Queries, bus *events.Bus, workspaceID, userID pgtype.UUID) {
	item, err := q.CreateInboxItem(ctx, db.CreateInboxItemParams{
		ID:            dbid.NewV7(),
		WorkspaceID:   workspaceID,
		RecipientType: "member",
		RecipientID:   userID,
		Type:          InboxTypeNeedsYou,
		Severity:      "action_required",
		Title:         "你的 Bot 暂时收不到消息",
		Body:          pgtype.Text{String: "要你决定的事先放回站内提醒；到 设置 → 通知 看看 Bot 的连接，恢复后会自动改回 Bot。", Valid: true},
		ActorType:     pgtype.Text{String: "system", Valid: true},
		Details:       []byte(`{"human_needed":"channel_broken"}`),
	})
	if err != nil {
		slog.Warn("human needed: channel broken notice failed", "error", err, "user_id", util.UUIDToString(userID))
		return
	}
	PublishInboxNew(bus, item, "")
}

// HumanMentionKey is the dedupe key for an @-mention of a person in a comment
// (or a description, keyed by the issue). Summon and the mention listener both
// use it, so one @ is one push.
func HumanMentionKey(source, target pgtype.UUID) string {
	return "mention:" + util.UUIDToString(source) + ":" + util.UUIDToString(target)
}

// HumanDescriptionMentionKey is the key for an @ added to a description. The
// version names the edit that added it, so the same edit never pushes twice
// but removing the @ and adding it again later is a new ask.
func HumanDescriptionMentionKey(issueID, target pgtype.UUID, version string) string {
	return "mention:" + util.UUIDToString(issueID) + ":" + util.UUIDToString(target) + ":desc:" + version
}

// MentionQuestion is what a Bot is told when someone @s a person in a comment:
// the comment text with the mention markup flattened.
func MentionQuestion(content string) string {
	text := strings.TrimSpace(util.MentionRe.ReplaceAllString(content, "@$1"))
	if text == "" {
		text = "在评论里 @ 了你"
	}
	return truncateRunesForHuman(text, 300)
}

// CreateHumanNeededFollowUp writes a child of an open event (an escalation or
// a reminder). It carries no in-app row: the parent's row is the one that
// falls back, once.
func CreateHumanNeededFollowUp(ctx context.Context, q *db.Queries, parent db.HumanNeededEvent, key string) (db.HumanNeededEvent, error) {
	return q.CreateHumanNeededEvent(ctx, db.CreateHumanNeededEventParams{
		WorkspaceID:     parent.WorkspaceID,
		BotID:           parent.BotID,
		BindingID:       parent.BindingID,
		TargetUserID:    parent.TargetUserID,
		EventType:       HumanEventEscalation,
		ParentEventID:   parent.EventID,
		Kind:            parent.Kind,
		DedupeKey:       key,
		IssueID:         parent.IssueID,
		ChatSessionID:   parent.ChatSessionID,
		SummonID:        parent.SummonID,
		SourceCommentID: parent.SourceCommentID,
		CallerType:      parent.CallerType,
		CallerID:        parent.CallerID,
		Question:        parent.Question,
		Options:         parent.Options,
		Urgency:         HumanUrgencyUrgent,
		Deadline:        parent.Deadline,
		ExpiresAt:       parent.ExpiresAt,
	})
}

// CreateHumanChannelVerify asks the Bot to confirm it can receive: the Bot
// answers with the usual woken receipt and that verifies the binding.
func CreateHumanChannelVerify(ctx context.Context, q *db.Queries, binding db.MemberBotBinding) (db.HumanNeededEvent, error) {
	return q.CreateHumanNeededEvent(ctx, db.CreateHumanNeededEventParams{
		WorkspaceID:  binding.WorkspaceID,
		BotID:        binding.BotID,
		BindingID:    binding.ID,
		TargetUserID: binding.UserID,
		EventType:    HumanEventVerify,
		Kind:         "verify",
		DedupeKey:    "verify:" + util.UUIDToString(binding.ID) + ":" + time.Now().UTC().Format("20060102T150405.000"),
		CallerType:   "system",
		Question:     "",
		Options:      []byte("[]"),
		Urgency:      HumanUrgencyNormal,
		ExpiresAt:    pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
}
