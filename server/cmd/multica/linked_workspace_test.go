package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestUseLinkedWorkspace(t *testing.T) {
	var linkedHeader, workspaceHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workspace-links" {
			w.Write([]byte(`{"links":[
				{"id":"l1","side":"viewer","status":"active","managed":true,"source":{"id":"src-1","slug":"wd-game"}},
				{"id":"l2","side":"viewer","status":"active","managed":false,"source":{"id":"src-2","slug":"read-only"}},
				{"id":"l3","side":"source","status":"active","managed":true,"source":{"id":"self","slug":"mine"}},
				{"id":"l4","side":"viewer","status":"pending","managed":true,"source":{"id":"src-4","slug":"pending"}}]}`))
			return
		}
		linkedHeader, workspaceHeader = r.Header.Get("X-Linked-Workspace"), r.Header.Get("X-Workspace-ID")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	for _, c := range []struct{ ref, want string }{
		{"read-only", "--managed on"},
		{"mine", "no active link"},
		{"pending", "no active link"},
		{"nowhere", "no active link"},
	} {
		err := useLinkedWorkspace(cli.NewAPIClient(srv.URL, "viewer", "mat_x"), c.ref)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.ref, err, c.want)
		}
	}

	for _, ref := range []string{"wd-game", "src-1"} {
		client := cli.NewAPIClient(srv.URL, "viewer", "mat_x")
		if err := useLinkedWorkspace(client, ref); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		var out map[string]any
		if err := client.GetJSON(context.Background(), "/api/issues", &out); err != nil {
			t.Fatal(err)
		}
		if linkedHeader != "src-1" || workspaceHeader != "src-1" {
			t.Fatalf("%s: sent X-Linked-Workspace=%q X-Workspace-ID=%q", ref, linkedHeader, workspaceHeader)
		}
	}
}
