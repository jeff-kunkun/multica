package daemon

import (
	"context"
	"encoding/json"
	"fmt"
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

// A refused update is an error, not a move.
func TestBlockIssueAfterIdleStopsReportsARefusedUpdate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"status":"in_progress","assignee_type":"agent","assignee_id":"agent-1","revision":3}`))
	}))
	defer srv.Close()
	moved, err := NewClient(srv.URL).BlockIssueAfterIdleStops(context.Background(), "mat_x", "issue-1", "agent-1", "看一下")
	if err == nil || moved {
		t.Fatalf("moved=%v err=%v, want the refusal reported", moved, err)
	}
}

// A revision conflict means someone else edited the issue: it is read again
// and the decision taken again on what it is now.
func TestBlockIssueAfterIdleStopsRereadsAfterAConflict(t *testing.T) {
	t.Run("still in progress: moved with the new revision", func(t *testing.T) {
		var revisions []float64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				revisions = append(revisions, body["expected_revision"].(float64))
				if len(revisions) == 1 {
					w.WriteHeader(http.StatusConflict)
				}
				return
			}
			_, _ = fmt.Fprintf(w, `{"status":"in_progress","assignee_type":"agent","assignee_id":"agent-1","revision":%d}`, 3+len(revisions))
		}))
		defer srv.Close()
		moved, err := NewClient(srv.URL).BlockIssueAfterIdleStops(context.Background(), "mat_x", "issue-1", "agent-1", "看一下")
		if err != nil || !moved || len(revisions) != 2 || revisions[1] != 4 {
			t.Fatalf("moved=%v err=%v revisions=%v, want a second try at revision 4", moved, err, revisions)
		}
	})
	t.Run("someone moved it meanwhile: left alone", func(t *testing.T) {
		puts := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				puts++
				w.WriteHeader(http.StatusConflict)
				return
			}
			status := "in_progress"
			if puts > 0 {
				status = "in_review"
			}
			_, _ = fmt.Fprintf(w, `{"status":%q,"assignee_type":"agent","assignee_id":"agent-1","revision":%d}`, status, 3+puts)
		}))
		defer srv.Close()
		moved, err := NewClient(srv.URL).BlockIssueAfterIdleStops(context.Background(), "mat_x", "issue-1", "agent-1", "看一下")
		if err != nil || moved || puts != 1 {
			t.Fatalf("moved=%v err=%v puts=%d, want the other change respected", moved, err, puts)
		}
	})
	t.Run("conflicts every time: reported after a bounded number of tries", func(t *testing.T) {
		puts := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				puts++
				w.WriteHeader(http.StatusConflict)
				return
			}
			_, _ = w.Write([]byte(`{"status":"in_progress","assignee_type":"agent","assignee_id":"agent-1","revision":3}`))
		}))
		defer srv.Close()
		moved, err := NewClient(srv.URL).BlockIssueAfterIdleStops(context.Background(), "mat_x", "issue-1", "agent-1", "看一下")
		if err == nil || moved || puts != blockAfterIdleStopsAttempts {
			t.Fatalf("moved=%v err=%v puts=%d, want the conflict reported after %d tries", moved, err, puts, blockAfterIdleStopsAttempts)
		}
	})
}
