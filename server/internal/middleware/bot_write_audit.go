package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// BotWriteAudit is the one interceptor in front of every authenticated route
// that does two things for every write (DENE-1936), so neither is a convention
// that route 251 can forget:
//
//  1. A write made with a Bot's key is recorded as "Bot〈name〉on behalf of the
//     owner" (bot_write_audit), and if it is one of the four actions the owner
//     must approve first (delete the workspace, change members or roles, reset
//     keys, change quota or billing) it is stopped unless the request carries
//     the id of a go-ahead the owner gave personally (X-Owner-Confirmation):
//     the server opens a question card for the owner, only the owner's own
//     answer approves it, and one repeat of the exact call spends it.
//  2. Every successful write by anyone is handed to observe, which turns it
//     into a workspace event for the Bots that asked for the whole workspace.
//
// What is recorded is the route pattern and the names of the top-level body
// fields. Never a value, never a raw path (which can carry an id or a token).
//
// The route is only known once the request has been routed, so the decision to
// stop a dangerous write is made on method and URL path before `next`, and the
// record of an allowed one is made after it.

// ConfirmationHeader carries the id of the owner's go-ahead for one of the four
// dangerous actions. The id comes from the 403 that stopped the first attempt;
// it only works once the owner approved that card themselves, for that Bot,
// workspace and call, before it expired, and only once.
const ConfirmationHeader = "X-Owner-Confirmation"

// WriteObserver receives each successful write. It must not block for long.
type WriteObserver func(ctx context.Context, w service.WorkspaceWrite)

const (
	maxFieldScanBytes = 64 << 10
	maxResponseIDScan = 16 << 10
)

var (
	uuidSegmentRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	wsPathRe      = regexp.MustCompile(`^/api/workspaces/([0-9a-fA-F-]{36})(/|$)`)
	safeSegmentRe = regexp.MustCompile(`^[a-z][a-z0-9_\-]{0,31}$`)
)

func BotWriteAudit(queries *db.Queries, observe WriteObserver, confirmer service.OwnerConfirmer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isReadMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			bot := botsource.FromContext(r.Context())
			isBot := bot.ID.Valid
			if !isBot && observe == nil {
				next.ServeHTTP(w, r)
				return
			}

			scanned := scanBody(r)
			bodyIDs := scanned.ids
			rec := service.WorkspaceWrite{
				Method:    r.Method,
				Fields:    scanned.fields,
				Dangerous: service.DangerousBotAction(r.Method, r.URL.Path),
			}
			actor := botsource.ActorFromContext(r.Context())
			rec.ActorType, rec.ActorID = actor.Type, actor.ID
			rec.WorkspaceID = writeWorkspaceID(r, queries)
			var confirmation string
			if isBot {
				rec.BotID, rec.BotName = bot.ID, bot.Name
				owner, err := util.ParseUUID(r.Header.Get("X-User-ID"))
				if err != nil {
					// A Bot key always resolves to its owner; without one there
					// is no one to act for, so there is nothing to allow.
					writeError(w, http.StatusForbidden, "bot write has no owner")
					return
				}
				rec.OnBehalfOf = owner
				if rec.Dangerous != "" {
					var ok bool
					if confirmation, ok = authorizeDangerousWrite(w, r, queries, observe, confirmer, rec, scanned); !ok {
						return
					}
				}
			}

			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			var created idCapture
			if r.Method == http.MethodPost {
				ww.Tee(&created)
			}
			next.ServeHTTP(ww, r)

			rec.Status = ww.Status()
			if rec.Status == 0 {
				rec.Status = http.StatusOK
			}
			if rec.Status >= 400 {
				return // nothing was written
			}
			rctx := chi.RouteContext(r.Context())
			if rctx != nil && rctx.RoutePattern() != "" {
				rec.Route = rctx.RoutePattern()
			} else {
				rec.Route = sanitizePath(r.URL.Path)
			}
			if rctx != nil {
				for i := len(rctx.URLParams.Values) - 1; i >= 0; i-- {
					v := rctx.URLParams.Values[i]
					if uuidSegmentRe.MatchString(v) {
						if id, err := util.ParseUUID(v); err == nil && id != rec.WorkspaceID {
							if !rec.EntityID.Valid {
								rec.EntityID = id
							}
							rec.TargetIDs = append(rec.TargetIDs, id)
						}
					}
				}
			}
			// What the write was aimed at, for the access check: the ids in its
			// path, a few well-known body fields, and the id of what it created.
			rec.TargetIDs = append(rec.TargetIDs, bodyIDs...)
			if id := created.ID(); id.Valid {
				rec.TargetIDs = append(rec.TargetIDs, id)
				if !rec.EntityID.Valid {
					rec.EntityID = id
				}
			}
			recordAndObserve(r.Context(), queries, observe, rec, confirmation)
		})
	}
}

