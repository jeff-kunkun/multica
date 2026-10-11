package daemonws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWebSocketRevalidatesCredentialAfterUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"revoked", ErrAuthExpired, websocket.ClosePolicyViolation},
		{"storage unavailable", errors.New("db unavailable"), websocket.CloseTryAgainLater},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewHub()
			hub.authCheckInterval = 10 * time.Millisecond
			var rejected atomic.Bool
			hub.SetRPCHandler(func(context.Context, ClientIdentity, string, json.RawMessage) (int, json.RawMessage, error) {
				return http.StatusOK, json.RawMessage(`{}`), nil
			})
			conn := dialRPCTestConn(t, hub, ClientIdentity{UserID: "user", RuntimeIDs: []string{"rt"}, Revalidate: func(context.Context) error {
				if rejected.Load() {
					return tc.err
				}
				return nil
			}})
			resp := sendRPCRequest(t, conn, protocol.RPCRequestPayload{RequestID: "before-revoke", Method: "tasks.claim", Body: json.RawMessage(`{}`)})
			if resp.Status != 200 {
				t.Fatalf("valid connection failed: %+v", resp)
			}
			rejected.Store(true)
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _, err := conn.ReadMessage()
			if !websocket.IsCloseError(err, tc.code) {
				t.Fatalf("close=%v, expected %d", err, tc.code)
			}
			deadline := time.Now().Add(time.Second)
			for hub.RuntimeConnectionCount("rt") != 0 || hub.UserConnectionCount("user") != 0 {
				if time.Now().After(deadline) {
					t.Fatal("revoked connection remains in presence")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestWebSocketAuthProbeStopsWithConnection(t *testing.T) {
	hub := NewHub()
	hub.authCheckInterval = 10 * time.Millisecond
	entered := make(chan struct{})
	stopped := make(chan struct{})
	conn := dialRPCTestConn(t, hub, ClientIdentity{UserID: "user", Revalidate: func(ctx context.Context) error { close(entered); <-ctx.Done(); close(stopped); return ctx.Err() }})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe never started")
	}
	conn.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("probe outlived connection")
	}
}
