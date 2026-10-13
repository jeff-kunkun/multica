package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// HTTP surface of the office event stream (DENE-1963). Contract:
// server/docs/office-event-contract.md.
//
//	GET  /api/workspaces/{id}/office-events                    member pull by cursor
//	GET  /api/workspaces/{id}/office-events/{eventId}          one event, its files, its deliveries
//	GET  /api/workspaces/{id}/me/office-deliveries             my Bot's deliveries (settings page)
//	POST /api/workspaces/{id}/me/office-deliveries/retry       put my blocked ones back
//	POST /api/workspaces/{id}/bots/{botId}/office-events/{eventId}/receipt
//	GET  /api/workspaces/{id}/bots/{botId}/office-deliveries
//	POST /api/workspaces/{id}/bots/{botId}/office-deliveries/retry

// officeEventView is the one shape of an event, for push and pull alike, so a
// Bot that missed a push reads exactly what it would have received.
func (h *Handler) officeEventView(ctx context.Context, ev db.OfficeEvent, viewer pgtype.UUID) (map[string]any, error) {
	ws, err := h.Queries.GetWorkspace(ctx, ev.WorkspaceID)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(h.cfg.PublicURL, "/")
	changes := service.ParseOfficeChanges(ev.Changes)
	if changes == nil {
		changes = []service.OfficeChange{}
	}
	view := map[string]any{
		"schema":                 service.OfficeEventSchema,
		"type":                   "office_event",
		"workspace_id":           uuidToString(ev.WorkspaceID),
		"event_id":               uuidToString(ev.EventID),
		"stream_seq":             ev.StreamSeq.Int64,
		"family":                 ev.Family,
		"event_type":             ev.EventType,
		"meaning":                ev.Meaning,
		"summary":                ev.Summary,
		"must_relay":             ev.MustRelay,
		"needs_kun":              ev.MustRelay,
		"bypass_semantic_dedupe": ev.MustRelay,
		"pending_approval":       ev.PendingApproval,
		"business_key":           ev.BusinessKey,
		"occurred_at":            ev.OccurredAt.Time.UTC().Format(time.RFC3339Nano),
		"object":                 map[string]any{"type": ev.ObjectType, "id": ev.ObjectID},
		"changes":                changes,
		"actor":                  h.officeActor(ctx, ev, viewer),
		"event_url":              base + "/api/workspaces/" + uuidToString(ev.WorkspaceID) + "/office-events/" + uuidToString(ev.EventID),
	}
	if ev.PendingApproval {
		view["notice"] = "系统改成完成，正式发布仍待批准。这不是发布许可。"
	}
	if ev.RecipientGap != "" {
		view["recipient_gap"] = ev.RecipientGap
	}
	if ev.IssueID.Valid {
		if issue, err := h.Queries.GetIssue(ctx, ev.IssueID); err == nil {
			view["issue"] = map[string]any{
				"id": uuidToString(issue.ID), "key": issueIdentifier(ws.IssuePrefix, issue.Number),
				"title": issue.Title, "status": issue.Status,
			}
			view["web_url"] = base + "/" + url.PathEscape(ws.Slug) + "/issues/" + uuidToString(issue.ID)
		}
	}
	if ev.ChatSessionID.Valid {
		if chat, err := h.Queries.GetChatSession(ctx, ev.ChatSessionID); err == nil {
			view["chat"] = map[string]any{"id": uuidToString(chat.ID), "title": chat.Title}
			if _, ok := view["web_url"]; !ok {
				view["web_url"] = base + "/" + url.PathEscape(ws.Slug) + "/chat/" + uuidToString(chat.ID)
			}
		}
	}
	if ev.ChatMessageID.Valid && ev.EventType == "chat.replied" {
		if msg, err := h.Queries.GetChatMessage(ctx, ev.ChatMessageID); err == nil {
			view["message"] = map[string]any{"id": uuidToString(msg.ID), "content": msg.Content, "kind": msg.MessageKind}
		}
	}
	view["artifacts"] = h.officeArtifacts(ctx, ev)
	return view, nil
}

