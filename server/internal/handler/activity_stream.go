package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/repoident"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The Bot's side of the full activity stream (DENE-1935): the webhook body for
// one activity event, the `handled` receipt, and a pull list for a Bot that
// missed pushes. Pushing and delivery are the human-needed outbox's.

// activityPayloadFields completes the webhook body for an activity event. The
// envelope, issue/chat block and author were set by the caller; this adds the
// `activity` block and a one-line summary. Every free-text value is masked: the
// stream carries words people wrote, and none of it may carry a credential out.
func (h *Handler) activityPayloadFields(fields map[string]any, ev db.HumanNeededEvent, title, sourceURL string) (map[string]any, error) {
	var body service.ActivityBody
	if err := json.Unmarshal(ev.Activity, &body); err != nil {
		return nil, err
	}
	if body.Categories == nil {
		body.Categories = []string{}
	}
	targetKind := "issue"
	switch {
	case ev.ChatSessionID.Valid:
		targetKind = "chat"
	case !ev.IssueID.Valid:
		targetKind = "workspace"
	}
	fields["schema"] = service.ActivitySchema
	block := map[string]any{
		"activity_type":  body.ActivityType,
		"importance":     body.Importance,
		"categories":     body.Categories,
		"excerpt":        service.MaskSecrets(body.Excerpt),
		"source_kind":    targetKind,
		"source_title":   service.MaskSecrets(title),
		"source_url":     sourceURL,
		"source_id":      body.SourceID,
		"from_status":    body.FromStatus,
		"to_status":      body.ToStatus,
		"occurred_at":    body.OccurredAt,
		"target_user_id": uuidToString(ev.TargetUserID),
	}
	fields["activity"] = block
	if body.Workspace != nil {
		// Names and ids only; the scrub pass over the whole body still runs.
		block["workspace"] = body.Workspace
	}
	if author, ok := fields["author"].(map[string]any); ok {
		if name, ok := author["name"].(string); ok {
			author["name"] = service.MaskSecrets(name)
		}
	}
	if issue, ok := fields["issue"].(map[string]any); ok {
		if t, ok := issue["title"].(string); ok {
			issue["title"] = service.MaskSecrets(t)
		}
	}
	summary := body.Excerpt
	if title != "" {
		summary = title + "：" + summary
	}
	fields["summary"] = truncateRunes(service.MaskSecrets(summary), 180)
	return fields, nil
}

// HandledActivity is POST .../bots/{botId}/wake/events/{eventId}/handled: the
// Bot looked at an activity event and decided nobody needs telling (or told
// them through its own judgment). It closes the row; it is not required, since
// a pushed activity is delivered at HTTP 200.
func (h *Handler) HandledActivity(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "eventId"), "event id")
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid handled receipt")
		return
	}
	ev, found := h.humanNeededEventFor(r.Context(), bot, id)
	if !found || ev.EventType != service.HumanEventActivity {
		writeError(w, http.StatusNotFound, "activity event not found")
		return
	}
	reason := truncateRunes(strings.TrimSpace(req.Reason), 200)
	if reason == "" {
		reason = "handled by bot"
	}
	if err := h.Queries.RecordActivityHandled(r.Context(), db.RecordActivityHandledParams{EventID: ev.EventID, Reason: reason}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record receipt")
		return
	}
	saved, err := h.Queries.GetHumanNeededEventByID(r.Context(), ev.EventID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record receipt")
		return
	}
	writeJSON(w, http.StatusOK, humanNeededEventResp(saved))
}

