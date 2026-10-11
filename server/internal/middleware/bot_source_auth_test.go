package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/botsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type botAuthDB struct {
	token   db.PersonalAccessToken
	revoked bool
	reads   int
}

func (d *botAuthDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (d *botAuthDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	panic("unexpected Query")
}
func (d *botAuthDB) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	d.reads++
	return botAuthRow{d.token, d.revoked}
}

type botAuthRow struct {
	token   db.PersonalAccessToken
	revoked bool
}

func (r botAuthRow) Scan(dest ...interface{}) error {
	if r.revoked {
		return pgx.ErrNoRows
	}
	fields := reflect.ValueOf(r.token)
	for i, p := range dest {
		reflect.ValueOf(p).Elem().Set(fields.Field(i))
	}
	return nil
}

func TestBotAuthProvenance(t *testing.T) {
	for _, daemon := range []bool{false, true} {
		name := "regular"
		if daemon {
			name = "daemon"
		}
		t.Run(name, func(t *testing.T) {
			for _, cached := range []bool{false, true} {
				label := "miss"
				if cached {
					label = "hit"
				}
				t.Run(label, func(t *testing.T) {
					var cache *auth.PATCache
					if cached {
						cache = auth.NewPATCache(newRedisTestClient(t))
					}
					var id, user pgtype.UUID
					_ = id.Scan("11111111-1111-1111-1111-111111111111")
					_ = user.Scan("22222222-2222-2222-2222-222222222222")
					store := &botAuthDB{token: db.PersonalAccessToken{ID: id, UserID: user, Name: "Grok", Kind: "bot"}}
					token := "mul_bot-test"
					hash := auth.HashToken(token)
					cache.SetIdentity(context.Background(), hash, auth.PATIdentity{UserID: uuidToString(user), TokenID: uuidToString(id), Kind: "bot", Name: "Stale name"}, auth.AuthCacheTTL)
					calls := 0
					next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						actor := botsource.ActorFromContext(r.Context())
						if actor.Type != "member" || actor.ID != user {
							t.Fatalf("wrong authenticated actor: %#v", actor)
						}
						source := botsource.FromContext(r.Context())
						if source.ID != id || source.Name != "Grok" {
							t.Fatalf("wrong trusted source: %#v", source)
						}
						if r.Header.Get("X-Actor-Bot-ID") != "" || r.Header.Get("X-Actor-Bot-Name") != "" {
							t.Fatal("client headers survived")
						}
						if r.Header.Get("X-User-ID") != uuidToString(user) {
							t.Fatal("owner permissions changed")
						}
					})
					handler := Auth(db.New(store), cache, nil, nil)(next)
					if daemon {
						handler = DaemonAuth(db.New(store), cache, nil, nil)(next)
					}
					request := func() int {
						r := httptest.NewRequest("GET", "/api/me", nil)
						r.Header.Set("Authorization", "Bearer "+token)
						r.Header.Set("X-Actor-Bot-ID", "forged")
						r.Header.Set("X-Actor-Bot-Name", "forged")
						w := httptest.NewRecorder()
						handler.ServeHTTP(w, r)
						return w.Code
					}
					if got := request(); got != 200 {
						t.Fatalf("active status=%d", got)
					}
					store.revoked = true // Redis still contains the credential; DB must win.
					if got := request(); got != 401 {
						t.Fatalf("revoked status=%d", got)
					}
					if calls != 1 || store.reads != 2 {
						t.Fatalf("calls=%d reads=%d", calls, store.reads)
					}
				})
			}
		})
	}
}

func TestHumanAuthCannotForgeBotSource(t *testing.T) {
	for _, daemon := range []bool{false, true} {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if botsource.FromContext(r.Context()).ID.Valid {
				t.Fatal("human became Bot")
			}
			if r.Header.Get("X-Actor-Bot-ID") != "" || r.Header.Get("X-Actor-Bot-Name") != "" {
				t.Fatal("forged header survived")
			}
		})
		h := Auth(nil, nil, nil, nil)(next)
		if daemon {
			h = DaemonAuth(nil, nil, nil, nil)(next)
		}
		r := httptest.NewRequest("GET", "/api/me", nil)
		r.Header.Set("Authorization", "Bearer "+generateToken(validClaims(), auth.JWTSecret()))
		r.Header.Set("X-Actor-Bot-ID", "forged")
		r.Header.Set("X-Actor-Bot-Name", "forged")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("human status=%d", w.Code)
		}
	}
}

func TestAuthenticatedActorRejectsSpoofedUserOnDaemonToken(t *testing.T) {
	cache := auth.NewDaemonTokenCache(newRedisTestClient(t))
	token := "mdt_actor-test"
	cache.Set(context.Background(), auth.HashToken(token), auth.DaemonTokenIdentity{WorkspaceID: "workspace", DaemonID: "daemon"}, auth.AuthCacheTTL)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := botsource.ActorFromContext(r.Context()); got.ID.Valid || got.Type != "" {
			t.Fatalf("daemon fabricated actor: %#v", got)
		}
		if r.Header.Get("X-User-ID") != "" {
			t.Fatal("client user header survived")
		}
	})
	r := httptest.NewRequest("GET", "/api/daemon/tasks", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-User-ID", "22222222-2222-2222-2222-222222222222")
	w := httptest.NewRecorder()
	DaemonAuth(nil, nil, cache, nil)(next).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}