// recordAndObserve writes the audit row for a Bot write and hands the write on.
// It runs after the response was decided, detached from the request's
// cancellation, and never fails the request: an audit that cannot be written is
// logged loudly instead.
func recordAndObserve(parent context.Context, queries *db.Queries, observe WriteObserver, rec service.WorkspaceWrite, confirmation string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer cancel()
	if rec.BotID.Valid {
		outcome := "done"
		if rec.Blocked {
			outcome = "blocked"
		}
		var dangerous, conf pgtype.Text
		if rec.Dangerous != "" {
			dangerous = pgtype.Text{String: rec.Dangerous, Valid: true}
		}
		if confirmation != "" {
			conf = pgtype.Text{String: service.MaskSecrets(confirmation), Valid: true}
		}
		_, err := queries.InsertBotWriteAudit(ctx, db.InsertBotWriteAuditParams{
			WorkspaceID:      rec.WorkspaceID,
			BotID:            rec.BotID,
			BotName:          rec.BotName,
			OnBehalfOfUserID: rec.OnBehalfOf,
			Method:           strings.ToUpper(rec.Method),
			Route:            rec.Route,
			Family:           service.ClassifyWorkspaceRoute(rec.Route),
			EntityID:         rec.EntityID,
			Fields:           service.SafeFieldNames(rec.Fields),
			StatusCode:       int32(rec.Status),
			Outcome:          outcome,
			Dangerous:        dangerous,
			Confirmation:     conf,
		})
		if err != nil {
			slog.Error("bot write audit: insert failed", "error", err, "bot_id", util.UUIDToString(rec.BotID), "route", rec.Route)
		}
	}
	if observe != nil {
		observe(ctx, rec)
	}
}

