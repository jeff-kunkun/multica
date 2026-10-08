package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCheckChatSedimentHoldsTheClaimAgainstGit(t *testing.T) {
	files := []string{"CONTEXT.md", "server/x.go"}
	ok := ChatSedimentRequest{
		Changes:        []closeprotocol.KnowledgeChange{{Location: "context", Summary: "词条"}},
		DeliveredFiles: &files,
		Commits:        []string{"ABCDEF1234567"},
		Mainline:       "kun",
		Landing:        &closeprotocol.SedimentLanding{Via: closeprotocol.LandingPR, Remote: "github.com/o/r", PRURL: "https://github.com/o/r/pull/7", PRBase: "kun", PRMerged: true},
	}
	audit, commits, rejection := checkChatSediment(ok)
	if rejection != "" || len(commits) != 1 || commits[0] != "abcdef1234567" || audit.Changes[0].Files[0] != "CONTEXT.md" {
		t.Fatalf("audit = %+v commits = %v rejection = %q", audit, commits, rejection)
	}

	other := []string{"server/x.go"}
	for name, tc := range map[string]struct {
		mutate func(*ChatSedimentRequest)
		want   string
	}{
		"no changes":     {func(r *ChatSedimentRequest) { r.Changes = nil }, "知识审计"},
		"no file list":   {func(r *ChatSedimentRequest) { r.DeliveredFiles = nil }, "缺 delivered_files"},
		"file not there": {func(r *ChatSedimentRequest) { r.DeliveredFiles = &other }, "没有对应文件"},
		"no mainline":    {func(r *ChatSedimentRequest) { r.Mainline = " " }, "缺 mainline"},
		"no commits":     {func(r *ChatSedimentRequest) { r.Commits = nil }, "缺 commits"},
		"not a sha":      {func(r *ChatSedimentRequest) { r.Commits = []string{"main"} }, "不是 git SHA"},
		"no landing":     {func(r *ChatSedimentRequest) { r.Landing = nil }, "缺 landing"},
		"unpushed": {func(r *ChatSedimentRequest) {
			r.Landing = &closeprotocol.SedimentLanding{Via: closeprotocol.LandingPush, Remote: "github.com/o/r"}
		}, "还没收到"},
		"local merge with a remote": {func(r *ChatSedimentRequest) {
			r.Landing = &closeprotocol.SedimentLanding{Via: closeprotocol.LandingLocal, Remote: "github.com/o/r"}
		}, "本地合入不算数"},
		"pr in another repo": {func(r *ChatSedimentRequest) {
			l := *r.Landing
			l.PRURL = "https://github.com/x/r/pull/7"
			r.Landing = &l
		}, "不在这个仓库"},
		"pr into another branch": {func(r *ChatSedimentRequest) {
			l := *r.Landing
			l.PRBase = "feature/x"
			r.Landing = &l
		}, "项目主线是 kun"},
		"claimed files": {func(r *ChatSedimentRequest) {
			r.Changes = []closeprotocol.KnowledgeChange{{Location: "agents", Summary: "s", Files: []string{"AGENTS.md"}}}
		}, "没有对应文件"},
	} {
		req := ok
		tc.mutate(&req)
		if _, _, got := checkChatSediment(req); !strings.Contains(got, tc.want) {
			t.Errorf("%s: rejection = %q, want it to mention %q", name, got, tc.want)
		}
	}
}

func sedimentRowsFor(t *testing.T, column, id string) []db.KnowledgeSediment {
	t.Helper()
	rows, err := testPool.Query(context.Background(),
		`SELECT changes, verified, mainline FROM knowledge_sediment WHERE `+column+` = $1`, id)
	if err != nil {
		t.Fatalf("read sediments: %v", err)
	}
	defer rows.Close()
	var out []db.KnowledgeSediment
	for rows.Next() {
		var row db.KnowledgeSediment
		if err := rows.Scan(&row.Changes, &row.Verified, &row.Mainline); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, row)
	}
	return out
}

