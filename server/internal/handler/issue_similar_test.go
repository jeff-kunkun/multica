package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/issueguard"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func similarCreate(t *testing.T, project, title, description string, extra map[string]any, want int) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"project_id": project, "title": title, "description": description}
	for k, v := range extra {
		body[k] = v
	}
	w := testutil.Call(t, testHandler.CreateIssue, newRequest(http.MethodPost, "/api/issues", body)).Want(want)
	if w.Code == http.StatusCreated {
		var issue IssueResponse
		w.JSON(&issue)
		t.Cleanup(func() {
			testPool.Exec(context.Background(), "DELETE FROM issue WHERE id=$1", issue.ID)
		})
	}
	return w.ResponseRecorder
}

func similarRead(t *testing.T, project, title, description, parent string) []issueguard.SimilarCandidate {
	t.Helper()
	query := url.Values{"project_id": {project}, "title": {title}, "description": {description}}
	if parent != "" {
		query.Set("parent_issue_id", parent)
	}
	w := httptest.NewRecorder()
	testHandler.SimilarIssues(w, newRequest(http.MethodGet, "/api/issues/similar?"+query.Encode(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("similar read: %d %s", w.Code, w.Body)
	}
	var result struct {
		Candidates []issueguard.SimilarCandidate `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Candidates
}

func TestSimilarIssueCreateBlocksRelayAndRecordsOverride(t *testing.T) {
	project := dbfx.Project(t, "DENE-2913 duplicate create demo")
	first := similarCreate(t, project, "马上赢修复合 test 并 TEST 实测", "合并请求 !494", nil, http.StatusCreated)
	var original IssueResponse
	json.Unmarshal(first.Body.Bytes(), &original)
	candidates := similarRead(t, project, "发布核对结论", "再次验收 !494", "")
	if len(candidates) != 1 || candidates[0].ID != original.ID {
		t.Fatalf("preflight candidates=%+v", candidates)
	}
	w := similarCreate(t, project, "发布核对结论", "再次验收 !494", map[string]any{"allow_duplicate": true}, http.StatusConflict)
	var conflict struct {
		Code       string                        `json:"code"`
		Error      string                        `json:"error"`
		Candidates []issueguard.SimilarCandidate `json:"candidates"`
	}
	json.Unmarshal(w.Body.Bytes(), &conflict)
	if conflict.Code != "active_duplicate_issue" || len(conflict.Candidates) != 1 || !strings.Contains(conflict.Error, "评论接力") {
		t.Fatalf("conflict=%s", w.Body)
	}
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM issue WHERE project_id=$1", project).Scan(&count)
	if count != 1 {
		t.Fatalf("blocked request created work: count=%d", count)
	}
	t.Logf("DEMO POST similar: HTTP %d\n%s\nproject row count after rejected create: %d", w.Code, w.Body.String(), count)
	reason := "和 " + original.Identifier + " 不同在另一款游戏的独立环境执行回归测试"
	w = similarCreate(t, project, "发布核对结论", "再次验收 !494\n查重放行："+reason, nil, http.StatusCreated)
	var created IssueResponse
	json.Unmarshal(w.Body.Bytes(), &created)
	var stored string
	dbfx.QueryRow(t, "SELECT metadata->'dedupe_override'->>'reason' FROM issue WHERE id=$1", created.ID).Scan(&stored)
	if stored != reason {
		t.Fatalf("recorded reason=%q, want %q", stored, reason)
	}
	t.Logf("DEMO reasoned override: HTTP %d; metadata.dedupe_override.reason=%s", w.Code, stored)
}

func TestSimilarIssueRecentWindowsAndDecomposition(t *testing.T) {
	for _, tt := range []struct {
		name, status, age string
		want              int
	}{
		{"active old", "in_progress", "30 days", 1},
		{"recent done", "done", "2 days", 1},
		{"old done", "done", "4 days", 0},
		{"recent cancelled", "cancelled", "1 hour", 1},
		{"old cancelled", "cancelled", "3 hours", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			project := dbfx.Project(t, "Similar window "+tt.name)
			id := dbfx.Issue(t, "修复游戏部署验证超时", testutil.Cols{"project_id": project, "status": tt.status})
			dbfx.Exec(t, "UPDATE issue SET updated_at=now()-$2::interval WHERE id=$1", id, tt.age)
			candidates := similarRead(t, project, "修复游戏部署验证超时（接力）", "", "")
			if len(candidates) != tt.want {
				t.Fatalf("candidates=%+v, want count=%d", candidates, tt.want)
			}
			if tt.status == "done" && tt.want == 1 {
				similarCreate(t, project, "修复游戏部署验证超时（接力）", "", nil, http.StatusConflict)
				similarCreate(t, project, "修复游戏部署验证超时（接力）", "接力 "+candidates[0].Identifier, nil, http.StatusCreated)
			}
		})
	}
	project := dbfx.Project(t, "Similar decomposition")
	parent := dbfx.Issue(t, "修复游戏部署验证超时", testutil.Cols{"project_id": project})
	dbfx.Issue(t, "修复游戏部署验证超时", testutil.Cols{"project_id": project, "parent_issue_id": parent})
	if candidates := similarRead(t, project, "修复游戏部署验证超时", "", parent); len(candidates) != 0 {
		t.Fatalf("planned decomposition blocked: %+v", candidates)
	}
	similarCreate(t, project, "修复游戏部署验证超时", "", map[string]any{"parent_issue_id": parent}, http.StatusCreated)
	other := dbfx.Project(t, "Another independent project")
	if candidates := similarRead(t, other, "修复游戏部署验证超时", "", ""); len(candidates) != 0 {
		t.Fatalf("cross-project candidates leaked: %+v", candidates)
	}
}

func TestSimilarIssueConcurrentParaphrasesCreateOnlyOne(t *testing.T) {
	project := dbfx.Project(t, "Similar concurrency")
	t.Cleanup(func() {
		testPool.Exec(context.Background(), "DELETE FROM issue WHERE project_id=$1", project)
	})
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for _, title := range []string{"修复游戏测试环境部署故障", "修复游戏测试环境部署故障（接力）"} {
		wg.Add(1)
		go func(title string) {
			defer wg.Done()
			req := newRequest(http.MethodPost, "/api/issues", map[string]any{"project_id": project, "title": title})
			w := httptest.NewRecorder()
			<-start
			testHandler.CreateIssue(w, req)
			results <- w
		}(title)
	}
	close(start)
	wg.Wait()
	close(results)
	success, blocked := 0, 0
	for w := range results {
		switch w.Code {
		case http.StatusCreated:
			success++
		case http.StatusConflict:
			blocked++
		default:
			t.Fatalf("unexpected create status=%d body=%s", w.Code, w.Body)
		}
	}
	if success != 1 || blocked != 1 {
		t.Fatalf("concurrent requests: created=%d blocked=%d", success, blocked)
	}
}

func TestSimilarIssueDoesNotRevealPrivateCandidates(t *testing.T) {
	project := dbfx.Project(t, "Similar privacy", testutil.Cols{"visibility": "workspace"})
	hidden := dbfx.Issue(t, "修复游戏测试环境部署故障", testutil.Cols{"project_id": project, "visibility": "private"})
	stranger := visibilityTestMember(t, "Similar privacy member", fmt.Sprintf("similar-%d@example.test", time.Now().UnixNano()))
	query := url.Values{"project_id": {project}, "title": {"修复游戏测试环境部署故障"}}
	w := httptest.NewRecorder()
	testHandler.SimilarIssues(w, newRequestAs(stranger, http.MethodGet, "/api/issues/similar?"+query.Encode(), nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), hidden) || !strings.Contains(w.Body.String(), `"candidates":[]`) {
		t.Fatalf("private preflight leaked: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	testHandler.CreateIssue(w, newRequestAs(stranger, http.MethodPost, "/api/issues", map[string]any{
		"project_id": project, "title": "修复游戏测试环境部署故障",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("private candidate leaked through create: %d %s", w.Code, w.Body)
	}
	var created IssueResponse
	json.Unmarshal(w.Body.Bytes(), &created)
	t.Cleanup(func() { testPool.Exec(context.Background(), "DELETE FROM issue WHERE id=$1", created.ID) })
}
