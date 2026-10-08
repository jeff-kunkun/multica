package handler
import ("context"; "net/http"; "net/http/httptest"; "testing"; "time"; "strings")
func TestReviewReportSummaryVerbatim(t *testing.T) {
	if testPool == nil {
		t.Skip("no database")
	}
	ctx := context.Background()
	f := newProjectReportFixture(t)
	agentID := handlerTestAgentID(t)
	inProject := func(title string) string {
		issue := createIssueHTTP(t, title, "in_progress")
		if _, err := testPool.Exec(ctx, `UPDATE issue SET project_id = $2 WHERE id = $1`, issue.ID, f.projectID); err != nil {
			t.Fatalf("move into project: %v", err)
		}
		setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
		return issue.ID
	}
	closeIt := func(issueID string, body map[string]any) {
		taskID := insertIssueTaskWithStatus(t, agentID, issueID, "running")
		body["outcome"] = "in_progress"
		body["wake_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
		if w := closeIssueHTTP(t, issueID, agentID, taskID, body); w.Code != http.StatusOK {
			t.Fatalf("close status = %d: %s", w.Code, w.Body.String())
		}
	}

	withSummary := inProject("report summary")
	closeIt(withSummary, map[string]any{"summary": strings.Repeat("结", 501), "evidence": "证据：测试全绿"})
	noSummary := inProject("report no summary")
	closeIt(noSummary, map[string]any{"evidence": "只有证据没有结论"})
	handedOff := inProject("report handoff")
	closeIt(handedOff, map[string]any{"summary": "旧结论", "evidence": "证据"})
	// The handoff lands strictly after close.at (second precision).
	time.Sleep(1100 * time.Millisecond)
	next := createHandlerTestAgent(t, "report next "+time.Now().Format(time.RFC3339Nano), []byte("[]"))
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+handedOff+"/handoff", map[string]any{
		"to": agentNameDirect(t, next), "summary": "卡在前端，要验收席看截图",
	}), "id", handedOff)
	rec := httptest.NewRecorder()
	testHandler.HandoffIssue(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handoff status = %d: %s", rec.Code, rec.Body.String())
	}

	items := reportItems(callProjectReport(t, f.projectID, "", false))
	if got := items[withSummary].LatestSummary; got != strings.Repeat("结", 501) {
		t.Fatalf("closed with summary: latest_summary = %q", got)
	}
	if got, ok := items[noSummary]; !ok || got.LatestSummary != "" {
		t.Fatalf("closed without summary must be reported without latest_summary: %+v (present %v)", got, ok)
	}
	if got := items[handedOff].LatestSummary; got != "卡在前端，要验收席看截图" {
		t.Fatalf("handoff after close: latest_summary = %q", got)
	}
}

func TestReviewReportEmptyHandoff(t *testing.T) {
	if testPool == nil {
		t.Skip("no database")
	}
	ctx := context.Background()
	f := newProjectReportFixture(t)
	agentID := handlerTestAgentID(t)
	inProject := func(title string) string {
		issue := createIssueHTTP(t, title, "in_progress")
		if _, err := testPool.Exec(ctx, `UPDATE issue SET project_id = $2 WHERE id = $1`, issue.ID, f.projectID); err != nil {
			t.Fatalf("move into project: %v", err)
		}
		setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
		return issue.ID
	}
	closeIt := func(issueID string, body map[string]any) {
		taskID := insertIssueTaskWithStatus(t, agentID, issueID, "running")
		body["outcome"] = "in_progress"
		body["wake_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
		if w := closeIssueHTTP(t, issueID, agentID, taskID, body); w.Code != http.StatusOK {
			t.Fatalf("close status = %d: %s", w.Code, w.Body.String())
		}
	}

	withSummary := inProject("report summary")
	closeIt(withSummary, map[string]any{"summary": "服务端做完了，剩 CLI 说明", "evidence": "证据：测试全绿"})
	noSummary := inProject("report no summary")
	closeIt(noSummary, map[string]any{"evidence": "只有证据没有结论"})
	handedOff := inProject("report handoff")
	closeIt(handedOff, map[string]any{"summary": "旧结论", "evidence": "证据"})
	// The handoff lands strictly after close.at (second precision).
	time.Sleep(1100 * time.Millisecond)
	next := createHandlerTestAgent(t, "report next "+time.Now().Format(time.RFC3339Nano), []byte("[]"))
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+handedOff+"/handoff", map[string]any{
		"to": agentNameDirect(t, next),
	}), "id", handedOff)
	rec := httptest.NewRecorder()
	testHandler.HandoffIssue(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handoff status = %d: %s", rec.Code, rec.Body.String())
	}

	items := reportItems(callProjectReport(t, f.projectID, "", false))
	if got := items[withSummary].LatestSummary; got != "服务端做完了，剩 CLI 说明" {
		t.Fatalf("closed with summary: latest_summary = %q", got)
	}
	if got, ok := items[noSummary]; !ok || got.LatestSummary != "" {
		t.Fatalf("closed without summary must be reported without latest_summary: %+v (present %v)", got, ok)
	}
	if got := items[handedOff].LatestSummary; got != "" {
		t.Fatalf("handoff after close: latest_summary = %q", got)
	}
}
