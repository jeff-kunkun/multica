package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

const fastAccessFixture = `{"schema_version":"1","as_of":"2026-10-11T17:30:00Z","coverage_from":"2026-10-11T17:00:00Z","mode":"timestamp_snapshot","scope_revision":"scope-1","changes":[{"workspace_id":"ws-a","kind":"chat","id":"chat-1","title":"中文🙂","preview":"你好","unread_count":2,"revision":9007199254740993},{"workspace_id":"ws-b","kind":"issue","id":"issue-1","new_comment_count":1}],"active_runs":[{"workspace_id":"ws-b","id":"run-chat","source_type":"chat","source_id":"chat-1","status":"deferred","phase":"waiting"}],"active_runs_complete":true,"complete":true,"next_cursor":null,"checkpoint":"checkpoint-1","retention_floor":null,"future_field":{"number":9007199254740993}}`

func fastAccessTestCommand(t *testing.T, runs bool, handler http.HandlerFunc) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-default")
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_CONFIG_DIR", t.TempDir())
	cmd := newFastAccessCommand(runs)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	out, notes := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(notes)
	return cmd, out, notes
}

func TestFastAccessChangesOneRequestPreservesEnvelope(t *testing.T) {
	calls := 0
	cmd, out, notes := fastAccessTestCommand(t, false, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/fast-access/v1/read" {
			t.Errorf("request %s %s", r.Method, r.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		scope, _ := body["scope"].(map[string]any)
		if scope["mode"] != "all_visible" || body["since"] != "2026-10-11T17:00:00Z" || body["max_bytes"] != float64(50000) || body["limit"] != float64(100) {
			t.Errorf("body: %#v", body)
		}
		if _, ok := body["cursor"]; ok {
			t.Error("unexpected cursor")
		}
		if r.Header.Get("Authorization") != "Bearer mat_test-token" {
			t.Error("existing identity not reused")
		}
		io.WriteString(w, fastAccessFixture)
	})
	cmd.SetArgs([]string{"--since", "2026-10-11T17:00:00Z", "--all-workspaces", "--output", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || out.String() != fastAccessFixture {
		t.Fatalf("calls=%d envelope=%s", calls, out.String())
	}
	if !strings.Contains(notes.String(), "timestamp_snapshot") {
		t.Fatal("missing coverage note")
	}
}

func TestFastAccessPaginationAndScope(t *testing.T) {
	for _, runs := range []bool{false, true} {
		for _, scope := range []string{"all", "explicit", "default", "cursor"} {
			t.Run(fmt.Sprintf("runs=%v/%s", runs, scope), func(t *testing.T) {
				calls := 0
				partial := strings.ReplaceAll(fastAccessFixture, `"complete":true`, `"complete":false`)
				partial = strings.ReplaceAll(partial, `"active_runs_complete":true`, `"active_runs_complete":false`)
				partial = strings.ReplaceAll(partial, `"next_cursor":null`, `"next_cursor":"next+/?&中文"`)
				partial = strings.ReplaceAll(partial, `"checkpoint":"checkpoint-1"`, `"checkpoint":null`)
				cmd, out, notes := fastAccessTestCommand(t, runs, func(w http.ResponseWriter, r *http.Request) {
					calls++
					if runs {
						q := r.URL.Query()
						if r.Method != "GET" || r.URL.Path != "/api/fast-access/v1/runs" || q.Get("active") != "true" || q.Get("max_bytes") != "40000" || q.Get("limit") != "17" {
							t.Errorf("request %s %s", r.Method, r.URL)
						}
						want := "ws-default"
						if scope == "explicit" {
							want = "ws-a,ws-b"
						}
						switch scope {
						case "cursor":
							if q.Get("cursor") != "opaque+/?&中文" || q.Has("scope") || q.Has("workspace_ids") {
								t.Error(q)
							}
						case "all":
							if q.Get("scope") != "all_visible" || q.Has("workspace_ids") {
								t.Error(q)
							}
						default:
							if q.Get("workspace_ids") != want {
								t.Error(q)
							}
						}
					} else {
						var body map[string]any
						json.NewDecoder(r.Body).Decode(&body)
						if scope == "cursor" {
							if body["cursor"] != "opaque+/?&中文" || body["since"] != nil || body["scope"] != nil {
								t.Error(body)
							}
						} else {
							s := body["scope"].(map[string]any)
							if scope == "all" {
								if s["mode"] != "all_visible" {
									t.Error(s)
								}
							} else {
								want := `["ws-default"]`
								if scope == "explicit" {
									want = `["ws-a","ws-b"]`
								}
								got, _ := json.Marshal(s["workspace_ids"])
								if string(got) != want || s["mode"] != "workspace_ids" {
									t.Error(s)
								}
							}
						}
					}
					io.WriteString(w, partial)
				})
				args := []string{"--max-bytes", "40000", "--limit", "17", "--output", "json"}
				if runs {
					args = append(args, "--active")
				}
				if scope == "cursor" {
					args = append(args, "--cursor", "opaque+/?&中文")
				} else if !runs {
					args = append(args, "--since", "2026-10-11T17:00:00Z")
				}
				if scope == "all" {
					args = append(args, "--all-workspaces")
				}
				if scope == "explicit" {
					args = append(args, "--workspace", "ws-a", "--workspace", "ws-b")
				}
				cmd.SetArgs(args)
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
				if calls != 1 || out.String() != partial || !strings.Contains(notes.String(), "Partial page") {
					t.Fatalf("calls=%d out=%s notes=%s", calls, out, notes)
				}
			})
		}
	}
}

func TestFastAccessArgumentValidationAndHelp(t *testing.T) {
	for _, runs := range []bool{false, true} {
		cases := [][]string{{"--output", "yaml"}, {"--limit", "0"}, {"--limit", "201"}, {"--max-bytes", "0"}, {"--max-bytes", "50001"}, {"--all-workspaces", "--workspace", "ws-a"}, {"unexpected"}, {"--cursor", ""}, {"--workspace", ""}, {"--cursor", "opaque", "--workspace", "ws-a"}, {"--cursor", "opaque", "--all-workspaces"}}
		if !runs {
			cases = append(cases, []string{"--since", "not-time"}, []string{"--since", "2026-10-11T17:00:00Z", "--cursor", "next"})
		} else {
			cases = append(cases, []string{"--active=false"})
		}
		for _, args := range cases {
			t.Run(fmt.Sprintf("%v/%v", runs, args), func(t *testing.T) {
				cmd, _, _ := fastAccessTestCommand(t, runs, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid args made request") })
				cmd.SetArgs(args)
				if err := cmd.Execute(); err == nil {
					t.Fatal("accepted invalid arguments")
				}
			})
		}
		cmd := newFastAccessCommand(runs)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--help"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		for _, word := range []string{"--workspace", "--all-workspaces", "--cursor", "--output", "--limit", "--max-bytes", "50000", "complete", "coverage_from", "permission", "stderr", "multica "} {
			if !strings.Contains(out.String(), word) {
				t.Errorf("help missing %s", word)
			}
		}
	}
}

func TestFastAccessResponseFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, match string
		status            int
	}{
		{"permission", `{"code":"forbidden","error":"workspace denied"}`, "forbidden", 403},
		{"unauthorized", `{"code":"unauthorized"}`, "unauthorized", 401},
		{"rate", `{"code":"rate_limited","retry_after":7}`, "rate_limited", 429},
		{"reset", `{"code":"reset_required"}`, "reset_required", 410},
		{"unknown", strings.Replace(fastAccessFixture, `"1"`, `"2"`, 1), "unsupported_schema_version", 200},
		{"missing-version", `{"complete":true,"active_runs_complete":true}`, "unsupported_schema_version", 200},
		{"missing-completeness", `{"schema_version":"1"}`, "missing complete", 200},
		{"oversize", strings.Replace(fastAccessFixture, "你好", strings.Repeat("你", 17000), 1), "response_budget_exceeded", 200},
		{"malformed", `{"schema_version":`, "valid UTF-8 JSON", 200},
		{"invalid-utf8", strings.Replace(fastAccessFixture, "你好", string([]byte{0xff}), 1), "valid UTF-8 JSON", 200},
		{"trailing-json", fastAccessFixture + `{}`, "valid UTF-8 JSON", 200},
	} {
		for _, runs := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", tc.name, runs), func(t *testing.T) {
				calls := 0
				cmd, out, notes := fastAccessTestCommand(t, runs, func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
				})
				args := []string{"--all-workspaces", "--output", "json"}
				if runs {
					args = append(args, "--active")
				} else {
					args = append(args, "--since", "2026-10-11T17:00:00Z")
				}
				cmd.SetArgs(args)
				err := cmd.Execute()
				if err == nil || !strings.Contains(err.Error(), tc.match) {
					t.Fatalf("error %v, want %s", err, tc.match)
				}
				if out.Len() != 0 || calls != 1 {
					t.Fatalf("stdout=%s calls=%d", out, calls)
				}
				if tc.status == 429 && !strings.Contains(notes.String(), "Retry-After: 7") {
					t.Error(notes.String())
				}
				if tc.status >= 400 && !strings.Contains(notes.String(), `"code":"`+tc.match+`"`) {
					t.Errorf("missing server recovery code: %s", notes.String())
				}
			})
		}
	}
}