// authorizeDangerousWrite is the door for the four actions that need the
// owner's go-ahead. It returns the id of the approval it spent, or answers the
// request itself (403) and returns false.
//
// The header must name a request the server holds: opened by this Bot for this
// workspace and owner, for this exact call (method, path, query, body values),
// approved by the owner personally, unexpired and not used. All of that is one
// atomic spend in the database, so a made-up id, someone else's, another call's,
// an old one, a replay, or one the Bot "approved" for itself all fail the same
// way. A failed attempt opens (or finds) the request and tells the owner once.
func authorizeDangerousWrite(w http.ResponseWriter, r *http.Request, queries *db.Queries, observe WriteObserver, confirmer service.OwnerConfirmer, rec service.WorkspaceWrite, scanned scannedBody) (string, bool) {
	if !rec.WorkspaceID.Valid {
		writeJSONError(w, http.StatusForbidden, map[string]string{
			"error": "this action needs the owner's go-ahead, and the request does not say which workspace it is for: send the X-Workspace-ID header",
			"code":  "owner_confirmation_workspace_required",
		})
		return "", false
	}
	if !scanned.complete {
		// The approval is for exactly this call; a body that cannot be read
		// whole cannot be bound to one, so it is refused rather than guessed.
		rec.Blocked, rec.Status = true, http.StatusForbidden
		rec.Route = sanitizePath(r.URL.Path)
		recordAndObserve(r.Context(), queries, observe, rec, "")
		writeJSONError(w, http.StatusForbidden, map[string]string{
			"error": "this action needs the owner's go-ahead for exactly this request, and its body could not be read whole: send it as one JSON document under 64 KB",
			"code":  "owner_confirmation_unscannable",
		})
		return "", false
	}
	scope := service.OwnerConfirmationScopeHash(r.Method, r.URL.Path, r.URL.RawQuery, scanned.canonical)
	if raw := strings.TrimSpace(r.Header.Get(ConfirmationHeader)); raw != "" {
		if id, err := util.ParseUUID(raw); err == nil {
			_, err := queries.ConsumeBotActionConfirmation(r.Context(), db.ConsumeBotActionConfirmationParams{
				ID: id, WorkspaceID: rec.WorkspaceID, BotID: rec.BotID, OwnerUserID: rec.OnBehalfOf, ScopeHash: scope,
			})
			if err == nil {
				return util.UUIDToString(id), true
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				slog.Error("bot write audit: spend confirmation failed", "error", err)
				writeJSONError(w, http.StatusInternalServerError, map[string]string{"error": "could not check the owner's go-ahead", "code": "owner_confirmation_unavailable"})
				return "", false
			}
		}
	}

	rec.Blocked, rec.Status = true, http.StatusForbidden
	rec.Route = sanitizePath(r.URL.Path)
	body := map[string]any{
		"code":   "owner_confirmation_required",
		"action": rec.Dangerous,
		"header": ConfirmationHeader,
	}
	if confirmer == nil {
		body["error"] = "this action needs the owner's go-ahead and this server cannot ask for it"
	} else {
		res, err := confirmer.EnsureOwnerConfirmation(r.Context(), service.OwnerConfirmationRequest{
			WorkspaceID: rec.WorkspaceID, BotID: rec.BotID, BotName: rec.BotName, OwnerUserID: rec.OnBehalfOf,
			Action: rec.Dangerous, Method: r.Method, Route: rec.Route, Fields: rec.Fields, ScopeHash: scope,
			Values:  service.OwnerConfirmationValues(scanned.canonical),
			Query:   service.OwnerConfirmationQueryText(r.URL.RawQuery),
			PathIDs: pathIDs(r.URL.Path, rec.WorkspaceID),
		})
		if err != nil {
			slog.Error("bot write audit: open owner confirmation failed", "error", err, "route", rec.Route)
			body["error"] = "this action needs the owner's go-ahead and asking for it failed; try again"
		} else {
			body["confirmation_id"] = util.UUIDToString(res.ID)
			body["ask_id"] = util.UUIDToString(res.AskID)
			body["status"] = res.Status
			body["expires_at"] = res.ExpiresAt.UTC().Format(time.RFC3339)
			switch res.Status {
			case service.OwnerConfirmApproved:
				body["error"] = "the owner approved this, but this call did not spend it: repeat the identical call with " + ConfirmationHeader + ": " + util.UUIDToString(res.ID) + " (CLI: MULTICA_OWNER_CONFIRMATION)"
			case service.OwnerConfirmDenied:
				body["code"] = "owner_confirmation_denied"
				body["error"] = "the owner said no to this action"
			default:
				body["error"] = "the owner has been asked (a question card in their Multica inbox). Only they can approve it, in the app or with `multica ask answer`; the Bot cannot answer for them. After they approve, repeat the identical call once with " + ConfirmationHeader + ": " + util.UUIDToString(res.ID) + " (CLI: MULTICA_OWNER_CONFIRMATION)"
			}
		}
	}
	recordAndObserve(r.Context(), queries, observe, rec, "")
	writeJSONError(w, http.StatusForbidden, body)
	return "", false
}

func writeJSONError(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	raw, _ := json.Marshal(body)
	_, _ = w.Write(raw)
}

// idCapture keeps the first bytes of a response so the id of what a POST created
// can be read back for the access check. Nothing else is kept.
type idCapture struct{ buf []byte }

func (c *idCapture) Write(p []byte) (int, error) {
	if room := maxResponseIDScan - len(c.buf); room > 0 {
		if len(p) > room {
			c.buf = append(c.buf, p[:room]...)
		} else {
			c.buf = append(c.buf, p...)
		}
	}
	return len(p), nil
}

