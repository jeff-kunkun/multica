package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

// auditDB answers the queries the middleware makes: the membership check, the
// audit insert and the spend of an owner's go-ahead, and records what was
// inserted. approvals maps a confirmation id to the call (scope hash) the owner
// approved it for; spending removes it, as the real UPDATE sets used_at.
type auditDB struct {
	mu        sync.Mutex
	member    bool
	inserted  [][]any
	approvals map[string]string
	spends    [][]any
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
	case strings.Contains(sql, "UPDATE bot_action_confirmation"):
		d.spends = append(d.spends, args)
		id, _ := args[0].(pgtype.UUID)
		key := uuidKey(id)
		scope, _ := args[4].(string)
		if want, ok := d.approvals[key]; ok && want == scope &&
			args[1] == mustUUIDNoT(auditWS) && args[2] == mustUUIDNoT(auditBot) && args[3] == mustUUIDNoT(auditOwner) {
			delete(d.approvals, key)
			return auditRow{}
		}
		return auditRow{err: pgx.ErrNoRows}
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

func uuidKey(u pgtype.UUID) string {
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func mustUUIDNoT(s string) pgtype.UUID {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		panic(err)
	}
	return u
}

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatal(err)
	}
	return u
}

const auditConfirmation = "55555555-5555-5555-5555-555555555555"

// fakeConfirmer stands in for the handler that opens the owner's card.
type fakeConfirmer struct {
	calls  []service.OwnerConfirmationRequest
	status string
}

func (f *fakeConfirmer) EnsureOwnerConfirmation(_ context.Context, req service.OwnerConfirmationRequest) (service.OwnerConfirmation, error) {
	f.calls = append(f.calls, req)
	status := f.status
	if status == "" {
		status = service.OwnerConfirmPending
	}
	return service.OwnerConfirmation{
		ID: mustUUIDNoT(auditConfirmation), AskID: mustUUIDNoT("66666666-6666-6666-6666-666666666666"),
		Status: status, ExpiresAt: time.Now().Add(service.OwnerConfirmationTTL), Created: len(f.calls) == 1,
	}, nil
}

type auditHarness struct {
	confirmer *fakeConfirmer
	store     *auditDB
	observed  []service.WorkspaceWrite
	handled   int
	status    int
	router    chi.Router
}

func newAuditHarness(t *testing.T, asBot bool) *auditHarness {
	t.Helper()
	h := &auditHarness{store: &auditDB{member: true, approvals: map[string]string{}}, status: http.StatusOK, confirmer: &fakeConfirmer{}}
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
	}, h.confirmer))
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

// approve is what the owner's own click does: it makes the confirmation
// spendable for exactly one call, body values included.
func (h *auditHarness) approve(method, path, body string) {
	h.store.approvals[auditConfirmation] = scopeOf(method, path, body)
}

func scopeOf(method, path, body string) string {
	var canonical []byte
	if body != "" {
		canonical, _ = service.CanonicalJSON([]byte(body))
	}
	p, q, _ := strings.Cut(path, "?")
	return service.OwnerConfirmationScopeHash(method, p, q, canonical)
}