// officeArtifacts is the file catalogue of an event: what the event names,
// the files on its chat message or comment, and the files its run uploaded.
// Each entry carries what a receiver needs to fetch and check it; a file that
// cannot be checked or no longer exists says so.
func (h *Handler) officeArtifacts(ctx context.Context, ev db.OfficeEvent) []map[string]any {
	base := strings.TrimRight(h.cfg.PublicURL, "/")
	params := db.ListAttachmentsForOfficeEventParams{WorkspaceID: ev.WorkspaceID, AttachmentID: ev.AttachmentID}
	switch {
	case ev.EventType == "chat.replied":
		params.ChatMessageID = ev.ChatMessageID
	case strings.HasPrefix(ev.EventType, "run."):
		params.TaskID = ev.TaskID
	case ev.EventType == "issue.status_changed" && officeCarriesTicketFiles(ev.Meaning):
		params.IssueID = ev.IssueID
	}
	out := []map[string]any{}
	rows, err := h.Queries.ListAttachmentsForOfficeEvent(ctx, params)
	if err != nil {
		return out
	}
	found := false
	for _, a := range rows {
		if a.ID == ev.AttachmentID {
			found = true
		}
		status := "ready"
		if !a.Sha256.Valid || a.Sha256.String == "" {
			status = "unverifiable"
		}
		id := uuidToString(a.ID)
		out = append(out, map[string]any{
			"id":           id,
			"filename":     a.Filename,
			"content_type": a.ContentType,
			"size":         a.SizeBytes,
			"sha256":       a.Sha256.String,
			"status":       status,
			"download_url": base + "/api/attachments/" + id + "/download?raw=1",
			"metadata_url": base + "/api/attachments/" + id,
			"cli":          "multica attachment download " + id,
			"created_at":   a.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		})
	}
	if ev.AttachmentID.Valid && !found {
		out = append(out, map[string]any{"id": uuidToString(ev.AttachmentID), "status": "deleted"})
	}
	return out
}

// officeCarriesTicketFiles: a ticket that finished, changed hands or waits
// for release approval carries every file on it, so the person gets the
// deliverables with the news, not just the status.
func officeCarriesTicketFiles(meaning string) bool {
	switch meaning {
	case service.OfficeMeaningTaskDone, service.OfficeMeaningHandoff, service.OfficeMeaningPendingRel:
		return true
	}
	return false
}

func (h *Handler) officeActor(ctx context.Context, ev db.OfficeEvent, viewer pgtype.UUID) map[string]any {
	a := map[string]any{"type": ev.ActorType, "id": uuidToString(ev.ActorID), "source": ev.ActorSource}
	switch ev.ActorType {
	case "member":
		if u, err := h.Queries.GetUser(ctx, ev.ActorID); err == nil {
			a["name"] = u.Name
		}
	case "agent":
		if agent, err := h.Queries.GetAgent(ctx, ev.ActorID); err == nil && agent.WorkspaceID == ev.WorkspaceID &&
			h.canAccessPrivateAgent(ctx, agent, "member", uuidToString(viewer), uuidToString(ev.WorkspaceID)) {
			a["name"] = agent.Name
		}
	}
	if ev.ViaBotID.Valid {
		a["via_bot_id"] = uuidToString(ev.ViaBotID)
		if bot, err := h.Queries.GetWakeBot(ctx, db.GetWakeBotParams{ID: ev.ViaBotID, WorkspaceID: ev.WorkspaceID}); err == nil {
			a["via_bot_name"] = bot.Name
		}
	}
	if ev.OnBehalfOfUserID.Valid {
		a["on_behalf_of_user_id"] = uuidToString(ev.OnBehalfOfUserID)
	}
	if ev.ActorTaskID.Valid {
		a["task_id"] = uuidToString(ev.ActorTaskID)
	}
	return a
}

