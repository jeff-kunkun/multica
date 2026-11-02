package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// BlockIssueAfterIdleStops moves only an issue still in_progress with the
// run's agent assigned; any other state means someone already moved it.
func TestBlockIssueAfterIdleStopsLeavesOtherStatesAlone(t *testing.T) {
	cases := []struct {
		name  string
		issue string
	}{
		{"in review", `{"status":"in_review","assignee_type":"agent","assignee_id":"agent-1","revision":3}`},
		{"other agent", `{"status":"in_progress","assignee_type":"agent","assignee_id":"agent-2","revision":3}`},
		{"member", `{"status":"in_progress","assignee_type":"member","assignee_id":"agent-1","revision":3}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			puts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					puts++
				}
				_, _ = w.Write([]byte(tc.issue))
			}))
			defer srv.Close()
			moved, err := NewClient(srv.URL).BlockIssueAfterIdleStops(context.Background(), "mat_x", "issue-1", "agent-1", "看一下")
			if err != nil || moved || puts != 0 {
				t.Fatalf("moved=%v err=%v puts=%d, want the issue left alone", moved, err, puts)
			}
		})
	}
}

// A refused update (for example a revision someone else moved past) is an
// error, not a move.
func TestBlockIssueAfterIdleStopsReportsARefusedUpdate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusConflict)
			return
		}
		_, _ = w.Write([]byte(`{"status":"in_progress","assignee_type":"agent","assignee_id":"agent-1","revision":3}`))
	}))
	defer srv.Close()
	moved, err := NewClient(srv.URL).BlockIssueAfterIdleStops(context.Background(), "mat_x", "issue-1", "agent-1", "看一下")
	if err == nil || moved {
		t.Fatalf("moved=%v err=%v, want the conflict reported", moved, err)
	}
}