func TestBotWriteAuditStopsTheFourDangerousActionsUntilTheOwnerApproves(t *testing.T) {
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
			for _, want := range []string{"owner_confirmation_required", c.action, auditConfirmation, "ask_id"} {
				if !strings.Contains(rec.Body.String(), want) {
					t.Fatalf("body lacks %q: %s", want, rec.Body)
				}
			}
			if len(h.confirmer.calls) != 1 || h.confirmer.calls[0].ScopeHash != scopeOf(c.method, c.path, `{}`) {
				t.Fatalf("the owner must be asked once for this exact call: %+v", h.confirmer.calls)
			}
			if len(h.store.inserted) != 1 || h.store.inserted[0][8] != "blocked" || h.store.inserted[0][11].(pgtype.Text).String != c.action {
				t.Fatalf("audit = %v", h.store.inserted)
			}
			if len(h.observed) != 1 || !h.observed[0].Blocked {
				t.Fatalf("a blocked attempt is still a recorded event: %+v", h.observed)
			}

			// The owner approves that card: the repeat goes through and the
			// audit keeps the id of the approval it spent.
			h = newAuditHarness(t, true)
			h.approve(c.method, c.path, `{}`)
			rec = h.do(c.method, c.path, `{}`, map[string]string{ConfirmationHeader: auditConfirmation})
			if rec.Code != http.StatusOK || h.handled != 1 {
				t.Fatalf("approved: status %d handled %d body %s", rec.Code, h.handled, rec.Body)
			}
			row := h.store.inserted[0]
			if row[8] != "done" || !strings.Contains(row[12].(pgtype.Text).String, auditConfirmation) {
				t.Fatalf("approved audit = %v", row)
			}
			if len(h.confirmer.calls) != 0 {
				t.Fatalf("an approved call must not open another card: %+v", h.confirmer.calls)
			}
		})
	}
}

// The header is only ever an id the server can find and spend. Anything the Bot
// can make up, or reuse, or point at another call, is the same refusal.
func TestBotWriteAuditRefusesFabricatedWrongObjectAndReplayedConfirmations(t *testing.T) {
	path := "/api/workspaces/" + auditWS
	other := "/api/workspaces/" + auditWS + "/members"

	t.Run("free text the Bot wrote", func(t *testing.T) {
		h := newAuditHarness(t, true)
		for _, bad := range []string{"chat message 42, Kun said yes", " ", strings.Repeat("x", 500), "line\nbreak"} {
			rec := h.do("DELETE", path, "", map[string]string{ConfirmationHeader: bad})
			if rec.Code != http.StatusForbidden || h.handled != 0 {
				t.Fatalf("confirmation %q: status %d handled %d", bad, rec.Code, h.handled)
			}
		}
		if len(h.store.spends) != 0 {
			t.Fatalf("text that is not an id must never reach the database: %v", h.store.spends)
		}
	})
	t.Run("an id nobody approved", func(t *testing.T) {
		h := newAuditHarness(t, true)
		rec := h.do("DELETE", path, "", map[string]string{ConfirmationHeader: auditConfirmation})
		if rec.Code != http.StatusForbidden || h.handled != 0 {
			t.Fatalf("status %d handled %d", rec.Code, h.handled)
		}
	})
	t.Run("approved for another call", func(t *testing.T) {
		h := newAuditHarness(t, true)
		h.approve("POST", other, "")
		rec := h.do("DELETE", path, "", map[string]string{ConfirmationHeader: auditConfirmation})
		if rec.Code != http.StatusForbidden || h.handled != 0 {
			t.Fatalf("a go-ahead for the members call must not delete the workspace: %d", rec.Code)
		}
		if _, still := h.store.approvals[auditConfirmation]; !still {
			t.Fatal("a refused attempt must not spend the approval")
		}
	})
	t.Run("different fields", func(t *testing.T) {
		h := newAuditHarness(t, true)
		h.approve("POST", other, `{"role":"admin"}`)
		rec := h.do("POST", other, `{"role":"admin","email":"x@y.z"}`, map[string]string{ConfirmationHeader: auditConfirmation})
		if rec.Code != http.StatusForbidden || h.handled != 0 {
			t.Fatalf("an approval is for the fields it showed: %d", rec.Code)
		}
	})
	t.Run("replayed", func(t *testing.T) {
		h := newAuditHarness(t, true)
		h.approve("DELETE", path, "")
		if rec := h.do("DELETE", path, "", map[string]string{ConfirmationHeader: auditConfirmation}); rec.Code != http.StatusOK {
			t.Fatalf("first use: %d", rec.Code)
		}
		rec := h.do("DELETE", path, "", map[string]string{ConfirmationHeader: auditConfirmation})
		if rec.Code != http.StatusForbidden || h.handled != 1 {
			t.Fatalf("second use: status %d handled %d", rec.Code, h.handled)
		}
	})
}

