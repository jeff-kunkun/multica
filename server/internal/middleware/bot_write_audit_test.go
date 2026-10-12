package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	auditWS    = "11111111-1111-1111-1111-111111111111"
	auditOwner = "22222222-2222-2222-2222-222222222222"
	auditBot   = "33333333-3333-3333-3333-333333333333"
)

// auditDB answers the two queries the middleware makes: the membership check
// and the audit insert, and records what was inserted.
type auditDB struct {
	mu       sync.Mutex
	member   bool
	inserted [][]any
}

func (d *auditDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (d *auditDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	panic("unexpected Query")
}
func (d *auditDB) QueryRow(_ context.Context, sql string, args ...interface{}) pgx.Row {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case strings.Contains(sql, "bot_write_audit"):
		d.inserted = append(d.inserted, args)
		return auditRow{}
	case strings.Contains(sql, "FROM member"):
		if d.member {
			return auditRow{}
		}
		return auditRow{err: pgx.ErrNoRows}
	}
	panic("unexpected QueryRow: " + sql)
}

type auditRow struct{ err error }

func (r auditRow) Scan(...interface{}) error { return r.err }

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatal(err)
	}
	return u
}

type auditHarness struct {
	store    *auditDB
	observed []service.WorkspaceWrite
	handled  int
	status   int
	router   chi.Router
}

func newAuditHarness(t *testing.T, asBot bool) *auditHarness {
	t.Helper()
	h := &auditHarness{store: &auditDB{member: true}, status: http.StatusOK}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			req.Header.Set("X-User-ID", auditOwner)
			ctx := req.Context()
			if asBot {
				ctx = botsource.WithContext(ctx, botsource.Source{ID: mustUUID(t, auditBot), Name: "管家"})
			}
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Use(BotWriteAudit(db.New(h.store), func(_ context.Context, w service.WorkspaceWrite) {
		h.observed = append(h.observed, w)
	}))
	handler := func(w http.ResponseWriter, req *http.Request) {
		h.handled++
		w.WriteHeader(h.status)
	}
	r.Post("/api/workspaces/{id}/projects/{projectId}", handler)
	r.Patch("/api/workspaces/{id}/projects/{projectId}", handler)
	r.Get("/api/workspaces/{id}/projects", handler)
	r.Delete("/api/workspaces/{id}", handler)
	r.Post("/api/workspaces/{id}/members", handler)
	h.router = r
	return h
}

