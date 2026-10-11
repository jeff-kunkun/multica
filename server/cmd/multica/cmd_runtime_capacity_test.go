package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeCapacityReadWriteAndBotMap(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	limit := 12
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/runtimes/rt-1" && r.Method == http.MethodPatch {
			var body struct {
				Limit int `json:"max_concurrent_runs"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			limit = body.Limit
		} else if r.URL.Path != "/api/runtimes" || r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		result := map[string]any{"id": "rt-1", "machine_capacity": map[string]int{"active_runs": 12, "max_concurrent_runs": limit}}
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode([]any{result})
		} else {
			json.NewEncoder(w).Encode(result)
		}
	}))
	defer srv.Close()
	for _, set := range []bool{false, true} {
		cmd := newRuntimeDeleteTestCmd(srv.URL)
		cmd.Flags().Int("max-concurrent-runs", 12, "")
		cmd.Flags().Set("output", "json")
		if set {
			cmd.Flags().Set("max-concurrent-runs", "8")
		}
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := runRuntimeCapacity(cmd, []string{"rt-1"}); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Capacity struct {
				Limit int `json:"max_concurrent_runs"`
			} `json:"machine_capacity"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := 12
		if set {
			want = 8
		}
		if got.Capacity.Limit != want {
			t.Fatalf("limit=%d want=%d", got.Capacity.Limit, want)
		}
	}
	description := describeCommand(runtimeCapacityCmd, "runtime")
	if description.Access != "write" || description.BotKey != "yes" {
		t.Fatalf("bot capability=%+v", description)
	}
}
