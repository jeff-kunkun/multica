package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestReferenceProjectsRequestModes(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	const link = "22222222-2222-4222-8222-222222222222"
	const project = "33333333-3333-4333-8333-333333333333"
	for _, tt := range []struct {
		kind         string
		args         []string
		method, body string
	}{
		{"project", nil, "GET", ""},
		{"project", []string{"--reference", link + ":" + project}, "PUT", `{"linked_projects":[{"link_id":"` + link + `","project_id":"` + project + `"}]}`},
		{"autopilot", []string{"--clear"}, "PUT", `{"linked_projects":[]}`},
		{"autopilot", []string{"--inherit"}, "PUT", `{"linked_projects":null}`},
	} {
		t.Run(tt.kind+tt.body, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tt.method || r.URL.Path != "/api/"+tt.kind+"s/"+id+"/linked-projects" {
					t.Errorf("request %s %s", r.Method, r.URL)
				}
				if tt.body != "" {
					var got, want any
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					_ = json.Unmarshal([]byte(tt.body), &want)
					if !reflect.DeepEqual(got, want) {
						t.Errorf("body = %#v, want %#v", got, want)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"linked_projects":null,"effective_linked_projects":[],"inherited":true}`))
			}))
			defer srv.Close()
			t.Setenv("HOME", t.TempDir())
			t.Setenv("MULTICA_TOKEN", "mat_test-token")
			t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
			t.Setenv("MULTICA_SERVER_URL", srv.URL)
			cmd := newReferenceProjectsCommand(tt.kind)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(append([]string{id}, tt.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Errorf("calls = %d", calls)
			}
			if !strings.Contains(out.String(), `"inherited": true`) {
				t.Errorf("response = %s", out.String())
			}
		})
	}
}

func TestReferenceProjectsRejectsAmbiguousAndMalformedWrites(t *testing.T) {
	for _, args := range [][]string{{"--clear", "--inherit"}, {"--reference", "bad"}, {"--reference", "bad:id"}, {"--output", "yaml"}} {
		cmd := newReferenceProjectsCommand("autopilot")
		cmd.SetArgs(append([]string{"11111111-1111-4111-8111-111111111111"}, args...))
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestReferenceProjectsHelpDocumentsInheritance(t *testing.T) {
	for _, kind := range []string{"project", "autopilot"} {
		cmd := newReferenceProjectsCommand(kind)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--help"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"--reference", "--clear", "effective_linked_projects", "available", "multica " + kind + " linked-projects"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%s help lacks %q", kind, want)
			}
		}
		if kind == "autopilot" && !strings.Contains(out.String(), "--inherit") {
			t.Error("autopilot help lacks inheritance")
		}
	}
}