// officeEventPush wraps the view for one Bot: where to send the receipt, and
// whether this is a repeat of something already sent.
func (h *Handler) officeEventPush(view map[string]any, d db.OfficeEventDelivery) map[string]any {
	view["needs_kun"] = d.MustRelay
	if d.Reason == "owner" {
		view["owner_bot"] = uuidToString(d.BotID)
	}
	base := strings.TrimRight(h.cfg.PublicURL, "/")
	view["delivery"] = map[string]any{
		"attempt":    d.Attempts + 1,
		"redelivery": d.SentAt.Valid,
		// Anything sent before may be in the Bot's inbox already (its 200,
		// receipt or the claim itself may have been lost): look the event_id
		// up, resend that receipt, act only on steps not yet done.
		"reconcile":    d.SentAt.Valid || d.UncertainAt.Valid,
		"bot_id":       uuidToString(d.BotID),
		"owner_bot":    uuidToString(d.BotID),
		"reason":       d.Reason,
		"relay_target": d.RelayTarget,
		"must_relay":   d.MustRelay,
		"target_user":  uuidToString(d.TargetUserID),
		"receipt_url": base + "/api/workspaces/" + uuidToString(d.WorkspaceID) + "/bots/" + uuidToString(d.BotID) +
			"/office-events/" + uuidToString(d.EventID) + "/receipt",
	}
	return view
}

func (h *Handler) officeDeliveryView(d db.OfficeEventDelivery) map[string]any {
	ts := func(t pgtype.Timestamptz) any {
		if !t.Valid {
			return nil
		}
		return t.Time.UTC().Format(time.RFC3339Nano)
	}
	var artifacts any = map[string]any{}
	if len(d.Artifacts) > 0 {
		_ = json.Unmarshal(d.Artifacts, &artifacts)
	}
	return map[string]any{
		"event_id":        uuidToString(d.EventID),
		"bot_id":          uuidToString(d.BotID),
		"owner_bot":       uuidToString(d.BotID),
		"target_user_id":  uuidToString(d.TargetUserID),
		"reason":          d.Reason,
		"relay_target":    d.RelayTarget,
		"must_relay":      d.MustRelay,
		"status":          d.Status,
		"attempts":        d.Attempts,
		"last_error":      d.LastError,
		"blocked_reason":  d.BlockedReason,
		"next_attempt_at": ts(d.NextAttemptAt),
		"last_attempt_at": ts(d.LastAttemptAt),
		"sent_at":         ts(d.SentAt),
		"received_at":     ts(d.ReceivedAt),
		"relayed_at":      ts(d.RelayedAt),
		"relay_channel":   d.RelayChannel,
		"relay_ref":       d.RelayRef,
		"relay_error":     d.RelayError,
		"handled_reason":  d.HandledReason,
		"fallback_at":     ts(d.FallbackAt),
		"uncertain_at":    ts(d.UncertainAt),
		"artifacts":       artifacts,
		"cursor":          d.ID,
	}
}