func TestBotWriteAuditToldWhenTheOwnerSaidNo(t *testing.T) {
	h := newAuditHarness(t, true)
	h.confirmer.status = service.OwnerConfirmDenied
	rec := h.do("DELETE", "/api/workspaces/"+auditWS, "", nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "owner_confirmation_denied") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestBotWriteAuditCapturesTheTargetsOfAWrite(t *testing.T) {
	h := newAuditHarness(t, false)
	proj := "44444444-4444-4444-4444-444444444444"
	issue := "77777777-7777-7777-7777-777777777777"
	h.do("PATCH", "/api/workspaces/"+auditWS+"/projects/"+proj, `{"title":"x","issue_id":"`+issue+`"}`, nil)
	if len(h.observed) != 1 {
		t.Fatalf("observed = %+v", h.observed)
	}
	got := map[string]bool{}
	for _, id := range h.observed[0].TargetIDs {
		got[uuidKey(id)] = true
	}
	if !got[uuidKey(mustUUID(t, proj))] || !got[uuidKey(mustUUID(t, issue))] {
		t.Fatalf("targets = %+v: the project in the path and the issue in the body must both be kept", h.observed[0].TargetIDs)
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

// R1 (second review): the owner approves what the call does, not its shape. The
// same fields with other values, another query, or a body that cannot be read
// whole do not spend the approval; the identical call does, once.
func TestBotWriteAuditApprovalIsForTheExactContent(t *testing.T) {
	members := "/api/workspaces/" + auditWS + "/members"
	alice := `{"email":"alice@example.invalid","role":"member"}`

	t.Run("same fields, another member and role", func(t *testing.T) {
		h := newAuditHarness(t, true)
		h.approve("POST", members, alice)
		rec := h.do("POST", members, `{"email":"mallory@example.invalid","role":"admin"}`, map[string]string{ConfirmationHeader: auditConfirmation})
		if rec.Code != http.StatusForbidden || h.handled != 0 {
			t.Fatalf("Mallory/admin spent Alice/member's approval: status %d handled %d", rec.Code, h.handled)
		}
		if _, still := h.store.approvals[auditConfirmation]; !still {
			t.Fatal("a refused attempt must not spend the approval")
		}
		if len(h.confirmer.calls) != 1 || h.confirmer.calls[0].ScopeHash == scopeOf("POST", members, alice) {
			t.Fatalf("the changed call must ask the owner again, as a new card: %+v", h.confirmer.calls)
		}
	})
	t.Run("same fields, another amount", func(t *testing.T) {
		h := newAuditHarness(t, true)
		h.approve("POST", members, `{"seats":5}`)
		if rec := h.do("POST", members, `{"seats":500}`, map[string]string{ConfirmationHeader: auditConfirmation}); rec.Code != http.StatusForbidden {
			t.Fatalf("another amount went through: %d", rec.Code)
		}
	})
	t.Run("another query", func(t *testing.T) {
		h := newAuditHarness(t, true)
		h.approve("POST", members+"?role=member", alice)
		if rec := h.do("POST", members+"?role=admin", alice, map[string]string{ConfirmationHeader: auditConfirmation}); rec.Code != http.StatusForbidden {
			t.Fatalf("another query went through: %d", rec.Code)
		}
	})
	t.Run("a body too big to read whole", func(t *testing.T) {
		h := newAuditHarness(t, true)
		big := `{"email":"alice@example.invalid","pad":"` + strings.Repeat("x", maxFieldScanBytes) + `"}`
		h.approve("POST", members, big)
		rec := h.do("POST", members, big, map[string]string{ConfirmationHeader: auditConfirmation})
		if rec.Code != http.StatusForbidden || h.handled != 0 || !strings.Contains(rec.Body.String(), "owner_confirmation_unscannable") {
			t.Fatalf("an unreadable body must be refused: status %d body %s", rec.Code, rec.Body)
		}
		if len(h.store.spends) != 0 || len(h.confirmer.calls) != 0 {
			t.Fatal("an unreadable body neither spends nor asks")
		}
	})
	t.Run("a body that is not JSON", func(t *testing.T) {
		h := newAuditHarness(t, true)
		if rec := h.do("POST", members, `email=alice`, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "owner_confirmation_unscannable") {
			t.Fatalf("status %d body %s", rec.Code, rec.Body)
		}
	})
	t.Run("a key named twice is refused, whatever its case or depth", func(t *testing.T) {
		for name, body := range map[string]string{
			"case alias":          `{"email":"alice@example.invalid","Role":"admin","role":"member"}`,
			"case alias, swapped": `{"email":"alice@example.invalid","role":"member","Role":"admin"}`,
			"exact repeat":        `{"email":"alice@example.invalid","role":"member","role":"admin"}`,
			"nested":              `{"plan":{"seats":5,"SEATS":500}}`,
			"inside an array":     `{"items":[{"id":1},{"role":"member","ROLE":"admin"}]}`,
			"kelvin sign":         "{\"kind\":\"a\",\"\u212aind\":\"b\"}",
		} {
			h := newAuditHarness(t, true)
			h.approve("POST", members, alice)
			rec := h.do("POST", members, body, map[string]string{ConfirmationHeader: auditConfirmation})
			if rec.Code != http.StatusForbidden || h.handled != 0 || !strings.Contains(rec.Body.String(), "owner_confirmation_unscannable") {
				t.Fatalf("%s: status %d handled %d body %s", name, rec.Code, h.handled, rec.Body)
			}
			if len(h.store.spends) != 0 || len(h.confirmer.calls) != 0 {
				t.Fatalf("%s: an ambiguous body neither spends nor asks", name)
			}
		}
		h := newAuditHarness(t, true)
		rec := h.do("POST", members, `{"a":{"x":1},"b":{"x":2},"c":[{"x":3},{"x":4}]}`, nil)
		if strings.Contains(rec.Body.String(), "owner_confirmation_unscannable") || len(h.confirmer.calls) != 1 {
			t.Fatalf("the same key in different objects is not a repeat: %s", rec.Body)
		}
	})
	t.Run("the identical call, reordered, goes through once", func(t *testing.T) {
		h := newAuditHarness(t, true)
		h.approve("POST", members, alice)
		same := `{ "role":"member", "email":"alice@example.invalid" }`
		if rec := h.do("POST", members, same, map[string]string{ConfirmationHeader: auditConfirmation}); rec.Code != http.StatusOK || h.handled != 1 {
			t.Fatalf("the approved call: status %d handled %d", rec.Code, h.handled)
		}
		if rec := h.do("POST", members, same, map[string]string{ConfirmationHeader: auditConfirmation}); rec.Code != http.StatusForbidden || h.handled != 1 {
			t.Fatalf("a second use: status %d", rec.Code)
		}
	})
	t.Run("the card shows what is approved, credentials masked", func(t *testing.T) {
		h := newAuditHarness(t, true)
		h.do("POST", members+"?notify=yes", `{"email":"alice@example.invalid","role":"member","password":"hunter2-Very-Secret"}`, nil)
		if len(h.confirmer.calls) != 1 {
			t.Fatalf("calls = %+v", h.confirmer.calls)
		}
		q := service.OwnerConfirmationQuestion(h.confirmer.calls[0])
		for _, want := range []string{"email=alice@example.invalid", "role=member", "notify=yes"} {
			if !strings.Contains(q, want) {
				t.Fatalf("card lacks %q: %s", want, q)
			}
		}
		if strings.Contains(q, "hunter2") || strings.Contains(fmt.Sprintf("%+v %+v", h.store.inserted, h.observed), "alice@example.invalid") {
			t.Fatalf("a credential reached the card, or a value reached the audit: %s", q)
		}
	})
}
