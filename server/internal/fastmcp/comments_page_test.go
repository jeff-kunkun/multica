package fastmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestMCPRootDirectoryServerPaging(t *testing.T) {
	rows := make([]any, 100)
	for i := range rows {
		rows[i] = map[string]any{"id": fmt.Sprintf("root-%d", i), "content": strings.Repeat("字", 200), "reply_count": 0}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))
		if limit == 0 {
			json.NewEncoder(w).Encode(rows)
			return
		} // original over-budget fixture
		if q.Get("roots_only") != "true" || q.Get("summary") != "true" || limit != 1 {
			t.Error("directory parameters not forwarded", q)
		}
		end := min(offset+limit, len(rows))
		if end < len(rows) {
			w.Header().Set("X-Multica-Next-Offset", strconv.Itoa(end))
			w.Header().Set("X-Comments-Truncated", "true")
		}
		json.NewEncoder(w).Encode(rows[min(offset, len(rows)):end])
	}))
	defer srv.Close()
	a := &adapter{client: cli.NewAPIClient(srv.URL, testWS, "mat_fixture")}
	for i := 0; i < 100; i++ {
		out, err := a.comments(context.Background(), CommentsInput{ID: testID, Limit: 1, Offset: i})
		if err != nil || out["code"] != nil {
			t.Fatalf("page %d: %v %v", i, out, err)
		}
		comments := out["comments"].([]any)
		if len(comments) != 1 || comments[0].(map[string]any)["id"] != fmt.Sprintf("root-%d", i) {
			t.Fatal("lost/duplicate root", out)
		}
		if i < 99 && out["next_offset"] != i+1 {
			t.Fatal("missing continuation", out)
		}
		if i == 99 && out["next_offset"] != nil {
			t.Fatal("false continuation", out)
		}
	}
}

func TestMCPRootDirectoryRejectsUnpagedUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := make([]any, 100)
		for i := range rows {
			rows[i] = map[string]any{"id": testID, "content": strings.Repeat("字", 200), "reply_count": 0}
		}
		json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()
	a := &adapter{client: cli.NewAPIClient(srv.URL, testWS, "mat_fixture")}
	out, err := a.comments(context.Background(), CommentsInput{ID: testID, Limit: 1})
	if err != nil || out["code"] != "response_budget_exceeded" {
		t.Fatalf("transport budget bypassed: %v %v", out, err)
	}
}