// ListActivityForBot is GET .../bots/{botId}/activity: the stream rows still
// held for this Bot, newest first, for catching up after downtime. status
// defaults to the ones nobody has handled.
func (h *Handler) ListActivityForBot(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	statuses := []string{"queued", "sent", "woken", "relayed"}
	if raw := strings.TrimSpace(r.URL.Query().Get("status")); raw != "" {
		statuses = strings.Split(raw, ",")
	}
	limit := int32(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	before := pgtype.Int8{}
	if raw := r.URL.Query().Get("before"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			before = pgtype.Int8{Int64: n, Valid: true}
		}
	}
	minRank := ""
	if raw := strings.TrimSpace(r.URL.Query().Get("importance")); raw != "" {
		minRank = raw
	}
	// The list is filtered by what the person may see and what their stream
	// covers right now, not by what was true when a row was queued; a page that
	// filtering empties is followed by the next one, so a hidden run of rows
	// never hides the visible ones behind it.
	gate := newActivityGate(h, r.Context())
	out := make([]map[string]any, 0, limit)
	next := ""
	cursor := before
scan:
	for scans := 0; scans < activityListMaxScans; scans++ {
		rows, err := h.Queries.ListActivityForBot(r.Context(), db.ListActivityForBotParams{
			BotID: bot.ID, WorkspaceID: bot.WorkspaceID, Column3: statuses, Before: cursor, Limit: limit,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list activity")
			return
		}
		for _, ev := range rows {
			var body service.ActivityBody
			_ = json.Unmarshal(ev.Activity, &body)
			if !service.ActivityAtLeast(body.Importance, minRank) {
				continue
			}
			if ok, _ := gate.allows(ev); !ok {
				continue
			}
			if body.Categories == nil {
				body.Categories = []string{}
			}
			row := map[string]any{
				"event_id": uuidToString(ev.EventID), "sequence": ev.Sequence, "status": ev.Status,
				"activity_type": body.ActivityType, "importance": body.Importance, "categories": body.Categories,
				"excerpt": body.Excerpt, "from_status": body.FromStatus, "to_status": body.ToStatus,
				"issue_id": uuidToPtr(ev.IssueID), "chat_session_id": uuidToPtr(ev.ChatSessionID),
				"source_id": body.SourceID, "occurred_at": body.OccurredAt, "created_at": timestampToString(ev.CreatedAt),
			}
			if body.Workspace != nil {
				row["workspace"] = body.Workspace
			}
			out = append(out, row)
			if len(out) == int(limit) {
				next = strconv.FormatInt(ev.Sequence, 10)
				break scan
			}
		}
		if len(rows) < int(limit) {
			next = ""
			break
		}
		last := rows[len(rows)-1].Sequence
		cursor = pgtype.Int8{Int64: last, Valid: true}
		next = strconv.FormatInt(last, 10)
	}
	writeScrubbedJSON(w, map[string]any{"events": out, "next_cursor": next})
}

// activityListMaxScans bounds how many pages one catch-up request may read
// while filtering; the cursor it returns resumes where it stopped.
const activityListMaxScans = 10

// activityGate answers, for an activity row, whether it may still reach its
// person: the person is still a member who can see the ticket, and their
// binding is still live with a scope that covers the source. It runs where a
// row is sent and where a row is listed, so the two cannot disagree.
type activityGate struct {
	h       *Handler
	ctx     context.Context
	viewers map[pgtype.UUID]*visibilityViewer
	checked map[string]gateResult
}

type gateResult struct {
	ok     bool
	reason string
}

func newActivityGate(h *Handler, ctx context.Context) *activityGate {
	return &activityGate{h: h, ctx: ctx, viewers: map[pgtype.UUID]*visibilityViewer{}, checked: map[string]gateResult{}}
}

func (g *activityGate) allows(ev db.HumanNeededEvent) (bool, string) {
	family := workspaceFamilyOf(ev)
	key := "w:" + uuidToString(ev.BindingID) + ":" + family
	switch {
	case family != "":
		t := workspaceTargetOf(ev)
		key += ":" + uuidToString(t.IssueID) + ":" + uuidToString(t.ProjectID) + ":" + uuidToString(t.ChatSessionID) + ":" + t.Repo
		for _, id := range t.IssueIDs {
			key += ":" + uuidToString(id)
		}
	case ev.IssueID.Valid:
		key = "i:" + uuidToString(ev.IssueID) + ":" + uuidToString(ev.BindingID)
	case ev.ChatSessionID.Valid:
		key = "c:" + uuidToString(ev.ChatSessionID) + ":" + uuidToString(ev.BindingID)
	}
	if r, ok := g.checked[key]; ok {
		return r.ok, r.reason
	}
	ok, reason := g.decide(ev)
	g.checked[key] = gateResult{ok, reason}
	return ok, reason
}

// workspaceFamilyOf is the family of a workspace-level event ("" for the
// ticket and chat stream).
func workspaceFamilyOf(ev db.HumanNeededEvent) string {
	var body service.ActivityBody
	if json.Unmarshal(ev.Activity, &body) != nil || body.Workspace == nil {
		return ""
	}
	return body.Workspace.Family
}

// workspaceTargetOf is the ticket, project and chat a workspace-level event is
// about: what its person has to be able to open for it to be told.
func workspaceTargetOf(ev db.HumanNeededEvent) service.WriteTarget {
	t := service.WriteTarget{IssueID: ev.IssueID, ChatSessionID: ev.ChatSessionID}
	var body service.ActivityBody
	if json.Unmarshal(ev.Activity, &body) != nil || body.Workspace == nil {
		return t
	}
	w := body.Workspace
	if w.ProjectID != "" {
		if id, err := parseUUIDSafe(w.ProjectID); err == nil {
			t.ProjectID = id
		}
	}
	for _, raw := range w.IssueIDs {
		id, err := parseUUIDSafe(raw)
		if err != nil {
			// A ticket that cannot be read back cannot be judged: point at
			// nothing that exists, so the gate says no.
			id = pgtype.UUID{Bytes: [16]byte{0xff}, Valid: true}
		}
		t.IssueIDs = append(t.IssueIDs, id)
	}
	if w.Family == service.FamilyPR {
		t.Repo = w.Attrs["repo"]
	}
	return t
}

// TargetVisibility is the one answer to "could this person open the ticket,
// project or chat?" for a Bot's stream. Queueing a write, sending a row and
// listing rows for catch-up all call it, so they cannot disagree. A person who
// is not (or no longer) a member, or whose access cannot be established, sees
// nothing.
func (h *Handler) TargetVisibility() service.TargetVisibility {
	return func(ctx context.Context, ws, user pgtype.UUID, t service.WriteTarget) bool {
		v, err := h.visibilityViewerForUser(ctx, ws, user)
		if err != nil {
			return false
		}
		return h.targetVisibleTo(ctx, v, ws, t)
	}
}

// NewTargetVisibility is TargetVisibility for callers that have the queries but
// no handler (the event-bus listeners).
func NewTargetVisibility(q *db.Queries) service.TargetVisibility {
	return (&Handler{Queries: q}).TargetVisibility()
}

// targetVisibleTo checks every part of the target against the viewer. A chat is
// its opener's own; a ticket and a project follow the sharing rules the lists
// use. Anything that cannot be loaded, or sits in another workspace, is not
// visible.
func (h *Handler) targetVisibleTo(ctx context.Context, v visibilityViewer, ws pgtype.UUID, t service.WriteTarget) bool {
	for _, id := range append([]pgtype.UUID{t.IssueID}, t.IssueIDs...) {
		if !id.Valid {
			continue
		}
		issue, err := h.Queries.GetIssue(ctx, id)
		if err != nil || issue.WorkspaceID != ws || !v.canSeeIssue(issue) {
			return false
		}
	}
	if t.Repo != "" && !h.repoVisibleTo(ctx, v, ws, t.Repo) {
		return false
	}
	if t.ProjectID.Valid {
		project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: t.ProjectID, WorkspaceID: ws})
		if err != nil || !v.canSeeProject(project) {
			return false
		}
	}
	if t.ChatSessionID.Valid {
		chat, err := h.Queries.GetChatSessionInWorkspace(ctx, db.GetChatSessionInWorkspaceParams{ID: t.ChatSessionID, WorkspaceID: ws})
		if err != nil || !v.userID.Valid || chat.CreatorID != v.userID {
			return false
		}
	}
	return true
}

