package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPrintCommentWake(t *testing.T) {
	cases := []struct {
		name string
		wake any
		want []string
		deny []string
	}{
		{"older server", nil, nil, []string{"Woke"}},
		{"none woken", map[string]any{"woken": []any{}, "none_woken": true},
			[]string{"Woke no one", "multica issue handoff TST-1 --to", "@ them"}, nil},
		{"attended", map[string]any{"woken": []any{}, "none_woken": false}, nil, []string{"Woke"}},
		{"woke agents", map[string]any{"woken": []any{
			map[string]any{"agent_id": "a1", "agent_name": "孙悟空", "source": "mention_agent", "status": "queued"},
			map[string]any{"agent_id": "a2", "agent_name": "", "source": "issue_assignee", "status": "steered"},
		}, "none_woken": false}, []string{"Woke: 孙悟空 (queued), a2 (steered)"}, []string{"Woke no one"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printCommentWake(&buf, "TST-1", tc.wake)
			for _, w := range tc.want {
				if !strings.Contains(buf.String(), w) {
					t.Errorf("output %q missing %q", buf.String(), w)
				}
			}
			for _, d := range tc.deny {
				if strings.Contains(buf.String(), d) {
					t.Errorf("output %q must not contain %q", buf.String(), d)
				}
			}
		})
	}
}

// The receipt line reaches the agent in both output modes, and --output json
// keeps the server's wake field verbatim.
func TestCommentAddReportsNoneWoken(t *testing.T) {
	const issueID = "11111111-1111-4111-8111-111111111111"
	for _, mode := range []string{"table", "json"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/issues/"+issueID:
					_ = json.NewEncoder(w).Encode(map[string]any{"id": issueID, "identifier": "TST-1"})
				case r.Method == http.MethodPost && r.URL.Path == "/api/issues/"+issueID+"/comments":
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"id": "c1", "content": "please continue",
						"wake": map[string]any{"woken": []any{}, "none_woken": true},
					})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			setCLITestServerEnv(t, srv.URL)
			t.Setenv("MULTICA_TOKEN", "mat_test-token")
			t.Chdir(t.TempDir())
			cmd := newIssueCommentAddTestCmd()
			if err := cmd.Flags().Set("content", "please continue"); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Flags().Set("output", mode); err != nil {
				t.Fatal(err)
			}
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			orig := os.Stdout
			os.Stdout = w
			stderr := captureStderr(t)
			runErr := runIssueCommentAdd(cmd, []string{issueID})
			os.Stdout = orig
			_ = w.Close()
			out, _ := io.ReadAll(r)
			if runErr != nil {
				t.Fatal(runErr)
			}
			if got := stderr.read(); !strings.Contains(got, "Woke no one") || !strings.Contains(got, "multica issue handoff "+issueID+" --to") {
				t.Fatalf("stderr = %q, want the none-woken line with the handoff command", got)
			}
			if mode == "json" {
				var result map[string]any
				if err := json.Unmarshal(out, &result); err != nil {
					t.Fatal(err)
				}
				if wake, _ := result["wake"].(map[string]any); wake["none_woken"] != true {
					t.Fatalf("json wake = %v, want none_woken true", result["wake"])
				}
			}
		})
	}
}
