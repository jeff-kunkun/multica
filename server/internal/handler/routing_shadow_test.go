package handler

import (
	"github.com/multica-ai/multica/server/internal/testutil"
	"net/http"
	"testing"
)

func TestRoutingShadowListValidatesWindow(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, path := range []string{"/routing/shadow?days=0", "/routing/shadow?days=91", "/routing/shadow?offset=-1"} {
		testutil.Call(t, testHandler.ListRoutingShadowTickets, dispatchRequest(http.MethodGet, path, nil)).Want(http.StatusBadRequest)
	}
	var out struct {
		Items []map[string]string `json:"items"`
		Days  int                 `json:"days"`
	}
	testutil.Call(t, testHandler.ListRoutingShadowTickets, dispatchRequest(http.MethodGet, "/routing/shadow?days=30", nil)).Want(http.StatusOK).JSON(&out)
	if out.Days != 30 || out.Items == nil {
		t.Fatalf("invalid report: %+v", out)
	}
}