// repoVisibleTo checks "owner/name" against the workspace's repository
// registry. Every registered entry for that repository has to be one the viewer
// may see; a repository the workspace never registered has no sharing of its
// own and is judged by the tickets alone. An unreadable registry sees nothing.
func (h *Handler) repoVisibleTo(ctx context.Context, v visibilityViewer, ws pgtype.UUID, repo string) bool {
	workspace, err := h.Queries.GetWorkspace(ctx, ws)
	if err != nil {
		return false
	}
	suffix := "/" + strings.ToLower(strings.Trim(repo, "/"))
	for _, entry := range decodeWorkspaceRepos(workspace.Repos) {
		key := strings.ToLower(string(repoident.NormalizeURL(entry.URL)))
		if key == "" || !strings.HasSuffix(key, suffix) {
			continue
		}
		if !v.canSeeRepo(entry, h.repoProjectIDs(ctx, ws, entry.URL)) {
			return false
		}
	}
	return true
}

func (g *activityGate) viewer(ev db.HumanNeededEvent) (*visibilityViewer, bool) {
	if viewer, ok := g.viewers[ev.TargetUserID]; ok {
		return viewer, true
	}
	v, err := g.h.visibilityViewerForUser(g.ctx, ev.WorkspaceID, ev.TargetUserID)
	if err != nil {
		return nil, false
	}
	g.viewers[ev.TargetUserID] = &v
	return &v, true
}