func (h *auditHarness) do(method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func TestBotWriteAuditRecordsBotWritesAsOnBehalfOfTheOwner(t *testing.T) {
	h := newAuditHarness(t, true)
	path := "/api/workspaces/" + auditWS + "/projects/44444444-4444-4444-4444-444444444444"
	rec := h.do("PATCH", path, `{"title":"a secret-looking value sk-abcdefghijklmnopqrstuvwx","password":"hunter2"}`, nil)
	if rec.Code != http.StatusOK || h.handled != 1 {
		t.Fatalf("status %d handled %d", rec.Code, h.handled)
	}
	if len(h.store.inserted) != 1 {
		t.Fatalf("audit rows = %d", len(h.store.inserted))
	}
	row := h.store.inserted[0]
	if row[1] != "管家" || row[4] != "/api/workspaces/{id}/projects/{projectId}" || row[8] != "done" {
		t.Fatalf("audit row = %v", row)
	}
	if owner := row[2].(pgtype.UUID); owner != mustUUID(t, auditOwner) {
		t.Fatalf("on behalf of %v", owner)
	}
	if fields := row[6].([]string); strings.Join(fields, ",") != "password,title" {
		t.Fatalf("fields = %v (names only)", fields)
	}
	if len(h.observed) != 1 || h.observed[0].BotName != "管家" || !h.observed[0].OnBehalfOf.Valid {
		t.Fatalf("observed = %+v", h.observed)
	}
	// Names only: no value of the body is anywhere in what was recorded.
	dump := fmt.Sprintf("%+v %+v", h.store.inserted, h.observed)
	for _, leak := range []string{"hunter2", "sk-abcdefghijklmnopqrstuvwx", "secret-looking"} {
		if strings.Contains(dump, leak) {
			t.Fatalf("record leaks body value %q: %s", leak, dump)
		}
	}
}

func TestBotWriteAuditStopsTheFourDangerousActionsWithoutConfirmation(t *testing.T) {
	cases := []struct{ method, path, action string }{
		{"DELETE", "/api/workspaces/" + auditWS, service.DangerDeleteWorkspace},
		{"POST", "/api/workspaces/" + auditWS + "/members", service.DangerChangeMembers},
	}
	for _, c := range cases {
		t.Run(c.action, func(t *testing.T) {
			h := newAuditHarness(t, true)
			rec := h.do(c.method, c.path, `{}`, nil)
			if rec.Code != http.StatusForbidden || h.handled != 0 {
				t.Fatalf("status %d handled %d", rec.Code, h.handled)
			}
			if !strings.Contains(rec.Body.String(), "owner_confirmation_required") || !strings.Contains(rec.Body.String(), c.action) {
				t.Fatalf("body = %s", rec.Body)
			}
			if len(h.store.inserted) != 1 || h.store.inserted[0][8] != "blocked" || h.store.inserted[0][11].(pgtype.Text).String != c.action {
				t.Fatalf("audit = %v", h.store.inserted)
			}
			if len(h.observed) != 1 || !h.observed[0].Blocked {
				t.Fatalf("a blocked attempt must reach the owner's Bot: %+v", h.observed)
			}

			// With the owner's go-ahead the call goes through and the reference is kept.
			h = newAuditHarness(t, true)
			rec = h.do(c.method, c.path, `{}`, map[string]string{ConfirmationHeader: "chat message 42, Kun said yes"})
			if rec.Code != http.StatusOK || h.handled != 1 {
				t.Fatalf("confirmed: status %d handled %d", rec.Code, h.handled)
			}
			row := h.store.inserted[0]
			if row[8] != "done" || !strings.Contains(row[12].(pgtype.Text).String, "Kun said yes") {
				t.Fatalf("confirmed audit = %v", row)
			}
		})
	}
}

func TestBotWriteAuditRejectsMalformedConfirmation(t *testing.T) {
	for _, bad := range []string{" ", strings.Repeat("x", maxConfirmation+1), "line\nbreak"} {
		h := newAuditHarness(t, true)
		rec := h.do("DELETE", "/api/workspaces/"+auditWS, "", map[string]string{ConfirmationHeader: bad})
		if rec.Code != http.StatusForbidden || h.handled != 0 {
			t.Fatalf("confirmation %q: status %d handled %d", bad, rec.Code, h.handled)
		}
	}
}

func TestBotWriteAuditPeopleAreNotBlockedAndNotAudited(t *testing.T) {
	h := newAuditHarness(t, false)
	rec := h.do("DELETE", "/api/workspaces/"+auditWS, "", nil)
	if rec.Code != http.StatusOK || h.handled != 1 {
		t.Fatalf("a person's own call must go through: %d", rec.Code)
	}
	if len(h.store.inserted) != 0 {
		t.Fatalf("no Bot, no Bot audit row: %v", h.store.inserted)
	}
	if len(h.observed) != 1 || h.observed[0].BotID.Valid {
		t.Fatalf("observed = %+v", h.observed)
	}
}

func TestBotWriteAuditSkipsReadsAndFailures(t *testing.T) {
	h := newAuditHarness(t, true)
	h.do("GET", "/api/workspaces/"+auditWS+"/projects", "", nil)
	h.status = http.StatusInternalServerError
	h.do("POST", "/api/workspaces/"+auditWS+"/projects/44444444-4444-4444-4444-444444444444", `{"title":"x"}`, nil)
	if len(h.observed) != 0 || len(h.store.inserted) != 0 {
		t.Fatalf("reads and failed writes change nothing: %+v %v", h.observed, h.store.inserted)
	}
}

func TestBotWriteAuditIgnoresAWorkspaceTheCallerIsNotIn(t *testing.T) {
	h := newAuditHarness(t, false)
	h.store.member = false
	h.do("POST", "/api/workspaces/"+auditWS+"/projects/44444444-4444-4444-4444-444444444444", `{"title":"x"}`, nil)
	if len(h.observed) != 1 || h.observed[0].WorkspaceID.Valid {
		t.Fatalf("a non-member must not put events into that workspace: %+v", h.observed)
	}
}

func TestSanitizePathNeverKeepsAnIdOrToken(t *testing.T) {
	got := sanitizePath("/api/workspaces/" + auditWS + "/plugins/Zm9vYmFyYmF6/token")
	if got != "/api/workspaces/{id}/plugins/{x}/token" {
		t.Fatalf("sanitizePath = %q", got)
	}
}