// officeMember is the caller as a workspace member. Agents and machine keys
// read the stream through their own surfaces, not this one; a Bot key acts as
// its owner here.
func (h *Handler) officeMember(w http.ResponseWriter, r *http.Request) (pgtype.UUID, pgtype.UUID, bool) {
	user, ok := requireUserID(w, r)
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	ws, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	actor, _ := h.resolveActor(r, user, uuidToString(ws))
	if actor != "member" || (isMachineCredentialActor(r) && r.Header.Get("X-Actor-Source") != "mcp_connection") {
		writeError(w, http.StatusForbidden, "the office event stream is read by members and their Bots")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	return ws, parseUUID(user), true
}

// ListOfficeEvents is the cursor pull. Rows the caller cannot see are skipped,
// and the cursor still moves past them, so paging never stalls.
func (h *Handler) ListOfficeEvents(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.officeMember(w, r)
	if !ok {
		return
	}
	qv := r.URL.Query()
	after, _ := strconv.ParseInt(qv.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(qv.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	params := db.ListOfficeEventsParams{WorkspaceID: ws, Lim: int32(limit)}
	if f := qv.Get("family"); f != "" {
		params.Family = pgtype.Text{String: f, Valid: true}
	}
	if v := qv.Get("issue_id"); v != "" {
		id, ok := parseUUIDOrBadRequest(w, v, "issue_id")
		if !ok {
			return
		}
		params.IssueID = id
	}
	if v := qv.Get("chat_session_id"); v != "" {
		id, ok := parseUUIDOrBadRequest(w, v, "chat_session_id")
		if !ok {
			return
		}
		params.ChatSessionID = id
	}
	events := []map[string]any{}
	cursor := after
	for page := 0; page < 10 && len(events) < limit; page++ {
		params.After = pgtype.Int8{Int64: cursor, Valid: true}
		rows, err := h.Queries.ListOfficeEvents(r.Context(), params)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list office events")
			return
		}
		for _, ev := range rows {
			cursor = ev.StreamSeq.Int64
			if !h.officeEventVisible(r.Context(), ev, user) {
				continue
			}
			view, err := h.officeEventView(r.Context(), ev, user)
			if err != nil {
				continue
			}
			events = append(events, view)
			if len(events) >= limit {
				break
			}
		}
		if len(rows) < int(params.Lim) {
			break
		}
	}
	writeScrubbed(w, (map[string]any{
		"events": events, "next_cursor": cursor, "schema": service.OfficeEventSchema,
	}))
}

// GetOfficeEvent is one event with its file catalogue and the caller's Bot
// deliveries (all deliveries for owners and admins).
func (h *Handler) GetOfficeEvent(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.officeMember(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "eventId"), "event id")
	if !ok {
		return
	}
	ev, err := h.Queries.GetOfficeEvent(r.Context(), db.GetOfficeEventParams{EventID: id, WorkspaceID: ws})
	if err != nil || !ev.StreamSeq.Valid || !h.officeEventVisible(r.Context(), ev, user) {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	view, err := h.officeEventView(r.Context(), ev, user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read event")
		return
	}
	admin := false
	if m, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: user, WorkspaceID: ws}); err == nil {
		admin = m.Role == "owner" || m.Role == "admin"
	}
	deliveries := []map[string]any{}
	if rows, err := h.Queries.ListOfficeDeliveriesForEvent(r.Context(), id); err == nil {
		for _, d := range rows {
			if admin || d.TargetUserID == user {
				deliveries = append(deliveries, h.officeDeliveryView(d))
			}
		}
	}
	view["deliveries"] = deliveries
	writeScrubbed(w, (view))
}

type officeReceiptRequest struct {
	// received | relayed | handled | relay_failed
	Stage      string `json:"stage"`
	Target     string `json:"target"`
	Channel    string `json:"channel"`
	MessageRef string `json:"message_ref"`
	Reason     string `json:"reason"`
	Error      string `json:"error"`
	Artifacts  []struct {
		ID     string `json:"id"`
		Status string `json:"status"` // sent | failed | skipped
		Ref    string `json:"ref"`
		Error  string `json:"error"`
	} `json:"artifacts"`
}

