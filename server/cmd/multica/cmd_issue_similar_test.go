package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestRunIssueSimilarReadsServerCandidatesAsJSON(t *testing.T) {
	project := uuid.NewString()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/issues/similar" || r.URL.Query().Get("project_id") != project || r.URL.Query().Get("title") != "修复测试部署故障" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"candidates":  []any{map[string]any{"id": "original", "identifier": "DENE-2904", "title": "已有工作"}},
			"would_block": true, "threshold": 0.4,
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", uuid.NewString())
	t.Setenv("MULTICA_TOKEN", "test-token")
	cmd := newFlagTestCmd("description")
	cmd.Flags().String("project", project, "")
	cmd.Flags().String("title", "修复测试部署故障", "")
	cmd.Flags().String("parent", "", "")
	cmd.Flags().String("output", "json", "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runIssueSimilar(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Candidates []map[string]any `json:"candidates"`
		WouldBlock bool             `json:"would_block"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !got.WouldBlock || len(got.Candidates) != 1 {
		t.Fatalf("invalid JSON result: %s, err=%v", &out, err)
	}
}

func TestRunIssueCreateDuplicateJSONIsMachineReadableAndFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{
			"code": "active_duplicate_issue", "error": "请回原单评论接力",
			"issue":      map[string]any{"id": "original", "identifier": "DENE-2904"},
			"candidates": []any{map[string]any{"id": "original", "identifier": "DENE-2904"}},
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", uuid.NewString())
	t.Setenv("MULTICA_TOKEN", "test-token")
	cmd := newIssueCreateTestCmd()
	cmd.Flags().Set("title", "修复测试部署故障")
	cmd.Flags().Set("output", "json")
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := runIssueCreate(cmd, nil)
	if err == nil || err.Error() != "请回原单评论接力" {
		t.Fatalf("duplicate error=%v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(out.Bytes(), &body); err != nil || body["code"] != "active_duplicate_issue" {
		t.Fatalf("conflict stdout is not a JSON body: %s, err=%v", &out, err)
	}
}