func TestFastAccessTimeoutNoRetry(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	cmd, out, _ := fastAccessTestCommand(t, false, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); <-release })
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"--since", "2026-10-11T17:00:00Z", "--all-workspaces", "--output", "json"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("error %v", err)
	}
	if calls.Load() != 1 || out.Len() != 0 {
		t.Fatalf("calls=%d out=%s", calls.Load(), out)
	}
}

func TestFastAccessBudgetBoundaryAndTable(t *testing.T) {
	var snapshot map[string]any
	dec := json.NewDecoder(strings.NewReader(fastAccessFixture))
	dec.UseNumber()
	dec.Decode(&snapshot)
	var out bytes.Buffer
	if err := printFastAccess(&out, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"coverage_from:", "complete: true", "active_runs_complete: true", "checkpoint:", `"status":"deferred"`, `"phase":"waiting"`, "中文🙂", "9007199254740993"} {
		if !strings.Contains(out.String(), want) {
			t.Error(want)
		}
	}
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			cmd, out, _ := fastAccessTestCommand(t, true, func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, fastAccessFixture+strings.Repeat(" ", extra))
			})
			cmd.SetArgs([]string{"--active", "--all-workspaces", "--max-bytes", fmt.Sprint(len(fastAccessFixture)), "--output", "json"})
			err := cmd.Execute()
			if extra == 0 && (err != nil || out.String() != fastAccessFixture) {
				t.Fatalf("err=%v out=%s", err, out)
			}
			if extra == 1 && (err == nil || out.Len() != 0) {
				t.Fatalf("err=%v out=%s", err, out)
			}
		})
	}
}

