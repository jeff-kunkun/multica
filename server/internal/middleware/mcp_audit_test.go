package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestMCPEnvelopeAndReadDoNotCreateActivity(t *testing.T) {
	const ws = "00000000-0000-4000-8000-000000000001"
	var bot pgtype.UUID
	_ = bot.Scan(ws)
	for _, path := range []string{"/api/workspaces/" + ws + "/mcp", "/api/fast-access/v1/read"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("POST", path, nil)
			req.Header.Set("X-Actor-Source", "mcp_connection")
			req.Header.Set("X-Workspace-ID", ws)
			req = req.WithContext(botsource.WithContext(req.Context(), botsource.Source{ID: bot, Name: "fixture"}))
			observed := 0
			called := false
			audit := BotWriteAudit(nil, func(context.Context, service.WorkspaceWrite) { observed++ }, nil)
			rec := httptest.NewRecorder()
			audit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) })).ServeHTTP(rec, req)
			if !called || observed != 0 {
				t.Fatalf("called=%v observed=%d", called, observed)
			}
		})
	}
}
