package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// Explicitly opt in with the separately checked-out public client. This uses
// real TCP, handlers, journal and PostgreSQL; only login is injected at the
// existing authenticated test boundary. No public client code is vendored.
func TestFastAccessPublicClientIntegration(t *testing.T) {
	client := os.Getenv("MULTICA_PUBLIC_FAST_CLIENT")
	if client == "" {
		t.Skip("set MULTICA_PUBLIC_FAST_CLIENT to the public client.py")
	}
	client, err := filepath.Abs(client)
	if err != nil {
		t.Fatal(err)
	}
	ws2 := dbfx.Workspace(t, "Public client two", "public-client-"+randomID())
	dbfx.Member(t, ws2, testUserID, "owner")
	rt := dbfx.Runtime(t, "Public client runtime")
	agent := dbfx.Agent(t, "Public client agent", rt)
	issue := ""
	for i := 0; i < 200; i++ {
		ws := testWorkspaceID
		if i%2 == 1 {
			ws = ws2
		}
		id := dbfx.Issue(t, fmt.Sprintf("Public fixture %03d", i), testutil.Cols{"workspace_id": ws})
		if i == 0 {
			issue = id
		}
	}
	for i := 0; i < 17; i++ {
		dbfx.Task(t, agent, testutil.Cols{"runtime_id": rt, "status": "running"})
	}
	// Synthetic installation age makes since30m valid; production coverage is
	// never altered. All 200 issue changes were actually journalled above.
	fastMustExec(t, `UPDATE fast_access_meta SET coverage_from=now()-interval '1 hour',permission_changed_at=now()-interval '1 hour'`)
	handler := func(h *Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set("X-User-ID", testUserID)
			r = withChatTestWorkspaceCtx(t, r)
			// Measurements exclude rate limiting, as in TestFastAccessSnapshot.
			h.fastStore().Lock()
			h.fastStore().Quotas = map[string]fastQuota{}
			h.fastStore().Unlock()
			switch r.URL.Path {
			case "/api/fast-access/v1/read":
				h.ReadFastAccess(w, r)
			case "/api/fast-access/v1/events":
				h.FastAccessEvents(w, r)
			case "/fixture/change":
				title := r.URL.Query().Get("title")
				_, e := testPool.Exec(r.Context(), `UPDATE issue SET title=$2,updated_at=now() WHERE id=$1`, parseUUID(issue), title)
				if e != nil {
					http.Error(w, e.Error(), 500)
					return
				}
				json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			default:
				http.NotFound(w, r)
			}
		})
	}
	first := httptest.NewServer(handler(testHandler))
	defer first.Close()
	// Separate Handler has no in-memory cursor history and shares only database.
	secondHandler := New(testHandler.Queries, testPool, nil, nil, nil, nil, nil, nil, Config{})
	second := httptest.NewServer(handler(secondHandler))
	defer second.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "../../../scripts/verify-fast-access-client.py", client, first.URL, second.URL, testUserID, testWorkspaceID, ws2, issue, t.TempDir())
	out, err := cmd.CombinedOutput()
	t.Log(string(out))
	if err != nil {
		t.Fatal(err)
	}
}
