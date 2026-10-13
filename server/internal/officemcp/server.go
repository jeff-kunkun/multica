// Package officemcp is the remote MCP server a project-manager Bot (Grok and
// the like) connects to. It speaks MCP over stateless Streamable HTTP
// (JSON-RPC in a POST, JSON reply) and implements every tool by calling the
// office's own REST API in-process with the Bot's token, so authorization,
// validation and events are exactly what the REST API and CLI already do.
package officemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/go-chi/chi/v5"
)

// ProtocolVersion is the newest MCP revision this server speaks. Clients that
// ask for an older revision get it echoed back; the tool surface is the same.
const ProtocolVersion = "2025-06-18"

var supportedVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// Bot is the caller resolved from the request's token.
type Bot struct {
	ID          string
	Name        string
	WorkspaceID string
	CanRelease  bool
}

// LookupBot resolves the authenticated user (X-User-ID, set by the auth
// middleware) to its Bot record. ok=false rejects the request.
type LookupBot func(ctx context.Context, userID string) (Bot, bool)

// Server serves the MCP endpoint.
type Server struct {
	api     http.Handler
	lookup  LookupBot
	version string
	tools   []tool
	byName  map[string]tool
}

// New builds a Server whose tools call api (the office router).
func New(api http.Handler, lookup LookupBot, version string) *Server {
	s := &Server{api: api, lookup: lookup, version: version, tools: officeTools()}
	s.byName = make(map[string]tool, len(s.tools))
	for _, t := range s.tools {
		s.byName[t.Name] = t
	}
	return s
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// No server-initiated stream: events reach the Bot by webhook or
		// the get_events tool.
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	bot, ok := s.lookup(r.Context(), r.Header.Get("X-User-ID"))
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a bot token can use the MCP endpoint"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
		return
	}
	c := &caller{api: s.api, origin: r, bot: bot}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var batch []rpcRequest
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			return
		}
		var out []rpcResponse
		for _, req := range batch {
			if resp, ok := s.handle(c, req); ok {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
		return
	}
	resp, ok := s.handle(c, req)
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handle answers one JSON-RPC message; ok=false for notifications.
func (s *Server) handle(c *caller, req rpcRequest) (rpcResponse, bool) {
	if len(req.ID) == 0 {
		return rpcResponse{}, false
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := ProtocolVersion
		if supportedVersions[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		resp.Result = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "multica-office", "version": s.version},
			"instructions":    serverInstructions,
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		list := make([]map[string]any, len(s.tools))
		for i, t := range s.tools {
			list[i] = map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema}
		}
		resp.Result = map[string]any{"tools": list}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{-32602, "invalid params"}
			break
		}
		t, ok := s.byName[p.Name]
		if !ok {
			resp.Error = &rpcError{-32602, "unknown tool: " + p.Name}
			break
		}
		args := args{}
		if len(p.Arguments) > 0 && string(p.Arguments) != "null" {
			if err := json.Unmarshal(p.Arguments, &args); err != nil {
				resp.Error = &rpcError{-32602, "arguments must be an object"}
				break
			}
		}
		out, err := t.Call(c, args)
		resp.Result = toolResult(out, err)
	default:
		resp.Error = &rpcError{-32601, "method not found: " + req.Method}
	}
	return resp, true
}

// toolResult wraps a tool's output (or failure) as MCP content. Failures are
// tool errors (isError), not protocol errors, so the model sees the reason.
func toolResult(out any, err error) map[string]any {
	if err != nil {
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		}
	}
	var text string
	switch v := out.(type) {
	case json.RawMessage:
		text = string(v)
	case string:
		text = v
	default:
		raw, _ := json.Marshal(v)
		text = string(raw)
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

// caller runs REST calls in-process as the Bot.
type caller struct {
	api    http.Handler
	origin *http.Request
	bot    Bot
}

// apiError is a non-2xx REST reply surfaced to the model.
type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	msg := strings.TrimSpace(e.Body)
	var parsed struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(msg), &parsed) == nil && parsed.Error != "" {
		msg = parsed.Error
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, msg)
}

func (c *caller) do(method, path string, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	// A nil chi route context makes the router route the inner request
	// from scratch instead of reusing the outer /api/mcp match.
	ctx := context.WithValue(c.origin.Context(), chi.RouteCtxKey, nil)
	req, err := http.NewRequestWithContext(ctx, method, path, reader)
	if err != nil {
		return nil, err
	}
	req.RemoteAddr = c.origin.RemoteAddr
	req.Host = c.origin.Host
	for _, h := range []string{"Authorization", "User-Agent", "X-Forwarded-For", "X-Real-IP"} {
		if v := c.origin.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	req.Header.Set("X-Workspace-ID", c.bot.WorkspaceID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	c.api.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code > 299 {
		return nil, &apiError{Status: rec.Code, Body: rec.Body.String()}
	}
	if rec.Body.Len() == 0 {
		return json.RawMessage(`{"ok":true}`), nil
	}
	return json.RawMessage(rec.Body.Bytes()), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