// ID is the top-level "id" of a JSON object response, when there is one.
func (c *idCapture) ID() pgtype.UUID {
	var obj struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(c.buf, &obj) != nil {
		return pgtype.UUID{}
	}
	id, err := util.ParseUUID(obj.ID)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}

// writeWorkspaceID finds the workspace a write targets: the one in the path of
// the workspace routes, else wherever the request names it. A request names
// its workspace in headers and query strings anyone can set, so the id is only
// used when the caller is a member of that workspace; otherwise the write has
// no workspace and reaches nobody's Bot.
func writeWorkspaceID(r *http.Request, queries *db.Queries) pgtype.UUID {
	var ws pgtype.UUID
	if m := wsPathRe.FindStringSubmatch(r.URL.Path); m != nil {
		ws, _ = util.ParseUUID(m[1])
	}
	if !ws.Valid {
		if raw := ResolveWorkspaceIDFromRequest(r, queries); raw != "" {
			ws, _ = util.ParseUUID(raw)
		}
	}
	user, err := util.ParseUUID(r.Header.Get("X-User-ID"))
	if !ws.Valid || err != nil {
		return pgtype.UUID{}
	}
	if _, err := queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: user, WorkspaceID: ws}); err != nil {
		return pgtype.UUID{}
	}
	return ws
}

// sanitizePath keeps the shape of a path and drops everything that could be a
// value: ids become {id}, any other segment that is not a plain lowercase word
// becomes {x}.
func sanitizePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		switch {
		case s == "":
		case uuidSegmentRe.MatchString(s):
			parts[i] = "{id}"
		case !safeSegmentRe.MatchString(s):
			parts[i] = "{x}"
		}
	}
	return strings.Join(parts, "/")
}

// bodyTargetKeys are the body fields that point a write at a ticket, a project
// or a chat. Only their ids are read, and only for the access check.
var bodyTargetKeys = []string{"issue_id", "parent_issue_id", "project_id", "chat_session_id"}

// scannedBody is what the audit reads from a request body. Values are only
// kept as the canonical bytes the owner's approval is bound to; they are not
// recorded anywhere.
type scannedBody struct {
	fields    []string
	ids       []pgtype.UUID
	canonical []byte // CanonicalJSON of the body; nil for no body
	complete  bool   // the whole body was read and understood
}

// scanBody returns the top-level keys of a small JSON body, the ids it points
// at and its canonical form, and puts the body back for the handler.
func scanBody(r *http.Request) scannedBody {
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return scannedBody{complete: true}
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if ct != "" && !strings.Contains(ct, "json") {
		return scannedBody{}
	}
	head, err := io.ReadAll(io.LimitReader(r.Body, maxFieldScanBytes+1))
	rest := r.Body
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), rest), rest}
	if err != nil || len(head) > maxFieldScanBytes {
		return scannedBody{}
	}
	if len(bytes.TrimSpace(head)) == 0 {
		return scannedBody{complete: true}
	}
	canonical, ok := service.CanonicalJSON(head)
	if !ok {
		return scannedBody{}
	}
	out := scannedBody{canonical: canonical, complete: true}
	var obj map[string]json.RawMessage
	if json.Unmarshal(head, &obj) != nil {
		return out
	}
	names := make([]string, 0, len(obj))
	for k := range obj {
		names = append(names, k)
	}
	for _, k := range bodyTargetKeys {
		var raw string
		if json.Unmarshal(obj[k], &raw) != nil {
			continue
		}
		if id, err := util.ParseUUID(raw); err == nil {
			out.ids = append(out.ids, id)
		}
	}
	out.fields = service.SafeFieldNames(names)
	return out
}

// pathIDs are the ids in a raw path other than the workspace's own.
func pathIDs(path string, workspace pgtype.UUID) []pgtype.UUID {
	var out []pgtype.UUID
	for _, seg := range strings.Split(path, "/") {
		if !uuidSegmentRe.MatchString(seg) {
			continue
		}
		if id, err := util.ParseUUID(seg); err == nil && id != workspace {
			out = append(out, id)
		}
	}
	return out
}