// OfficeEventReceipt is the Bot's durable acknowledgement. Repeats are safe.
// A result that must reach the person cannot be closed as "handled": only a
// "relayed" receipt closes it.
func (h *Handler) OfficeEventReceipt(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	eventID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "eventId"), "event id")
	if !ok {
		return
	}
	var req officeReceiptRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid receipt")
		return
	}
	switch req.Stage {
	case "received", "relayed", "handled", "relay_failed":
	default:
		writeError(w, http.StatusBadRequest, "stage must be received, relayed, handled or relay_failed")
		return
	}
	d, err := h.Queries.GetOfficeDeliveryForBot(r.Context(), db.GetOfficeDeliveryForBotParams{EventID: eventID, BotID: bot.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no delivery of this event to this Bot")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read delivery")
		return
	}
	if req.Stage == "handled" && d.MustRelay && d.Status != "relayed" {
		writeErrorCode(w, http.StatusConflict, "must_relay",
			"this result must reach the person: tell them, then send stage relayed")
		return
	}
	// The server cannot see the person's chat app; it can insist the Bot
	// names where it told them, so "relayed" is a checkable claim and not a
	// bare word.
	if req.Stage == "relayed" && (strings.TrimSpace(req.Channel) == "" || strings.TrimSpace(req.MessageRef) == "") {
		writeErrorCode(w, http.StatusBadRequest, "relay_proof_required",
			"stage relayed needs channel and message_ref: where you told the person, and the message id there")
		return
	}
	if req.Stage == "relayed" && d.RelayTarget != "" && strings.TrimSpace(req.Target) != d.RelayTarget {
		writeErrorCode(w, http.StatusBadRequest, "relay_target_mismatch", "stage relayed must name the delivery relay_target as target")
		return
	}
	if req.Stage == "relay_failed" && strings.TrimSpace(req.Error) == "" {
		writeError(w, http.StatusBadRequest, "stage relay_failed needs error")
		return
	}
	artifacts, msg := h.officeArtifactReceipts(r.Context(), eventID, d, req)
	if msg != "" {
		writeErrorCode(w, http.StatusBadRequest, "invalid_artifact_receipt", msg)
		return
	}
	raw, _ := json.Marshal(artifacts)
	updated, err := h.Queries.AckOfficeDelivery(r.Context(), db.AckOfficeDeliveryParams{
		ID: d.ID, Stage: req.Stage, RelayChannel: clip(req.Channel, 100), RelayRef: clip(req.MessageRef, 500),
		RelayError: clip(req.Error, 500), HandledReason: clip(req.Reason, 500), Artifacts: raw,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record receipt")
		return
	}
	writeScrubbed(w, (h.officeDeliveryView(updated)))
}

