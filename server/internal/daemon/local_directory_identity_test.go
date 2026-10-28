package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The backfill names the task's runtime so a daemon signed in with a personal
// token, which has no daemon id on its credential, is not refused with 401.
func TestReportLocalDirectoryIdentitySendsTheRuntime(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon/project-resources/res-1/identity" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"applied":true}`))
	}))
	defer srv.Close()
	d := &Daemon{client: NewClient(srv.URL), logger: discardLogger()}

	d.reportLocalDirectoryIdentity("ws-1", "rt-1", &localDirectoryAssignment{ResourceID: "res-1", AbsPath: t.TempDir()})
	if body["runtime_id"] != "rt-1" || body["real_path"] == nil {
		t.Fatalf("backfill body = %v, want runtime_id and real_path", body)
	}
}
