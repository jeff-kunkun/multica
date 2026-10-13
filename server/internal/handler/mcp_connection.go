package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/fastmcp"
	"github.com/multica-ai/multica/server/internal/mcpaccess"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func mcpSecret(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

func (h *Handler) mcpManager(w http.ResponseWriter, r *http.Request) (pgtype.UUID, pgtype.UUID, bool) {
	if isMachineCredentialActor(r) || botsource.FromContext(r.Context()).ID.Valid {
		writeError(w, 403, "only a person can manage MCP connections")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	return h.mcpInventoryReader(w, r)
}

// Inventory contains no authorization material. Legacy Bots retain their
// owner's read-only overview while connection and route writes stay human-only.
func (h *Handler) mcpInventoryReader(w http.ResponseWriter, r *http.Request) (pgtype.UUID, pgtype.UUID, bool) {
	ws, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return ws, pgtype.UUID{}, false
	}
	user, ok := requireUserID(w, r)
	if !ok {
		return ws, pgtype.UUID{}, false
	}
	if isMachineCredentialActor(r) {
		writeError(w, 403, "machine credentials cannot read connection inventory")
		return ws, pgtype.UUID{}, false
	}
	member, ok := h.workspaceMember(w, r, uuidToString(ws))
	if !ok {
		return ws, pgtype.UUID{}, false
	}
	if !roleAllowed(member.Role, "owner", "admin") {
		writeError(w, 403, "only workspace owners and admins can manage MCP connections")
		return ws, pgtype.UUID{}, false
	}
	return ws, parseUUID(user), true
}

func (h *Handler) CreateMCPConnection(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.mcpManager(w, r)
	if !ok {
		return
	}
	var req struct {
		Name  string `json:"name"`
		BotID string `json:"bot_id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) == 0 || len(req.Name) > 100 {
		writeError(w, 400, "name must contain 1-100 bytes")
		return
	}
	botID, ok := parseUUIDOrBadRequest(w, req.BotID, "bot id")
	if !ok {
		return
	}
	bot, err := h.Queries.GetWakeBot(r.Context(), db.GetWakeBotParams{ID: botID, WorkspaceID: ws})
	if err != nil || bot.UserID != user || bot.DeletedAt.Valid || bot.Revoked {
		writeError(w, 404, "active owned Bot not found")
		return
	}
	code, err := mcpSecret("mcp_code_")
	if err != nil {
		writeError(w, 500, "could not create connection")
		return
	}
	row, err := h.Queries.CreateMCPConnection(r.Context(), db.CreateMCPConnectionParams{WorkspaceID: ws, UserID: user, BotID: botID, Name: req.Name, CodeHash: pgtype.Text{String: auth.HashToken(code), Valid: true}})
	if err != nil {
		writeError(w, 500, "could not create connection")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	base := strings.TrimRight(h.cfg.PublicURL, "/")
	writeJSON(w, 201, map[string]any{"id": uuidToString(row.ID), "code": code, "expires_at": timestampToString(row.CodeExpiresAt), "endpoint": base + "/api/workspaces/" + uuidToString(ws) + "/mcp", "exchange_url": base + "/api/mcp/exchange"})
}

func (h *Handler) ListMCPConnections(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.mcpInventoryReader(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListMCPConnections(r.Context(), db.ListMCPConnectionsParams{WorkspaceID: ws, UserID: user})
	if err != nil {
		writeError(w, 500, "could not list connections")
		return
	}
	out := []map[string]any{}
	for _, row := range rows {
		out = append(out, map[string]any{"id": uuidToString(row.ID), "name": row.Name, "bot_id": uuidToString(row.BotID), "expires_at": timestampToString(row.ExpiresAt), "created_at": timestampToString(row.CreatedAt), "last_used_at": timestampToPtr(row.LastUsedAt), "revoked": row.Revoked})
	}
	writeJSON(w, 200, out)
}

func (h *Handler) RevokeMCPConnection(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.mcpManager(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "connectionId"), "connection id")
	if !ok {
		return
	}
	n, err := h.Queries.RevokeMCPConnection(r.Context(), db.RevokeMCPConnectionParams{ID: id, WorkspaceID: ws, UserID: user})
	if err != nil {
		writeError(w, 500, "could not revoke connection")
		return
	}
	if n == 0 {
		writeError(w, 404, "connection not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ExchangeMCPConnection consumes a high-entropy five-minute code in one SQL
// update. Parallel/replayed exchanges cannot mint a second credential. Only
// hashes are persisted; existing PATs and browser sessions are never touched.
func (h *Handler) ExchangeMCPConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var req struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&req) != nil || len(req.Code) != 73 || !strings.HasPrefix(req.Code, "mcp_code_") {
		writeError(w, 400, "invalid or expired authorization code")
		return
	}
	token, err := mcpSecret("mmc_")
	if err != nil {
		writeError(w, 500, "could not authorize connection")
		return
	}
	row, err := h.Queries.ExchangeMCPConnection(r.Context(), db.ExchangeMCPConnectionParams{CodeHash: pgtype.Text{String: auth.HashToken(req.Code), Valid: true}, TokenHash: pgtype.Text{String: auth.HashToken(token), Valid: true}})
	if err != nil {
		writeError(w, 400, "invalid or expired authorization code")
		return
	}
	writeJSON(w, 200, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": int(time.Until(row.ExpiresAt.Time).Seconds()), "endpoint": strings.TrimRight(h.cfg.PublicURL, "/") + "/api/workspaces/" + uuidToString(row.WorkspaceID) + "/mcp"})
}

// RemoteMCP never reuses an MCP server across identities. Stateless JSON
// responses keep reconnect independent of a particular replica; durable event
// cursors and receipts live in the existing office event ledger.
func (h *Handler) RemoteMCP(api http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Actor-Source") != "mcp_connection" {
			writeError(w, 401, "authorize a remote MCP connection first")
			return
		}
		if r.Header.Get("Origin") != "" {
			writeError(w, 403, "browser origins cannot call the remote MCP endpoint")
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		client := cli.NewAPIClient("http://multica.internal", r.Header.Get("X-Workspace-ID"), token)
		client.HTTPClient = &http.Client{Transport: mcpaccess.Transport{Handler: api}, Timeout: 30 * time.Second}
		server := fastmcp.NewRemoteServer(client)
		transport := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
		transport.ServeHTTP(w, r)
	}
}
