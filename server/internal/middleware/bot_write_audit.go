package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
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
//     the owner's go-ahead (X-Owner-Confirmation).
//  2. Every successful write by anyone is handed to observe, which turns it
//     into a workspace event for the Bots that asked for the whole workspace.
//
// What is recorded is the route pattern and the names of the top-level body
// fields. Never a value, never a raw path (which can carry an id or a token).
//
// The route is only known once the request has been routed, so the decision to
// stop a dangerous write is made on method and URL path before `next`, and the
// record of an allowed one is made after it.

// ConfirmationHeader carries the owner's go-ahead for the four dangerous
// actions: a short reference to the message in the Bot chat where the owner
// agreed. The server records it; it cannot read the chat, so it is the Bot's
// attestation, kept next to the write it licensed.
const ConfirmationHeader = "X-Owner-Confirmation"

// WriteObserver receives each successful write. It must not block for long.
type WriteObserver func(ctx context.Context, w service.WorkspaceWrite)

const (
	maxFieldScanBytes = 64 << 10
	maxConfirmation   = 200
)

var (
	uuidSegmentRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	wsPathRe      = regexp.MustCompile(`^/api/workspaces/([0-9a-fA-F-]{36})(/|$)`)
	safeSegmentRe = regexp.MustCompile(`^[a-z][a-z0-9_\-]{0,31}$`)
)

func BotWriteAudit(queries *db.Queries, observe WriteObserver) func(http.Handler) http.Handler {
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

			rec := service.WorkspaceWrite{
				Method:    r.Method,
				Fields:    bodyFieldNames(r),
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
					confirmation = cleanConfirmation(r.Header.Get(ConfirmationHeader))
					if confirmation == "" {
						rec.Blocked, rec.Status = true, http.StatusForbidden
						rec.Route = sanitizePath(r.URL.Path)
						recordAndObserve(r.Context(), queries, observe, rec, confirmation)
						writeConfirmationRequired(w, rec.Dangerous)
						return
					}
				}
			}

			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
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
							rec.EntityID = id
							break
						}
					}
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

func writeConfirmationRequired(w http.ResponseWriter, action string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	body, _ := json.Marshal(map[string]string{
		"error":  "this action needs the owner's go-ahead first: ask them in the Bot chat, then repeat the call with the " + ConfirmationHeader + " header naming their message",
		"code":   "owner_confirmation_required",
		"action": action,
		"header": ConfirmationHeader,
	})
	_, _ = w.Write(body)
}

// cleanConfirmation keeps a plain one-line reference. Anything else (empty,
// too long, control characters) counts as no confirmation.
func cleanConfirmation(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > maxConfirmation {
		return ""
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return s
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

// bodyFieldNames returns the top-level keys of a small JSON body and puts the
// body back for the handler. Values are parsed away and never kept.
func bodyFieldNames(r *http.Request) []string {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if ct != "" && !strings.Contains(ct, "json") {
		return nil
	}
	head, err := io.ReadAll(io.LimitReader(r.Body, maxFieldScanBytes+1))
	rest := r.Body
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), rest), rest}
	if err != nil || len(head) > maxFieldScanBytes {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(head, &obj) != nil {
		return nil
	}
	names := make([]string, 0, len(obj))
	for k := range obj {
		names = append(names, k)
	}
	return service.SafeFieldNames(names)
}