// officeArtifactReceipts checks per-file receipts: the file must be in this
// event's catalogue, the status one of sent/failed/skipped, a sent file names
// where it went (ref), a failed one says why (error). A file already recorded
// as sent stays sent: the merge in AckOfficeDelivery never takes it back.
func (h *Handler) officeArtifactReceipts(ctx context.Context, eventID pgtype.UUID, d db.OfficeEventDelivery, req officeReceiptRequest) (map[string]any, string) {
	out := map[string]any{}
	if len(req.Artifacts) == 0 {
		return out, ""
	}
	ev, err := h.Queries.GetOfficeEvent(ctx, db.GetOfficeEventParams{EventID: eventID, WorkspaceID: d.WorkspaceID})
	if err != nil {
		return nil, "event not found"
	}
	catalogue := map[string]bool{}
	for _, a := range h.officeArtifacts(ctx, ev) {
		if id, _ := a["id"].(string); id != "" {
			catalogue[id] = true
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, a := range req.Artifacts {
		id, err := parseUUIDStrict(a.ID)
		if err != nil {
			return nil, "artifact id must be an attachment id"
		}
		key := uuidToString(id)
		if _, dup := out[key]; dup {
			return nil, "artifact " + key + " appears twice"
		}
		if !catalogue[key] {
			return nil, "artifact " + key + " is not a file of this event"
		}
		switch a.Status {
		case "sent":
			if strings.TrimSpace(a.Ref) == "" {
				return nil, "artifact " + key + ": sent needs ref (where the file went)"
			}
		case "failed":
			if strings.TrimSpace(a.Error) == "" {
				return nil, "artifact " + key + ": failed needs error"
			}
		case "skipped":
		default:
			return nil, "artifact status must be sent, failed or skipped"
		}
		out[key] = map[string]any{"status": a.Status, "ref": clip(a.Ref, 500), "error": clip(a.Error, 500), "at": now}
	}
	return out, ""
}

// ListBotOfficeDeliveries is a Bot's own ledger: what it was sent, what it
// acknowledged, what is blocked and why. Newest first; before= pages back.
func (h *Handler) ListBotOfficeDeliveries(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	h.writeOfficeDeliveries(w, r, bot.ID)
}

func (h *Handler) RetryBotOfficeDeliveries(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	h.retryOfficeDeliveries(w, r, bot.ID)
}

// ListMyOfficeDeliveries is the same ledger for the person whose Bot it is.
func (h *Handler) ListMyOfficeDeliveries(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.myOfficeBot(w, r)
	if !ok {
		return
	}
	h.writeOfficeDeliveries(w, r, bot)
}

func (h *Handler) RetryMyOfficeDeliveries(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.myOfficeBot(w, r)
	if !ok {
		return
	}
	h.retryOfficeDeliveries(w, r, bot)
}

func (h *Handler) myOfficeBot(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	ws, user, ok := h.officeMember(w, r)
	if !ok {
		return pgtype.UUID{}, false
	}
	channel, err := h.Queries.GetHumanChannel(r.Context(), db.GetHumanChannelParams{WorkspaceID: ws, UserID: user})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"deliveries": []any{}, "next_cursor": 0, "connected": false})
		return pgtype.UUID{}, false
	}
	return channel.BotID, true
}

func (h *Handler) writeOfficeDeliveries(w http.ResponseWriter, r *http.Request, bot pgtype.UUID) {
	qv := r.URL.Query()
	limit, _ := strconv.Atoi(qv.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	before, _ := strconv.ParseInt(qv.Get("before"), 10, 64)
	statuses := []string{}
	for _, s := range strings.Split(qv.Get("status"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			statuses = append(statuses, s)
		}
	}
	rows, err := h.Queries.ListOfficeDeliveries(r.Context(), db.ListOfficeDeliveriesParams{
		BotID: bot, Statuses: statuses, Before: before, Lim: int32(limit),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list deliveries")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	var next int64
	for _, row := range rows {
		d := h.officeDeliveryView(row.OfficeEventDelivery)
		ev := row.OfficeEvent
		d["event"] = map[string]any{
			"event_type": ev.EventType, "family": ev.Family, "meaning": ev.Meaning, "summary": ev.Summary,
			"stream_seq": ev.StreamSeq.Int64, "pending_approval": ev.PendingApproval,
			"issue_id": uuidToString(ev.IssueID), "chat_session_id": uuidToString(ev.ChatSessionID),
			"occurred_at": ev.OccurredAt.Time.UTC().Format(time.RFC3339Nano),
		}
		out = append(out, d)
		next = row.OfficeEventDelivery.ID
	}
	if len(rows) < limit {
		next = 0
	}
	writeScrubbed(w, (map[string]any{"deliveries": out, "next_cursor": next, "connected": true}))
}

func (h *Handler) retryOfficeDeliveries(w http.ResponseWriter, r *http.Request, bot pgtype.UUID) {
	var req struct {
		EventID string `json:"event_id"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	params := db.RetryOfficeDeliveriesParams{BotID: bot}
	if req.EventID != "" {
		id, ok := parseUUIDOrBadRequest(w, req.EventID, "event_id")
		if !ok {
			return
		}
		params.EventID = id
	}
	n, err := h.Queries.RetryOfficeDeliveries(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retry")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requeued": n})
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// writeScrubbed masks credential-looking values the same way the push does.
func writeScrubbed(w http.ResponseWriter, body map[string]any) {
	out, err := service.ScrubPayload(body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode response")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