// DENE-1661: a done close that claims a memory slot must deliver the file.
func TestCloseKnowledgeChangeMustShipWithTheDelivery(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close knowledge must ship", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	body := func(files []string) map[string]any {
		return map[string]any{
			"outcome":         "done",
			"evidence":        "加了词条。",
			"no_code_reason":  "纯文档",
			"delivered_files": files,
			"knowledge_audit": map[string]any{
				"changes": []any{map[string]any{"location": "context", "summary": "加了「沉淀记录」词条",
					"files": []any{"CONTEXT.md"}}},
			},
		}
	}

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, body([]string{"server/x.go"}))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "本次交付的改动里没有对应文件") {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	assertCloseRejectedClean(t, issue.ID, "in_progress")
	if rows := sedimentRowsFor(t, "issue_id", issue.ID); len(rows) != 0 {
		t.Fatalf("a refused close recorded %d sediments", len(rows))
	}

	w = closeIssueHTTP(t, issue.ID, agentID, taskID, body([]string{"server/x.go", "CONTEXT.md"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	want := `{"changes":[{"location":"context","summary":"加了「沉淀记录」词条","files":["CONTEXT.md"]}]}`
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyKnowledgeAudit); got != want {
		t.Fatalf("close.knowledge_audit = %q", got)
	}
	rows := sedimentRowsFor(t, "issue_id", issue.ID)
	if len(rows) != 1 || !rows[0].Verified || !strings.Contains(string(rows[0].Changes), "CONTEXT.md") {
		t.Fatalf("sediments = %+v", rows)
	}
}

func TestChatSedimentIsRecordedAndListed(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := handlerTestAgentID(t)
	sessionID := insertChatSessionAs(t, agentID, testUserID)
	post := func(body map[string]any) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions/"+sessionID+"/sediment", body))
		req = withURLParam(req, "sessionId", sessionID)
		testHandler.CreateChatSediment(w, req)
		return w
	}
	changes := []any{map[string]any{"location": "agents", "summary": "聊天收尾要沉淀"}}
	landing := map[string]any{"via": "push", "remote": "github.com/o/r", "remote_has_commits": true}

	if w := post(map[string]any{"changes": changes, "delivered_files": []string{"README.md"}, "commits": []string{"abcdef1"}, "mainline": "kun", "landing": landing}); w.Code != http.StatusBadRequest {
		t.Fatalf("a sediment whose file is not delivered: status = %d: %s", w.Code, w.Body.String())
	}
	if w := post(map[string]any{"changes": changes, "delivered_files": []string{"AGENTS.md"}, "commits": []string{"abcdef1"}, "mainline": "kun",
		"landing": map[string]any{"via": "push", "remote": "github.com/o/r"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("an unpushed sediment: status = %d: %s", w.Code, w.Body.String())
	}
	w := post(map[string]any{"changes": changes, "delivered_files": []string{"AGENTS.md"}, "commits": []string{"abcdef1"}, "mainline": "kun", "landing": landing})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var created KnowledgeSedimentResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.SourceKind != "chat" || created.Mainline != "kun" || len(created.Changes) != 1 || created.Changes[0].Files[0] != "AGENTS.md" {
		t.Fatalf("created = %+v", created)
	}

	lw := httptest.NewRecorder()
	req := withChatTestWorkspaceCtx(t, newRequest(http.MethodGet, "/api/chat/sessions/"+sessionID+"/sediment", nil))
	req = withURLParam(req, "sessionId", sessionID)
	testHandler.ListChatSediments(lw, req)
	var list struct {
		Sediments []KnowledgeSedimentResponse `json:"sediments"`
	}
	if err := json.NewDecoder(lw.Body).Decode(&list); err != nil || len(list.Sediments) != 1 || list.Sediments[0].ID != created.ID {
		t.Fatalf("list = %+v err = %v (%s)", list, err, lw.Body.String())
	}
	var progress *string
	if err := testPool.QueryRow(context.Background(), `SELECT progress_text FROM chat_session WHERE id = $1`, sessionID).Scan(&progress); err != nil || progress == nil || !strings.Contains(*progress, "AGENTS.md") {
		t.Fatalf("chat progress line = %v; want it to name the file", progress)
	}
}

// DENE-1661: a milestone round's ticket carries what the source tickets
// concluded, what they wrote into memory, and the head of their evidence.
func TestMemoryRoundCarriesTheSourceTicketsDigest(t *testing.T) {
	memoryProgressReady(t)
	openSedimentSeat(t)
	projectID := dbfx.Project(t, "memory digest project")
	forgetSediment(t, projectID)
	parent := dbfx.Issue(t, "memory digest parent", testutil.Cols{
		"status": "in_progress", "project_id": projectID,
		"assignee_type": "member", "assignee_id": testUserID,
	})
	child := dbfx.Issue(t, "memory digest child", testutil.Cols{
		"status": "in_progress", "parent_issue_id": parent, "project_id": projectID,
	})
	var evidenceID string
	dbfx.QueryRow(t, `INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type)
		VALUES ($1, $2, 'member', $3, '证据：PR #9 合入，测试全绿', 'comment') RETURNING id::text`,
		child, testWorkspaceID, testUserID).Scan(&evidenceID)
	dbfx.Exec(t, `UPDATE issue SET progress_text = '收口改成随交付沉淀',
		metadata = COALESCE(metadata, '{}'::jsonb) || jsonb_build_object($2::text, $3::text, $4::text, $5::text)
		WHERE id = $1`, child,
		closeprotocol.KeyKnowledgeAudit, `{"changes":[{"location":"context","summary":"加了沉淀记录词条","files":["CONTEXT.md"]}]}`,
		closeprotocol.KeyEvidenceCommentID, evidenceID)

	finishChild(t, child, "in_progress")
	ids := sedimentIDs(t, projectID)
	if len(ids) != 1 {
		t.Fatalf("sediment tickets = %d", len(ids))
	}
	desc := issueDescription(t, ids[0])
	for _, want := range []string{"父票子票全部终态", "来源票：", "memory digest child", "结论：收口改成随交付沉淀", "沉淀：context(CONTEXT.md) 加了沉淀记录词条", "证据：证据：PR #9 合入"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description lacks %q:\n%s", want, desc)
		}
	}
}
