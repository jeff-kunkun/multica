package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/fastmcp"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestCommentDirectoryMCPRealHandler(t *testing.T) {
	issue := dbfx.Issue(t, "paged root directory")
	want := map[string]bool{}
	for i := 0; i < 105; i++ {
		want[dbfx.Comment(t, issue, strings.Repeat("字", 200))] = true
	}
	// Use the production adapter, transport budget and production comment handler.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := withURLParam(newRequest("GET", r.URL.String(), nil), "id", issue)
		rec := httptest.NewRecorder()
		testHandler.ListComments(rec, req)
		if rec.Body.Len() > 50000 {
			t.Errorf("upstream response %d exceeds budget", rec.Body.Len())
		}
		for k, vs := range rec.Header() {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	defer server.Close()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := fastmcp.NewServer(cli.NewAPIClient(server.URL, testWorkspaceID, "mat_fixture"), t.TempDir()).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "directory-test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	read := func(offset, limit int) map[string]any {
		t.Helper()
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "comments_list", Arguments: map[string]any{"id": issue, "limit": limit, "offset": offset}})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		if len(result.Content[0].(*mcp.TextContent).Text) > 50000 {
			t.Fatal("MCP logical payload exceeds budget")
		}
		if result.IsError {
			t.Fatalf("tool failed: %s", raw)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	seen := map[string]bool{}
	offset := 0
	for n := 0; ; n++ {
		if n > 105 {
			t.Fatal("infinite directory")
		}
		out := read(offset, 1)
		rows := out["comments"].([]any)
		if len(rows) != 1 {
			t.Fatalf("small page: %v", out)
		}
		id := rows[0].(map[string]any)["id"].(string)
		if !want[id] || seen[id] {
			t.Fatal("missing/duplicate root", id)
		}
		seen[id] = true
		next, more := out["next_offset"]
		if !more {
			break
		}
		offset = int(next.(float64))
	}
	if len(seen) != len(want) {
		t.Fatalf("discovered %d/%d roots", len(seen), len(want))
	}
	// At limit 50 the byte packer may shorten the page; continuation must
	// describe rows actually returned, and discover all remaining roots.
	seen = map[string]bool{}
	offset = 0
	for n := 0; ; n++ {
		if n > 105 {
			t.Fatal("infinite budget pages")
		}
		out := read(offset, 50)
		rows := out["comments"].([]any)
		for _, row := range rows {
			id := row.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatal("duplicate")
			}
			seen[id] = true
		}
		next, more := out["next_offset"]
		if !more {
			break
		}
		if int(next.(float64)) != offset+len(rows) {
			t.Fatal("wrong continuation", out)
		}
		offset = int(next.(float64))
	}
	if len(seen) != 105 {
		t.Fatal("byte paging lost roots", len(seen))
	}
	// The same HTTP route rechecks visibility between directory pages.
	stranger := dbfx.User(t, "private owner", "roots-"+randomID()+"@example.test")
	fastMustExec(t, `UPDATE issue SET creator_id=$2,visibility='private' WHERE id=$1`, parseUUID(issue), parseUUID(stranger))
	rec, _ := listComments(t, issue, "roots_only=true&summary=true&limit=1&offset=1")
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
		t.Fatalf("private page leaked: %d", rec.Code)
	}
	t.Log("production MCP + HTTP handler: 105 Chinese root summaries fully paged at limits 1 and 50; private continuation denied")
}

func TestCommentDirectoryPageSinceAndValidation(t *testing.T) {
	fx := newCommentListFixture(t)
	w, roots := listComments(t, fx.IssueID, "roots_only=true&summary=true&limit=1&offset=0")
	if w.Code != 200 || len(roots) != 1 || w.Header().Get("X-Multica-Next-Offset") != "1" {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	w, roots = listComments(t, fx.IssueID, "roots_only=true&summary=true&limit=1&offset=1")
	if w.Code != 200 || len(roots) != 1 || w.Header().Get("X-Multica-Next-Offset") != "" {
		t.Fatal("last", w.Code, w.Body.String())
	}
	for _, q := range []string{"limit=1", "roots_only=true&offset=1", "roots_only=true&limit=0", "roots_only=true&limit=51", "roots_only=true&limit=1&offset=-1"} {
		w, _ := listComments(t, fx.IssueID, q)
		if w.Code != 400 {
			t.Fatal(q, w.Code)
		}
	}
	issue := dbfx.Issue(t, "paged since roots")
	old := testutil.Cols{"created_at": testutil.Raw("now()-interval '2 hours'"), "updated_at": testutil.Raw("now()-interval '2 hours'")}
	root := dbfx.Comment(t, issue, "old root", old)
	dbfx.Comment(t, issue, "new reply", testutil.Cols{"parent_id": root})
	w, roots = listComments(t, issue, "roots_only=true&summary=true&limit=1&since="+fx.Base.UTC().Format("2006-01-02T15:04:05Z"))
	if w.Code != 200 || len(roots) != 1 || roots[0].ID != root {
		t.Fatal("since omitted changed old root", w.Code, w.Body.String())
	}
	t.Log(fmt.Sprintf("directory page sizes and query validation %s", strconv.Itoa(len(roots))))
}
