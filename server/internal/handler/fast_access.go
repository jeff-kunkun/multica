package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Shared envelope and bounded page packing for snapshot and durable deltas.
type fastScope struct {
	Mode         string   `json:"mode"`
	WorkspaceIDs []string `json:"workspace_ids,omitempty"`
}
type fastRequest struct {
	countSince *time.Time
	runFloor   *time.Time
	activeOnly bool
	Scope      *fastScope `json:"scope,omitempty"`
	Since      *time.Time `json:"since,omitempty"`
	Cursor     string     `json:"cursor,omitempty"`
	Include    []string   `json:"include,omitempty"`
	Limit      int        `json:"limit,omitempty"`
	MaxBytes   int        `json:"max_bytes,omitempty"`
}
type fastItem struct {
	EventID          string     `json:"event_id,omitempty"`
	WorkspaceID      string     `json:"workspace_id"`
	WorkspaceName    string     `json:"workspace_name"`
	Kind             string     `json:"kind"`
	ID               string     `json:"id"`
	Revision         string     `json:"revision"`
	ChangedAt        time.Time  `json:"changed_at"`
	Op               string     `json:"op"`
	Title            string     `json:"title,omitempty"`
	Preview          string     `json:"preview,omitempty"`
	ContentTruncated bool       `json:"content_truncated"`
	Identifier       string     `json:"identifier,omitempty"`
	Status           string     `json:"status,omitempty"`
	NewCommentCount  *int       `json:"new_comment_count,omitempty"`
	ChangedFields    []string   `json:"changed_fields,omitempty"`
	LastActiveAt     *time.Time `json:"last_active_at,omitempty"`
	UnreadCount      *int       `json:"unread_count,omitempty"`
	AgentID          string     `json:"agent_id,omitempty"`
	SourceType       string     `json:"source_type,omitempty"`
	SourceID         string     `json:"source_id,omitempty"`
	Phase            string     `json:"phase,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	ErrorClass       string     `json:"error_class,omitempty"`
	active           bool
}
type fastResponse struct {
	SchemaVersion      string     `json:"schema_version"`
	AsOf               time.Time  `json:"as_of"`
	CoverageFrom       time.Time  `json:"coverage_from"`
	Mode               string     `json:"mode"`
	ScopeRevision      string     `json:"scope_revision"`
	Changes            []fastItem `json:"changes"`
	ActiveRuns         []fastItem `json:"active_runs"`
	ActiveRunsComplete bool       `json:"active_runs_complete"`
	Complete           bool       `json:"complete"`
	NextCursor         *string    `json:"next_cursor"`
	Checkpoint         *string    `json:"checkpoint"`
	RetentionFloor     *time.Time `json:"retention_floor"`
}
type fastBatch struct {
	Delta         bool
	Current       []fastItem
	Lower         map[string]int64
	ID            string
	Mode          string
	CoverageFrom  time.Time
	Watermarks    map[string]int64
	Principal     string
	Request       fastRequest
	AsOf          time.Time
	ScopeRevision string
	Items         []fastItem
	ActiveCount   int
	Expires       time.Time
}
type fastQuota struct {
	Tokens float64
	At     time.Time
}
type fastStore struct {
	sync.Mutex
	Quotas map[string]fastQuota
}

// Stores belong to the handler, so independent test/server instances never
// share rate-limit buckets. The outer registry has one entry per server.
var fastStores sync.Map

func (h *Handler) fastStore() *fastStore {
	value, _ := fastStores.LoadOrStore(h, &fastStore{Quotas: map[string]fastQuota{}})
	return value.(*fastStore)
}
func fastError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
func fastClip(s string, chars, bytes int) (string, bool) {
	end, count := 0, 0
	for end < len(s) && count < chars {
		_, n := utf8.DecodeRuneInString(s[end:])
		if end+n > bytes {
			break
		}
		end += n
		count++
	}
	return s[:end], end < len(s)
}
func fastNormalize(req *fastRequest) error {
	if req.Limit == 0 {
		req.Limit = 100
	}
	if req.MaxBytes == 0 {
		req.MaxBytes = 50000
	}
	if req.Limit < 1 || req.Limit > 200 || req.MaxBytes < 1 || req.MaxBytes > 50000 {
		return fmt.Errorf("limit must be 1..200 and max_bytes 1..50000")
	}
	if req.Cursor != "" {
		if req.Since != nil || req.Scope != nil || len(req.Include) > 0 {
			return fmt.Errorf("cursor cannot be combined with since, scope or include")
		}
		return nil
	}
	if req.Scope == nil {
		req.Scope = &fastScope{Mode: "all_visible"}
	}
	if req.Scope.Mode != "all_visible" && req.Scope.Mode != "workspace_ids" {
		return fmt.Errorf("invalid scope mode")
	}
	if (req.Scope.Mode == "workspace_ids") != (len(req.Scope.WorkspaceIDs) > 0) {
		return fmt.Errorf("workspace_ids required only for workspace_ids scope")
	}
	if len(req.Scope.WorkspaceIDs) > 100 {
		return fmt.Errorf("too many workspaces")
	}
	for _, id := range req.Scope.WorkspaceIDs {
		if _, err := parseUUIDSafe(id); err != nil {
			return fmt.Errorf("invalid workspace id")
		}
	}
	if len(req.Include) == 0 {
		req.Include = []string{"chats", "issues", "run_changes", "active_runs"}
	}
	for _, s := range req.Include {
		if s != "chats" && s != "issues" && s != "run_changes" && s != "active_runs" {
			return fmt.Errorf("invalid include")
		}
	}
	if req.Since != nil && req.Since.After(time.Now()) {
		return fmt.Errorf("since cannot be in the future")
	}
	return nil
}
func fastIncludes(req fastRequest, s string) bool {
	for _, v := range req.Include {
		if v == s {
			return true
		}
	}
	return false
}

func (h *Handler) ReadFastAccess(w http.ResponseWriter, r *http.Request) {
	var req fastRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		fastError(w, 400, "invalid_request", "invalid request body")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		fastError(w, 400, "invalid_request", "expected one JSON object")
		return
	}
	h.readFastAccess(w, r, req)
}
func (h *Handler) ListFastAccessRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("active") != "true" {
		fastError(w, 400, "invalid_request", "active=true is required")
		return
	}
	req := fastRequest{Cursor: q.Get("cursor")}
	for key, dest := range map[string]*int{"limit": &req.Limit, "max_bytes": &req.MaxBytes} {
		if q.Has(key) {
			n, e := strconv.Atoi(q.Get(key))
			if e != nil || n == 0 {
				fastError(w, 400, "invalid_request", "invalid "+key)
				return
			}
			*dest = n
		}
	}
	if req.Cursor == "" {
		req.Include = []string{"active_runs"}
		req.Scope = &fastScope{Mode: "all_visible"}
		if q.Get("scope") != "" && q.Get("scope") != "all_visible" {
			fastError(w, 400, "invalid_request", "invalid scope")
			return
		}
		if ids := q.Get("workspace_ids"); ids != "" {
			req.Scope = &fastScope{Mode: "workspace_ids", WorkspaceIDs: strings.Split(ids, ",")}
		}
	} else if q.Has("scope") || q.Has("workspace_ids") {
		fastError(w, 400, "invalid_request", "cursor cannot be combined with scope")
		return
	}
	h.readFastAccess(w, r, req)
}
func (h *Handler) readFastAccess(w http.ResponseWriter, r *http.Request, req fastRequest) {
	if err := fastNormalize(&req); err != nil {
		fastError(w, 400, "invalid_request", err.Error())
		return
	}
	user, ok := requireUserID(w, r)
	if !ok {
		return
	}
	viewer, err := h.fastViewer(r, user)
	if err != nil {
		fastError(w, 403, "scope_denied", "cannot resolve visibility principal")
		return
	}
	if viewer == "" {
		fastError(w, 403, "scope_denied", "no visibility principal")
		return
	}
	principal := viewer + ":" + r.Header.Get("X-Actor-Source") + ":" + r.Header.Get("X-Task-ID") + ":" + ctxWorkspaceID(r.Context())
	store := h.fastStore()
	now := time.Now().UTC()
	store.Lock()
	for k, q := range store.Quotas {
		if now.Sub(q.At) > 5*time.Minute {
			delete(store.Quotas, k)
		}
	}
	quota, exists := store.Quotas[viewer]
	if !exists {
		quota = fastQuota{10, now}
	}
	quota.Tokens = min(10, quota.Tokens+now.Sub(quota.At).Seconds())
	quota.At = now
	if quota.Tokens < 1 {
		store.Quotas[viewer] = quota
		store.Unlock()
		w.Header().Set("Retry-After", "1")
		fastError(w, 429, "rate_limited", "retry after one second")
		return
	}
	quota.Tokens--
	store.Quotas[viewer] = quota
	store.Unlock()
	h.fastDurableRead(w, r, req, viewer, principal)
}

func fastPage(b *fastBatch, offset, limit, budget int, next, checkpoint string) (fastResponse, []byte, int, error) {
	resp := fastResponse{SchemaVersion: "1", AsOf: b.AsOf, CoverageFrom: b.AsOf, Mode: "timestamp_snapshot", ScopeRevision: b.ScopeRevision, Changes: []fastItem{}, ActiveRuns: []fastItem{}}
	if b.Mode != "" {
		resp.Mode = b.Mode
		resp.CoverageFrom = b.CoverageFrom
		floor := b.CoverageFrom
		resp.RetentionFloor = &floor
	}
	end := offset
	var payload []byte
	finish := func() {
		resp.Complete = end == len(b.Items)
		resp.ActiveRunsComplete = end >= b.ActiveCount
		resp.NextCursor = nil
		resp.Checkpoint = nil
		if resp.Complete {
			resp.Checkpoint = &checkpoint
		} else {
			resp.NextCursor = &next
		}
	}
	finish()
	payload, _ = json.Marshal(resp)
	for end < len(b.Items) && end-offset < limit {
		item := b.Items[end]
		if end < b.ActiveCount {
			resp.ActiveRuns = append(resp.ActiveRuns, item)
		} else {
			resp.Changes = append(resp.Changes, item)
		}
		end++
		finish()
		candidate, _ := json.Marshal(resp)
		if len(candidate) > budget {
			end--
			if end < b.ActiveCount {
				resp.ActiveRuns = resp.ActiveRuns[:len(resp.ActiveRuns)-1]
			} else {
				resp.Changes = resp.Changes[:len(resp.Changes)-1]
			}
			finish()
			break
		}
		payload = candidate
	}
	if end == offset && offset < len(b.Items) {
		return resp, nil, end, fmt.Errorf("max_bytes cannot fit the next item")
	}
	payload, _ = json.Marshal(resp)
	if len(payload) > budget {
		return resp, nil, end, fmt.Errorf("max_bytes cannot fit envelope")
	}
	return resp, payload, end, nil
}
func fastDigest(values []string) string {
	sort.Strings(values)
	sum := sha256.Sum256([]byte(strings.Join(values, "\n")))
	return hex.EncodeToString(sum[:])
}