func (g *activityGate) decide(ev db.HumanNeededEvent) (bool, string) {
	q := g.h.Queries
	if family := workspaceFamilyOf(ev); family != "" {
		// Workspace-level events: the person must still be a member whose role
		// may see this kind of event, the binding must still ask for the whole
		// workspace, and whatever the event is about (a ticket, a project, a
		// chat) must still be something they can open.
		member, mErr := q.GetMemberByUserAndWorkspace(g.ctx, db.GetMemberByUserAndWorkspaceParams{UserID: ev.TargetUserID, WorkspaceID: ev.WorkspaceID})
		if mErr != nil || !service.RoleCanSeeFamily(member.Role, family) {
			return false, "access unavailable"
		}
		all, aErr := q.ListWorkspaceActivityBindings(g.ctx, ev.WorkspaceID)
		if aErr != nil {
			return false, "access unavailable"
		}
		covered := false
		for _, b := range all {
			if b.ID == ev.BindingID {
				covered = true
				break
			}
		}
		if !covered {
			return false, "outside activity scope"
		}
		t := workspaceTargetOf(ev)
		if family == service.FamilyPR && !t.Established(family) {
			return false, "access unavailable" // a PR is read through its tickets; none named, none told
		}
		if !t.Any() {
			return true, ""
		}
		viewer, ok := g.viewer(ev)
		if !ok || !g.h.targetVisibleTo(g.ctx, *viewer, ev.WorkspaceID, t) {
			return false, "access unavailable"
		}
		return true, ""
	}
	var covering []db.MemberBotBinding
	var err error
	switch {
	case ev.IssueID.Valid:
		issue, getErr := q.GetIssue(g.ctx, ev.IssueID)
		if getErr != nil || issue.WorkspaceID != ev.WorkspaceID {
			return false, "access unavailable"
		}
		viewer, ok := g.viewer(ev)
		if !ok || !viewer.canSeeIssue(issue) {
			return false, "access unavailable"
		}
		covering, err = q.ListActivityBindingsForIssue(g.ctx, db.ListActivityBindingsForIssueParams{WorkspaceID: ev.WorkspaceID, ID: ev.IssueID})
	case ev.ChatSessionID.Valid:
		covering, err = q.ListActivityBindingsForChat(g.ctx, db.ListActivityBindingsForChatParams{WorkspaceID: ev.WorkspaceID, ID: ev.ChatSessionID})
	default:
		return false, "access unavailable"
	}
	if err != nil {
		return false, "access unavailable"
	}
	for _, b := range covering {
		if b.ID == ev.BindingID {
			return true, ""
		}
	}
	return false, "outside activity scope"
}