func TestFastAccessDiscovery(t *testing.T) {
	m := buildCapabilityMap(rootCmd, "test")
	found := map[string]bool{}
	for _, domain := range m.Domains {
		for _, cmd := range domain.Commands {
			if cmd.Command == "multica changes" || cmd.Command == "multica runs" {
				found[cmd.Command] = true
				if domain.Key != "overview" || cmd.Access != "read" || cmd.BotKey != "yes" || cmd.Example == "" || cmd.Help == "" {
					t.Errorf("domain=%s command=%+v", domain.Key, cmd)
				}
			}
		}
	}
	if len(found) != 2 {
		t.Fatalf("missing commands: %v", found)
	}
	// Existing common command schema remains generated by the same live tree.
	var out bytes.Buffer
	if err := cli.PrintJSON(&out, m); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out.Bytes()) {
		t.Fatal("invalid capability JSON")
	}
}

func TestFastAccessTwoWorkspacesSeventeenRuns(t *testing.T) {
	var fixture map[string]any
	dec := json.NewDecoder(strings.NewReader(fastAccessFixture))
	dec.UseNumber()
	dec.Decode(&fixture)
	runs := make([]any, 17)
	statuses := []string{"queued", "dispatched", "running", "waiting_local_directory", "deferred"}
	phases := []string{"queued", "executing", "executing", "waiting", "waiting"}
	for i := range runs {
		runs[i] = map[string]any{"workspace_id": fmt.Sprintf("ws-%c", 'a'+i%2), "id": fmt.Sprintf("run-%d", i), "source_type": "chat", "source_id": "chat-1", "status": statuses[i%5], "phase": phases[i%5]}
	}
	fixture["active_runs"] = runs
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	cmd, out, _ := fastAccessTestCommand(t, false, func(w http.ResponseWriter, r *http.Request) { calls++; w.Write(raw) })
	cmd.SetArgs([]string{"--since", "2026-10-11T17:00:00Z", "--all-workspaces", "--output", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !bytes.Equal(out.Bytes(), raw) || out.Len() > 50000 {
		t.Fatalf("calls=%d bytes=%d", calls, out.Len())
	}
	t.Logf("HTTP fixture: 2 workspaces, 17 active runs, 2 changes, %d bytes, 1 request, complete=true", out.Len())
}
