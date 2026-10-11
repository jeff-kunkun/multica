package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Events are compact journal notifications. The durable read checkpoint is
// separate from SSE's Last-Event-ID: consumers batch reads from their applied
// checkpoint, and only commit that checkpoint after all pages are applied.
func (h *Handler) FastAccessEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUserID(w, r)
	if !ok {
		return
	}
	viewer, err := h.fastViewer(r, user)
	if err != nil || viewer == "" {
		fastError(w, 403, "scope_denied", "cannot resolve principal")
		return
	}
	principal := viewer + ":" + r.Header.Get("X-Actor-Source") + ":" + r.Header.Get("X-Task-ID") + ":" + ctxWorkspaceID(r.Context())
	token := r.Header.Get("Last-Event-ID")
	if token == "" {
		token = r.URL.Query().Get("cursor")
	}
	if token == "" {
		fastError(w, 400, "invalid_request", "a completed read checkpoint is required")
		return
	}
	c, err := h.fastLoadCursor(r.Context(), token, principal)
	if err != nil {
		fastWriteFailure(w, err)
		return
	}
	if !c.Checkpoint {
		fastError(w, 400, "invalid_request", "finish pagination before subscribing")
		return
	}
	if err = h.fastCheckBatch(r, viewer, &c.Batch); err != nil {
		fastWriteFailure(w, err)
		return
	}
	// Shared leases enforce two live streams across replicas; crashes expire.
	lease := randomID()
	slot := 0
	for i := 1; i <= 2; i++ {
		result, e := h.DB.Exec(r.Context(), `INSERT INTO fast_access_stream(principal,slot,lease_id,expires_at) VALUES($1,$2,$3,clock_timestamp()+interval '45 seconds') ON CONFLICT(principal,slot) DO UPDATE SET lease_id=EXCLUDED.lease_id,expires_at=EXCLUDED.expires_at WHERE fast_access_stream.expires_at<clock_timestamp()`, viewer, i, lease)
		if e != nil {
			fastWriteFailure(w, e)
			return
		}
		if result.RowsAffected() == 1 {
			slot = i
			break
		}
	}
	if slot == 0 {
		w.Header().Set("Retry-After", "30")
		fastError(w, 429, "stream_limit", "two streams are already open")
		return
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = h.DB.Exec(ctx, `DELETE FROM fast_access_stream WHERE principal=$1 AND slot=$2 AND lease_id=$3`, viewer, slot, lease)
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	control := http.NewResponseController(w)
	write := func(s string) bool {
		_ = control.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, e := fmt.Fprint(w, s); e != nil {
			return false
		}
		return control.Flush() == nil
	}
	if !write(": connected\n\n") {
		return
	}
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	keep := time.NewTicker(30 * time.Second)
	defer keep.Stop()
	for {
		// Polling the journal is the correctness path; WS/Redis is unnecessary
		// for correctness and no in-memory unbounded producer queue exists.
		_, revision, upper, _, e := h.fastState(r, viewer, c.Batch.Request)
		if e != nil || revision != c.Batch.ScopeRevision {
			write("event: scope_reset\ndata: {\"code\":\"scope_reset\"}\n\n")
			return
		}
		changed := false
		for ws, n := range upper {
			if n != c.Batch.Watermarks[ws] {
				changed = true
			}
		}
		if changed {
			req := c.Batch.Request
			b, e := h.fastDurableCollect(r, viewer, req, &c.Batch)
			if e != nil {
				write("event: reset_required\ndata: {\"code\":\"reset_required\"}\n\n")
				return
			}
			// Stream notifications advance a bounded segment. Readers retain
			// their own applied checkpoint and paginate a fixed read snapshot.
			b.Items = nil
			b.ActiveCount = 0
			page, _, _, scanned, e := h.fastDeltaPage(r, viewer, b, 0, 200, 50000, "next", "checkpoint")
			if e != nil {
				write("event: read_required\ndata: {}\n\n")
				return
			}
			// Notifications are bounded to 1 MB, never buffered indefinitely.
			// Beyond that, require a compact read rather than truncate events.
			data := make([]byte, 0, 50000)
			for _, item := range page.Changes {
				record := map[string]any{"event_id": item.EventID, "workspace_id": item.WorkspaceID, "entity_kind": item.Kind, "entity_id": item.ID, "revision": item.Revision, "op": item.Op, "changed_at": item.ChangedAt}
				raw, _ := json.Marshal(record)
				data = append(data, []byte("event: change\ndata: ")...)
				data = append(data, raw...)
				data = append(data, '\n', '\n')
				if len(data) > 1024*1024 {
					write("event: read_required\ndata: {}\n\n")
					return
				}
			}
			if e = h.fastCheckBatch(r, viewer, b, page.Changes...); e != nil {
				write("event: scope_reset\ndata: {\"code\":\"scope_reset\"}\n\n")
				return
			}
			b.Principal = principal
			b.Items = nil
			b.Current = nil
			b.Delta = false
			b.Lower = nil
			b.Watermarks = scanned
			b.ActiveCount = 0
			b.Request.Since = nil
			b.Expires = b.AsOf.Add(7 * 24 * time.Hour)
			token = randomID()
			next := fastCursor{Batch: *b, Checkpoint: true}
			if e = h.fastSaveCursor(r.Context(), token, next); e != nil {
				return
			}
			// The id is emitted only AFTER every event in this batch. A broken
			// connection before it replays the batch (at least once).
			if !write(string(data) + "event: checkpoint\nid: " + token + "\ndata: {}\n\n") {
				return
			}
			c = &next
		}
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-keep.C:
			result, e := h.DB.Exec(r.Context(), `UPDATE fast_access_stream SET expires_at=clock_timestamp()+interval '45 seconds' WHERE principal=$1 AND slot=$2 AND lease_id=$3 AND expires_at>clock_timestamp()`, viewer, slot, lease)
			if e != nil || result.RowsAffected() != 1 {
				return
			}
			if !write(": keepalive\n\n") {
				return
			}
		}
	}
}
