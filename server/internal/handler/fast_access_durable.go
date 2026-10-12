package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"sort"
	"strconv"
	"time"
)

var errFastReset = errors.New("reset_required")
var errFastPermission = errors.New("scope_reset")
var errFastBudget = errors.New("byte_budget_too_small")

type fastCursor struct {
	Scanned    map[string]int64
	BatchID    string
	Batch      fastBatch
	Offset     int
	Checkpoint bool
}

// Handles live in PostgreSQL, not process memory. They contain only bounded
// compact projections and bind the existing principal, task, scope and filter.
func (h *Handler) fastLoadCursor(ctx context.Context, token, principal string) (*fastCursor, error) {
	var raw []byte
	err := h.DB.QueryRow(ctx, `SELECT payload FROM fast_access_cursor WHERE token=$1 AND principal=$2 AND expires_at>clock_timestamp()`, token, principal).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errFastReset
	}
	if err != nil {
		return nil, err
	}
	var cursor fastCursor
	if err = json.Unmarshal(raw, &cursor); err != nil {
		return nil, err
	}
	if cursor.BatchID != "" {
		err = h.DB.QueryRow(ctx, `SELECT payload FROM fast_access_batch WHERE id=$1 AND principal=$2 AND expires_at>clock_timestamp()`, cursor.BatchID, principal).Scan(&raw)
		if err != nil {
			return nil, errFastReset
		}
		if err = json.Unmarshal(raw, &cursor.Batch); err != nil {
			return nil, err
		}
	}
	return &cursor, nil
}
func (h *Handler) fastSaveCursor(ctx context.Context, token string, c fastCursor) error {
	// Release expired materializations opportunistically, bounded per request.
	_, err := h.DB.Exec(ctx, `DELETE FROM fast_access_cursor WHERE token IN (SELECT token FROM fast_access_cursor WHERE expires_at<clock_timestamp() LIMIT 100)`)
	if err != nil {
		return err
	}
	_, err = h.DB.Exec(ctx, `DELETE FROM fast_access_batch WHERE id IN (SELECT id FROM fast_access_batch WHERE expires_at<clock_timestamp() LIMIT 100)`)
	if err != nil {
		return err
	}
	_, err = h.DB.Exec(ctx, `SELECT fast_access_prune()`)
	if err != nil {
		return err
	}
	principal, expires := c.Batch.Principal, c.Batch.Expires
	if !c.Checkpoint {
		raw, e := json.Marshal(c.Batch)
		if e != nil {
			return e
		}
		_, err = h.DB.Exec(ctx, `INSERT INTO fast_access_batch(id,principal,payload,expires_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, c.Batch.ID, principal, raw, expires)
		if err != nil {
			return err
		}
		c.BatchID = c.Batch.ID
		c.Batch = fastBatch{}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = h.DB.Exec(ctx, `INSERT INTO fast_access_cursor(token,principal,payload,expires_at) VALUES($1,$2,$3,$4)`, token, principal, raw, expires)
	return err
}

// Membership/visibility is recalculated even with an unchanged permission
// epoch (for example an access pass expires without a database write).
func (h *Handler) fastState(r *http.Request, viewer string, req fastRequest) ([]fastWorkspace, string, map[string]int64, time.Time, error) {
	scopes, err := h.fastScopes(r, viewer, req)
	if err != nil {
		return nil, "", nil, time.Time{}, err
	}
	var epoch int64
	var floor time.Time
	err = h.DB.QueryRow(r.Context(), `SELECT permission_revision,GREATEST(coverage_from,clock_timestamp()-interval '7 days') FROM fast_access_meta WHERE singleton`).Scan(&epoch, &floor)
	if err != nil {
		return nil, "", nil, floor, err
	}
	parts := []string{strconv.FormatInt(epoch, 10)}
	marks := map[string]int64{}
	for _, s := range scopes {
		parts = append(parts, s.ID+":"+string(s.Viewer.role)+fmt.Sprint(s.Managed, s.IssuesAllowed))
		for _, a := range s.Agents {
			parts = append(parts, s.ID+":agent:"+uuidToString(a))
		}
		for _, p := range s.Viewer.projectIDs {
			parts = append(parts, s.ID+":member-project:"+uuidToString(p))
		}
		for _, p := range s.LinkedProjects {
			parts = append(parts, s.ID+":link-project:"+uuidToString(p))
		}
		var n int64
		if err = h.DB.QueryRow(r.Context(), `SELECT COALESCE((SELECT seq FROM fast_access_feed WHERE workspace_id=$1),0)`, parseUUID(s.ID)).Scan(&n); err != nil {
			return nil, "", nil, floor, err
		}
		marks[s.ID] = n
	}
	return scopes, fastDigest(parts), marks, floor, nil
}

func (h *Handler) fastDurableCollect(r *http.Request, viewer string, req fastRequest, previous *fastBatch) (*fastBatch, error) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY"); err != nil {
		return nil, err
	}
	reader := *h
	reader.DB = tx
	reader.Queries = h.Queries.WithTx(tx)
	scopes, revision, upper, floor, err := reader.fastState(r, viewer, req)
	if err != nil {
		return nil, err
	}
	if previous != nil && previous.ScopeRevision != revision {
		return nil, errFastPermission
	}
	if previous != nil && previous.AsOf.Before(floor) {
		return nil, errFastReset
	}
	if req.Since != nil && req.Since.Before(floor) {
		return nil, errFastReset
	}
	if req.Since != nil {
		var changed time.Time
		if err = tx.QueryRow(r.Context(), `SELECT permission_changed_at FROM fast_access_meta WHERE singleton`).Scan(&changed); err != nil {
			return nil, err
		}
		if req.Since.Before(changed) {
			return nil, errFastPermission
		}
	}
	var at time.Time
	if err = tx.QueryRow(r.Context(), "SELECT clock_timestamp()").Scan(&at); err != nil {
		return nil, err
	}
	// Read all current summaries in the SAME snapshot as the committed prefix.
	currentReq := req
	currentReq.Since = nil
	currentReq.countSince = req.Since
	currentReq.runFloor = &floor
	// Delta run authorization is resolved only for each bounded journal page.
	// Do not materialize terminal attempts into the current-object snapshot.
	currentReq.activeOnly = previous != nil || req.Since != nil
	if previous != nil {
		currentReq.countSince = &previous.AsOf
	}
	items, err := reader.fastRows(r.Context(), scopes, currentReq, at, nil)
	if err != nil {
		return nil, err
	}
	b := &fastBatch{ID: randomID(), Request: req, AsOf: at, CoverageFrom: floor, Mode: "durable_delta", ScopeRevision: revision, Watermarks: upper, Items: []fastItem{}}
	for _, item := range items {
		item.Revision = strconv.FormatInt(upper[item.WorkspaceID], 10)
		if item.active && fastIncludes(req, "active_runs") {
			b.Items = append(b.Items, item)
		}
	}
	b.ActiveCount = len(b.Items)
	if previous == nil && req.Since == nil {
		for _, item := range items {
			if item.active && fastIncludes(req, "active_runs") {
				continue
			}
			item.Revision = strconv.FormatInt(upper[item.WorkspaceID], 10)
			b.Items = append(b.Items, item)
		}
	} else {
		b.Delta = true
		b.Current = items
		b.Lower = map[string]int64{}
		for _, scope := range scopes {
			if previous != nil {
				b.Lower[scope.ID] = previous.Watermarks[scope.ID]
			} else {
				var low int64
				// Skip only the prefix preceding the first eligible event. This
				// also keeps active-first pages clear of opportunistic pruning
				// of older physical rows. Never use max(seq) of excluded rows:
				// that could skip an eligible event before an excluded one.
				if err = tx.QueryRow(r.Context(), `SELECT COALESCE(min(seq)-1,$3) FROM fast_access_journal WHERE workspace_id=$1 AND seq<=$3 AND changed_at >= $2`, parseUUID(scope.ID), req.Since, upper[scope.ID]).Scan(&low); err != nil {
					return nil, err
				}
				b.Lower[scope.ID] = low
			}
		}
	}
	if len(b.Items) > 10000 {
		return nil, errFastLarge
	}
	if err = tx.Commit(r.Context()); err != nil {
		return nil, err
	}
	return b, nil
}

func (h *Handler) fastCheckBatch(r *http.Request, viewer string, b *fastBatch, pageItems ...fastItem) error {
	_, revision, _, floor, err := h.fastState(r, viewer, b.Request)
	if errors.Is(err, errFastScope) {
		return errFastPermission
	}
	if err != nil {
		return err
	}
	if revision != b.ScopeRevision {
		return errFastPermission
	}
	if b.AsOf.Before(floor) {
		return errFastReset
	}
	if len(b.Items) == 0 && len(b.Current) == 0 && len(pageItems) == 0 {
		return nil
	}
	// Object/source rechecks are mandatory even when the epoch is unchanged.
	req := b.Request
	req.Since = nil
	scopes, err := h.fastScopes(r, viewer, req)
	if err != nil {
		return err
	}
	authorized := append(append(append([]fastItem{}, b.Items...), b.Current...), pageItems...)
	items, err := h.fastRows(r.Context(), scopes, req, b.AsOf, authorized)
	if err != nil {
		return err
	}
	visible := map[string]bool{}
	for _, i := range items {
		visible[fastKey(i)] = true
	}
	for _, i := range authorized {
		if !visible[fastKey(i)] {
			return errFastPermission
		}
	}
	return nil
}
func fastWriteFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errFastPermission):
		fastError(w, 409, "scope_reset", "access changed; clear cached scope and rebuild")
	case errors.Is(err, errFastReset):
		fastError(w, 410, "reset_required", "history or cursor unavailable; rebuild with a new read without since")
	case errors.Is(err, errFastScope):
		fastError(w, 403, "scope_denied", "requested scope is not accessible")
	case errors.Is(err, errFastLarge):
		fastError(w, 422, "snapshot_too_large", errFastLarge.Error())
	default:
		fastError(w, 503, "read_unavailable", "cannot read durable activity")
	}
}
func (h *Handler) fastDurableRead(w http.ResponseWriter, r *http.Request, req fastRequest, viewer, principal string) {
	var b *fastBatch
	offset := 0
	if req.Cursor != "" {
		c, err := h.fastLoadCursor(r.Context(), req.Cursor, principal)
		if err != nil {
			fastWriteFailure(w, err)
			return
		}
		if err = h.fastCheckBatch(r, viewer, &c.Batch); err != nil {
			fastWriteFailure(w, err)
			return
		}
		if c.Checkpoint {
			b, err = h.fastDurableCollect(r, viewer, c.Batch.Request, &c.Batch)
			if err != nil {
				fastWriteFailure(w, err)
				return
			}
		} else {
			b = &c.Batch
			offset = c.Offset
			if c.Scanned != nil {
				b.Lower = c.Scanned
			}
		}
	} else {
		var err error
		b, err = h.fastDurableCollect(r, viewer, req, nil)
		if err != nil {
			fastWriteFailure(w, err)
			return
		}
	}
	b.Principal = principal
	if b.Expires.IsZero() {
		b.Expires = time.Now().Add(5 * time.Minute)
	}
	next, checkpoint := randomID(), randomID()
	response, raw, end, err := fastPage(b, offset, req.Limit, req.MaxBytes, next, checkpoint)
	scanned := b.Lower
	if b.Delta {
		response, raw, end, scanned, err = h.fastDeltaPage(r, viewer, b, offset, req.Limit, req.MaxBytes, next, checkpoint)
	}
	if err != nil {
		if b.Delta && !errors.Is(err, errFastBudget) {
			fastWriteFailure(w, err)
		} else {
			fastError(w, 422, "byte_budget_too_small", err.Error())
		}
		return
	}
	// Recheck after collecting, before emitting or persisting a page.
	if err = h.fastCheckBatch(r, viewer, b, response.Changes...); err != nil {
		fastWriteFailure(w, err)
		return
	}
	if response.Complete {
		cp := *b
		cp.Items = nil
		cp.Current = nil
		cp.Lower = nil
		cp.Delta = false
		cp.ActiveCount = 0
		cp.Request.Since = nil
		cp.Expires = b.AsOf.Add(7 * 24 * time.Hour)
		err = h.fastSaveCursor(r.Context(), checkpoint, fastCursor{Batch: cp, Checkpoint: true})
	} else {
		err = h.fastSaveCursor(r.Context(), next, fastCursor{Batch: *b, Offset: end, Scanned: scanned})
	}
	if err != nil {
		fastWriteFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(raw)
}

// Historical event pages scan a bounded number of journal entries against a
// fixed upper bound and fixed, authorized summaries. Invisible events consume
// scan positions, not response slots; an empty page can still have next_cursor.
func (h *Handler) fastDeltaPage(r *http.Request, viewer string, b *fastBatch, offset, limit, budget int, next, checkpoint string) (fastResponse, []byte, int, map[string]int64, error) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return fastResponse{}, nil, offset, nil, err
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY"); err != nil {
		return fastResponse{}, nil, offset, nil, err
	}
	for ws, low := range b.Lower {
		var floorSeq int64
		var expired bool
		if err = tx.QueryRow(r.Context(), `SELECT COALESCE((SELECT floor_seq FROM fast_access_feed WHERE workspace_id=$1),0),EXISTS(SELECT 1 FROM fast_access_journal WHERE workspace_id=$1 AND seq>$2 AND seq<=$3 AND changed_at<clock_timestamp()-interval '7 days' AND ($4::timestamptz IS NULL OR changed_at >= $4))`, parseUUID(ws), low, b.Watermarks[ws], b.Request.Since).Scan(&floorSeq, &expired); err != nil {
			return fastResponse{}, nil, offset, nil, err
		}
		if low < floorSeq || expired {
			return fastResponse{}, nil, offset, nil, errFastReset
		}
	}
	floor := b.CoverageFrom
	response := fastResponse{SchemaVersion: "1", Mode: "durable_delta", AsOf: b.AsOf, CoverageFrom: floor, RetentionFloor: &floor, ScopeRevision: b.ScopeRevision, Changes: []fastItem{}, ActiveRuns: []fastItem{}}
	scanned := map[string]int64{}
	for ws, n := range b.Lower {
		scanned[ws] = n
	}
	end := offset
	finish := func() ([]byte, error) {
		response.ActiveRunsComplete = end >= b.ActiveCount
		response.Complete = response.ActiveRunsComplete
		for ws, n := range b.Watermarks {
			if scanned[ws] < n {
				response.Complete = false
			}
		}
		response.NextCursor = nil
		response.Checkpoint = nil
		if response.Complete {
			response.Checkpoint = &checkpoint
		} else {
			response.NextCursor = &next
		}
		return json.Marshal(response)
	}
	pack := func() (bool, error) { raw, e := finish(); return len(raw) <= budget, e }
	for end < b.ActiveCount && len(response.ActiveRuns) < limit {
		response.ActiveRuns = append(response.ActiveRuns, b.Items[end])
		end++
		ok, e := pack()
		if e != nil {
			return response, nil, end, scanned, e
		}
		if !ok {
			end--
			response.ActiveRuns = response.ActiveRuns[:len(response.ActiveRuns)-1]
			break
		}
	}
	remaining := limit - len(response.ActiveRuns)
	reader := *h
	reader.DB = tx
	reader.Queries = h.Queries.WithTx(tx)
	scopes, err := reader.fastScopes(r, viewer, b.Request)
	if err != nil {
		return response, nil, end, scanned, err
	}
	visible := map[string]fastItem{}
	for _, i := range b.Current {
		visible[fastKey(i)] = i
	}
	workspaces := []string{}
	for ws := range b.Watermarks {
		workspaces = append(workspaces, ws)
	}
	sort.Strings(workspaces)
	if end >= b.ActiveCount {
		for _, ws := range workspaces {
			if remaining == 0 {
				break
			}
			if scanned[ws] >= b.Watermarks[ws] {
				continue
			}
			rows, e := tx.Query(r.Context(), `SELECT seq,event_id::text,kind,entity_id::text,status,changed_at FROM fast_access_journal WHERE workspace_id=$1 AND seq>$2 AND seq<=$3 AND ($4::timestamptz IS NULL OR changed_at >= $4) ORDER BY seq LIMIT $5`, parseUUID(ws), scanned[ws], b.Watermarks[ws], b.Request.Since, remaining)
			if e != nil {
				return response, nil, end, scanned, e
			}
			type journalEvent struct {
				seq                     int64
				event, kind, id, status string
				changed                 time.Time
			}
			events := []journalEvent{}
			runIDs := []fastItem{}
			for rows.Next() {
				var ev journalEvent
				if e = rows.Scan(&ev.seq, &ev.event, &ev.kind, &ev.id, &ev.status, &ev.changed); e != nil {
					rows.Close()
					return response, nil, end, scanned, e
				}
				events = append(events, ev)
				if fastIncludes(b.Request, "run_changes") && (ev.kind == "run" || ev.kind == "autopilot_run") {
					runIDs = append(runIDs, fastItem{WorkspaceID: ws, Kind: ev.kind, ID: ev.id})
				}
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return response, nil, end, scanned, e
			}
			if len(runIDs) > 0 {
				runReq := b.Request
				runReq.Include = []string{"run_changes"}
				projections, e := reader.fastRows(r.Context(), scopes, runReq, b.AsOf, runIDs)
				if e != nil {
					return response, nil, end, scanned, e
				}
				for _, item := range projections {
					visible[fastKey(item)] = item
				}
			}
			exhausted := true
			for _, ev := range events {
				seq, event, kind, id, status, changed := ev.seq, ev.event, ev.kind, ev.id, ev.status, ev.changed
				remaining--
				item, ok := visible[ws+"/"+kind+"/"+id]
				if (kind == "run" || kind == "autopilot_run") && !fastIncludes(b.Request, "run_changes") {
					ok = false
				}
				if ok {
					item.EventID = event
					item.Revision = strconv.FormatInt(seq, 10)
					item.ChangedAt = changed
					if kind == "run" || kind == "autopilot_run" {
						item.Status = status
						item.Phase = fastPhase(status)
						item.StartedAt = nil
						item.FinishedAt = nil
					}
					response.Changes = append(response.Changes, item)
					fits, e := pack()
					if e != nil {
						return response, nil, end, scanned, e
					}
					if !fits {
						response.Changes = response.Changes[:len(response.Changes)-1]
						exhausted = false
						remaining = 0
						break
					}
				}
				scanned[ws] = seq
				if remaining == 0 {
					exhausted = false
					break
				}
			}
			if exhausted {
				scanned[ws] = b.Watermarks[ws]
			}
		}
	}
	advanced := end > offset
	for ws, n := range scanned {
		if n > b.Lower[ws] {
			advanced = true
		}
	}
	raw, e := finish()
	if e != nil {
		return response, nil, end, scanned, e
	}
	if len(raw) > budget || (!advanced && !response.Complete) {
		return response, nil, end, scanned, fmt.Errorf("%w: max_bytes cannot fit the next item or envelope", errFastBudget)
	}
	if err = tx.Commit(r.Context()); err != nil {
		return response, nil, end, scanned, err
	}
	return response, raw, end, scanned, nil
}
