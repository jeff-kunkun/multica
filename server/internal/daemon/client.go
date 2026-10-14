package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// requestError is returned by postJSON/getJSON when the server responds with an error status.
type requestError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *requestError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

var errInvalidResponseBody = errors.New("invalid response body")

// isWorkspaceNotFoundError returns true if the error is a 404 with "workspace not found" body.
func isWorkspaceNotFoundError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	if reqErr.StatusCode != http.StatusNotFound {
		return false
	}
	return strings.Contains(strings.ToLower(reqErr.Body), "workspace not found")
}

// isTaskNotFoundError returns true if the error is a 404 with "task not found"
// body. The daemon uses this to detect that a task was deleted server-side
// (issue removed, agent reassigned, ...) while the local agent was still
// running, so it can interrupt the agent rather than letting it keep
// emitting tool calls against a dead task.
func isTaskNotFoundError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	if reqErr.StatusCode != http.StatusNotFound {
		return false
	}
	return strings.Contains(strings.ToLower(reqErr.Body), "task not found")
}

// isUnauthorizedError returns true if the error is a 401 from the server.
// Used by the token-renewal loop to surface a clear "re-login required"
// message instead of a generic transport-level retry.
func isUnauthorizedError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	return reqErr.StatusCode == http.StatusUnauthorized
}

// isRuntimeNotFoundError returns true if the error is a 404 with "runtime not
// found" body. The daemon uses this to detect that the runtime row was deleted
// server-side (UI Delete, 7-day offline GC) while the daemon was still
// heartbeating against the dead UUID, so it can prune the stale runtime from
// its local state and re-register instead of looping on the dead ID forever.
//
// Server-side, this body is paired with pgx.ErrNoRows specifically (other DB
// errors return 500), so a transient DB hiccup cannot make the daemon
// self-cleanup.
func isRuntimeNotFoundError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	if reqErr.StatusCode != http.StatusNotFound {
		return false
	}
	return strings.Contains(strings.ToLower(reqErr.Body), "runtime not found")
}

// Client handles HTTP communication with the Multica server daemon API.
type Client struct {
	baseURL string
	token   string
	client  *http.Client

	// Identity headers sent on every request as X-Client-*. Populated by
	// SetIdentity(); empty values are simply omitted.
	platform string
	version  string
	os       string

	workspaceMu                    sync.Mutex
	workspaceETag                  string
	workspaceCache                 []WorkspaceInfo
	workspaceCacheValid            bool
	legacyWorkspaceEndpointEnabled bool
	issueGCBatchMu                 sync.Mutex
	legacyIssueGCBatchEnabled      bool
}

// NewClient creates a new daemon API client.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:  baseURL,
		client:   &http.Client{Timeout: 30 * time.Second, Transport: cloneDefaultTransport()},
		platform: "daemon",
		os:       normalizeGOOS(runtime.GOOS),
	}
}

func cloneDefaultTransport() http.RoundTripper {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		return transport.Clone()
	}
	return http.DefaultTransport
}

// CloseIdleConnections drops pooled control-plane HTTP connections. The
// daemon calls this after repeated heartbeat transport failures so a stale
// keep-alive socket from a server restart cannot delay recovery indefinitely.
func (c *Client) CloseIdleConnections() {
	if c == nil || c.client == nil {
		return
	}
	c.client.CloseIdleConnections()
}

// normalizeGOOS maps Go's runtime.GOOS values to the protocol vocabulary
// used by X-Client-OS / client_os ("macos" / "windows" / "linux").
func normalizeGOOS(goos string) string {
	switch goos {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	default:
		return goos
	}
}

// SetVersion records the daemon's CLI version, sent as X-Client-Version.
// Called by Daemon.Run after config is loaded.
func (c *Client) SetVersion(v string) {
	c.version = v
}

// setIdentityHeaders attaches X-Client-Platform/Version/OS to req when set.
func (c *Client) setIdentityHeaders(req *http.Request) {
	if c.platform != "" {
		req.Header.Set("X-Client-Platform", c.platform)
	}
	if c.version != "" {
		req.Header.Set("X-Client-Version", c.version)
	}
	if c.os != "" {
		req.Header.Set("X-Client-OS", c.os)
	}
	req.Header.Set("X-Client-Capabilities", daemonHTTPClientCapabilities())
}

// daemonClientCapabilities is the X-Client-Capabilities value the daemon
// advertises on the WS handshake. A claim built over WS gets the common
// capability gating plus WS-only scheduling metadata. rpc-v1 advertises WS
// request/response support (MUL-4257).
func daemonClientCapabilities() string {
	return strings.Join(append(daemonCommonCapabilities(),
		protocol.DaemonCapabilityClaimPollHintsV1,
	), ",")
}

// daemonHTTPClientCapabilities omits claim-poll-hints-v1 because HTTP fallback
// responses cannot drive the healthy-WS scheduler. Advertising it there would
// make the server run the deferred-task hint query only for the daemon to ignore
// the result.
func daemonHTTPClientCapabilities() string {
	return strings.Join(daemonCommonCapabilities(), ",")
}

func daemonCommonCapabilities() []string {
	return []string{
		protocol.DaemonCapabilityCoalescedCommentsV1,
		protocol.DaemonCapabilityExecutionManifestV1,
		protocol.DaemonCapabilityRPCV1,
		protocol.DaemonCapabilityCheckoutKeepsWorkV1,
	}
}

// SetToken sets the auth token for authenticated requests.
func (c *Client) SetToken(token string) {
	c.token = token
}

// Token returns the current auth token.
func (c *Client) Token() string {
	return c.token
}

func (c *Client) ClaimTask(ctx context.Context, runtimeID string) (*Task, error) {
	var resp struct {
		Task *Task `json:"task"`
	}
	if err := c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/tasks/claim", runtimeID), map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp.Task, nil
}

// batchClaimRequestTimeout is the short, request-scoped deadline for the
// machine-level batch claim (MUL-4257). Unlike the per-runtime claim — which
// gets the full 30s control-plane timeout because a stall there only blocks
// that one runtime's goroutine — the batch call covers every runtime the
// daemon hosts in a single request, so a slow claim would delay ALL of them
// (the head-of-line coupling the per-runtime pollers were split to avoid,
// MUL-1744). Bounding the batch to a few seconds caps that worst-case
// starvation; a claim that commits server-side after the client gives up is
// recovered by ReclaimStaleDispatchedTasksForRuntimes on the next poll. Kept
// comfortably above p99 claim latency so recovery stays the exception.
const batchClaimRequestTimeout = 5 * time.Second

// claimTasksResult carries optional scheduling metadata understood only by
// daemons advertising claim-poll-hints-v1. A zero-value result is deliberately
// conservative: it makes the poller retain PollInterval, which protects new
// daemons talking to old servers and claims whose WS outcome was uncertain.
type claimTasksResult struct {
	Tasks                       []*Task `json:"tasks"`
	ClaimPollHintSupported      bool    `json:"claim_poll_hint_supported,omitempty"`
	NextDeferredTaskAfterMillis int64   `json:"next_deferred_task_after_ms,omitempty"`
	ClaimedOverWS               bool    `json:"-"`
}

// ClaimTasks is the machine-level (MUL-4257) batch counterpart of ClaimTask:
// it asks the server, in a single request, to claim up to maxTasks tasks across
// every runtime the daemon hosts. daemonID scopes the request to this machine —
// the server rejects any runtime_id whose runtime.daemon_id doesn't match, so a
// stale/crossed runtime set can't claim another machine's tasks. Each returned
// Task carries its own RuntimeID so the daemon routes it to the matching
// runtime locally. The request runs under a short, request-scoped deadline
// (batchClaimRequestTimeout) rather than the shared 30s control-plane timeout so
// one slow claim cannot stall the whole batch; the deadline propagates to the
// server and cancels the in-flight query there too.
func (c *Client) ClaimTasks(ctx context.Context, daemonID string, runtimeIDs []string, maxTasks int) ([]*Task, error) {
	result, err := c.claimTasksWithHints(ctx, daemonID, runtimeIDs, maxTasks)
	return result.Tasks, err
}

func (c *Client) claimTasksWithHints(ctx context.Context, daemonID string, runtimeIDs []string, maxTasks int) (claimTasksResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, batchClaimRequestTimeout)
	defer cancel()
	var resp claimTasksResult
	if err := c.postJSON(reqCtx, "/api/daemon/tasks/claim", map[string]any{
		"daemon_id":   daemonID,
		"runtime_ids": runtimeIDs,
		"max_tasks":   maxTasks,
	}, &resp); err != nil {
		return claimTasksResult{}, err
	}
	return resp, nil
}

// isBatchClaimUnsupported reports whether err is a 404 from the batch claim
// endpoint — i.e. the server predates the /api/daemon/tasks/claim route and the
// daemon must fall back to the legacy per-runtime claim (MUL-4257). The batch
// handler itself never returns 404, so a 404 here means the route is
// unregistered on an un-upgraded server.
func isBatchClaimUnsupported(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	return reqErr.StatusCode == http.StatusNotFound
}

// claimTasksLegacy is the pre-batch compatibility fallback (MUL-4257): claim per
// runtime via the legacy POST /api/daemon/runtimes/{id}/tasks/claim so a new
// daemon still works against a server that has no batch route. Returns up to
// maxTasks tasks. A per-runtime error is only propagated when nothing has been
// claimed yet; otherwise the partial result is returned and the next poll
// retries the rest.
func (c *Client) claimTasksLegacy(ctx context.Context, runtimeIDs []string, maxTasks int) ([]*Task, error) {
	if maxTasks <= 0 {
		return nil, nil
	}
	out := make([]*Task, 0, maxTasks)
	for _, rid := range runtimeIDs {
		if len(out) >= maxTasks {
			break
		}
		task, err := c.ClaimTask(ctx, rid)
		if err != nil {
			if len(out) == 0 {
				return nil, err
			}
			return out, nil
		}
		if task != nil {
			out = append(out, task)
		}
	}
	return out, nil
}

func (c *Client) ExtendTaskPrepareLease(ctx context.Context, runtimeID, taskID string) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/tasks/%s/prepare-lease", runtimeID, taskID), map[string]any{}, nil)
}

type TaskSupplement struct {
	CommentID  string `json:"comment_id"`
	AuthorName string `json:"author_name"`
	Content    string `json:"content"`
}

func (c *Client) ClaimTaskSupplement(ctx context.Context, taskID string) (*TaskSupplement, error) {
	var supplement TaskSupplement
	if err := c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/supplements/claim", taskID), map[string]any{}, &supplement); err != nil {
		return nil, err
	}
	if supplement.CommentID == "" {
		return nil, nil
	}
	return &supplement, nil
}

func (c *Client) AckTaskSupplement(ctx context.Context, taskID, commentID string, delivered bool, errText string) error {
	return c.postJSONWithRetry(ctx,
		fmt.Sprintf("/api/daemon/tasks/%s/supplements/%s/ack", taskID, commentID),
		map[string]any{"delivered": delivered, "error": errText}, nil,
		[]time.Duration{0, 100 * time.Millisecond, 300 * time.Millisecond})
}

// startTaskRetrySchedule allows two short reconnects without the terminal
// callbacks' 124s backoff. The whole start has a 30s budget (not 3 x the HTTP
// client's 30s timeout). Preparation keeps renewing its 45s lease every 15s
// throughout requests and backoff; its own deadline can end this sooner.
// runTask calls this once. Task-level retries are separate executions, with
// fresh claims, and cannot reuse this acknowledgement.
var startTaskRetrySchedule = []time.Duration{500 * time.Millisecond, 2 * time.Second}

const (
	startTaskTimeout = 30 * time.Second
	// Start responses can include an issue snapshot alongside the capability.
	maxStartTaskResponseBytes = 1 << 20
)

var errStartClaimRejected = errors.New("task start claim rejected")

func (c *Client) StartTask(ctx context.Context, task Task, capabilities ...string) (bool, error) {
	var negotiated bool
	var decodeResponse responseDecoder = func(r io.Reader) error {
		data, err := io.ReadAll(io.LimitReader(r, maxStartTaskResponseBytes+1))
		if err != nil {
			return err
		}
		if len(data) > maxStartTaskResponseBytes {
			return fmt.Errorf("%w: task start exceeds %d bytes", errInvalidResponseBody, maxStartTaskResponseBytes)
		}
		var response struct {
			SupplementCapability string `json:"supplement_capability"`
		}
		// Empty acknowledgements start the task without negotiating supplements.
		// Invalid JSON fails without retrying; transport read failures can retry.
		if len(data) != 0 {
			if err := json.Unmarshal(data, &response); err != nil {
				return fmt.Errorf("%w: task start: %w", errInvalidResponseBody, err)
			}
		}
		negotiated = response.SupplementCapability == protocol.DaemonCapabilityTaskSupplementV1
		return nil
	}
	path := fmt.Sprintf("/api/daemon/tasks/%s/start", task.ID)
	if !task.StartClaimSupported {
		// Old servers have no safe replay contract. Preserve one attempt.
		err := c.postJSON(ctx, path, map[string]any{"capabilities": capabilities}, decodeResponse)
		return err == nil && negotiated, err
	}
	if task.RuntimeID == "" || task.DispatchedAt == "" {
		return false, fmt.Errorf("start task: claim is missing runtime_id or dispatched_at")
	}
	ctx, cancel := context.WithTimeout(ctx, startTaskTimeout)
	defer cancel()
	err := c.postJSONWithRetry(ctx, path, map[string]any{
		"runtime_id":    task.RuntimeID,
		"capabilities":  capabilities,
		"dispatched_at": task.DispatchedAt,
	}, decodeResponse, startTaskRetrySchedule)
	var reqErr *requestError
	if errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusConflict {
		return false, fmt.Errorf("%w: %w", errStartClaimRejected, err)
	}
	return err == nil && negotiated, err
}

// AckTaskCancelled tells the server this daemon observed the task's
// cancellation and has finished flushing the transcript (runner.run only
// returns after executeAndDrain's drain wait), so the server can settle its
// deferred chat finalization now instead of waiting out the sweeper grace
// period (#5219). Idempotent server-side.
func (c *Client) AckTaskCancelled(ctx context.Context, taskID string) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/cancel-ack", taskID), map[string]any{}, nil)
}

func (c *Client) ReportProgress(ctx context.Context, taskID, summary string, step, total int) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/progress", taskID), map[string]any{
		"summary": summary,
		"step":    step,
		"total":   total,
	}, nil)
}

// TaskMessageData represents a single agent execution message for batch reporting.
type TaskMessageData struct {
	// CallID is an opaque tool-call identity scoped to one backend execution.
	CallID  string         `json:"call_id,omitempty"`
	Seq     int            `json:"seq"`
	Type    string         `json:"type"`
	Tool    string         `json:"tool,omitempty"`
	Content string         `json:"content,omitempty"`
	Input   map[string]any `json:"input,omitempty"`
	Output  string         `json:"output,omitempty"`
	// CreatedAt is when the daemon observed the event, before the 500ms report
	// batch. Without it, every row in one batch gets the same database time.
	CreatedAt time.Time `json:"created_at"`
	// OutputTruncated reports whether Output dropped bytes to fit the preview
	// budget. Tri-state on purpose: nil means this daemon did not measure it,
	// which an older installed daemon talking to a newer server cannot say any
	// other way, and which the server must not record as "complete".
	OutputTruncated *bool `json:"output_truncated,omitempty"`
}

func (c *Client) ReportTaskMessages(ctx context.Context, taskID string, messages []TaskMessageData) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/messages", taskID), map[string]any{
		"messages": messages,
	}, nil)
}

func (c *Client) CompleteTask(ctx context.Context, taskID, output, sessionID, workDir string, sessionRolloutMissing bool, retiredSessionID string) error {
	return c.completeTaskWithRetrySchedule(ctx, taskID, output, sessionID, workDir, sessionRolloutMissing, retiredSessionID, defaultTerminalRetrySchedule)
}

func (c *Client) completeTaskWithRetrySchedule(ctx context.Context, taskID, output, sessionID, workDir string, sessionRolloutMissing bool, retiredSessionID string, schedule []time.Duration) error {
	body := map[string]any{"output": output}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if workDir != "" {
		body["work_dir"] = workDir
	}
	if sessionRolloutMissing {
		body["session_rollout_missing"] = true
	}
	if retiredSessionID != "" {
		body["retired_session_id"] = retiredSessionID
	}
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/daemon/tasks/%s/complete", taskID), body, nil, schedule)
}

func (c *Client) ReportTaskUsage(ctx context.Context, taskID string, usage []TaskUsageEntry) error {
	if len(usage) == 0 {
		return nil
	}
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/usage", taskID), map[string]any{
		"usage": usage,
	}, nil)
}

func (c *Client) FailTask(ctx context.Context, taskID, errMsg, sessionID, workDir, failureReason string, sessionRolloutMissing bool, retiredSessionID string) error {
	return c.failTaskWithRetrySchedule(ctx, taskID, errMsg, sessionID, workDir, failureReason, sessionRolloutMissing, retiredSessionID, defaultTerminalRetrySchedule)
}

func (c *Client) failTaskWithRetrySchedule(ctx context.Context, taskID, errMsg, sessionID, workDir, failureReason string, sessionRolloutMissing bool, retiredSessionID string, schedule []time.Duration) error {
	body := map[string]any{"error": errMsg}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if workDir != "" {
		body["work_dir"] = workDir
	}
	if failureReason != "" {
		body["failure_reason"] = failureReason
	}
	if sessionRolloutMissing {
		body["session_rollout_missing"] = true
	}
	if retiredSessionID != "" {
		body["retired_session_id"] = retiredSessionID
	}
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/daemon/tasks/%s/fail", taskID), body, nil, schedule)
}

// PinTaskSession persists the agent's session_id and work_dir on the task
// row mid-flight so a daemon crash doesn't lose the resume pointer.
func (c *Client) PinTaskSession(ctx context.Context, taskID, sessionID, workDir string) error {
	if sessionID == "" && workDir == "" {
		return nil
	}
	body := map[string]any{}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if workDir != "" {
		body["work_dir"] = workDir
	}
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/session", taskID), body, nil)
}

// RecoverOrphans tells the server to fail any dispatched/running tasks the
// previous daemon process for this runtime left behind. The server will
// auto-retry eligible tasks.
func (c *Client) RecoverOrphans(ctx context.Context, runtimeID string) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/recover-orphans", runtimeID), map[string]any{}, nil)
}

// GetTaskStatus returns the current status of a task. Used by the daemon to
// detect terminal/interruption signals (cancelled, failed, completed, or a
// 404 task-not-found) while a task is executing.
func (c *Client) GetTaskStatus(ctx context.Context, taskID string) (string, error) {
	var resp struct {
		Status string `json:"status"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/status", taskID), &resp); err != nil {
		return "", err
	}
	return resp.Status, nil
}

// HeartbeatResponse, PendingUpdate, etc. alias the wire types so HTTP and WS
// heartbeat paths share a single type and a single decoder shape. Aliases
// (rather than wrappers) keep call sites unchanged.
type (
	HeartbeatResponse = protocol.DaemonHeartbeatAckPayload
	PendingUpdate     = protocol.DaemonHeartbeatPendingUpdate
)

func (c *Client) SendHeartbeat(ctx context.Context, runtimeID string) (*HeartbeatResponse, error) {
	var resp HeartbeatResponse
	if err := c.postJSON(ctx, "/api/daemon/heartbeat", map[string]any{
		"runtime_id":            runtimeID,
		"supports_batch_import": true,
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ReportUpdateResult sends the CLI update result back to the server.
func (c *Client) ReportUpdateResult(ctx context.Context, runtimeID, updateID string, result map[string]any) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/update/%s/result", runtimeID, updateID), result, nil)
}

// WorkspaceInfo holds minimal workspace metadata returned by the API.
type WorkspaceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RenewTokenResponse mirrors handler.RenewPATResponse — kept loose (string +
// bool) because the daemon never parses the timestamp itself; it just logs it
// for operator visibility.
type RenewTokenResponse struct {
	ExpiresAt string `json:"expires_at"`
	Renewed   bool   `json:"renewed"`
}

// RenewToken asks the server to extend the daemon's current PAT in place when
// it's within the server-side renewal window. The server is authoritative on
// the threshold — the daemon doesn't know the token's expires_at locally —
// so this is safe to call on any cadence; the only thing extra calls cost is
// one round trip and one cheap SELECT.
func (c *Client) RenewToken(ctx context.Context) (*RenewTokenResponse, error) {
	var resp RenewTokenResponse
	if err := c.postJSON(ctx, "/api/tokens/current/renew", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListWorkspaces fetches the minimal workspace membership set used by the
// daemon. New servers expose a daemon-specific endpoint with ETag support;
// when an installed daemon talks to an older server, the first 404 switches
// this process to the legacy full-workspace endpoint for compatibility.
func (c *Client) ListWorkspaces(ctx context.Context) ([]WorkspaceInfo, error) {
	c.workspaceMu.Lock()
	defer c.workspaceMu.Unlock()

	if c.legacyWorkspaceEndpointEnabled {
		return c.listLegacyWorkspaces(ctx)
	}

	const path = "/api/daemon/workspaces"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	c.setIdentityHeaders(req)
	if c.workspaceETag != "" {
		req.Header.Set("If-None-Match", c.workspaceETag)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		c.legacyWorkspaceEndpointEnabled = true
		c.workspaceETag = ""
		c.workspaceCache = nil
		c.workspaceCacheValid = false
		return c.listLegacyWorkspaces(ctx)
	}
	if resp.StatusCode == http.StatusNotModified {
		if !c.workspaceCacheValid {
			return nil, fmt.Errorf("GET %s returned 304 without a cached workspace set", path)
		}
		return append([]WorkspaceInfo(nil), c.workspaceCache...), nil
	}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &requestError{Method: http.MethodGet, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}

	var workspaces []WorkspaceInfo
	if err := json.NewDecoder(resp.Body).Decode(&workspaces); err != nil {
		return nil, err
	}
	c.workspaceETag = resp.Header.Get("ETag")
	c.workspaceCache = append([]WorkspaceInfo(nil), workspaces...)
	c.workspaceCacheValid = true
	return append([]WorkspaceInfo(nil), workspaces...), nil
}

func (c *Client) listLegacyWorkspaces(ctx context.Context) ([]WorkspaceInfo, error) {
	var workspaces []WorkspaceInfo
	if err := c.getJSON(ctx, "/api/workspaces", &workspaces); err != nil {
		return nil, err
	}
	return workspaces, nil
}

func (c *Client) usesLegacyWorkspaceEndpoint() bool {
	c.workspaceMu.Lock()
	defer c.workspaceMu.Unlock()
	return c.legacyWorkspaceEndpointEnabled
}

// IssueGCStatus holds the minimal issue info returned by the GC check endpoint.
//
// Category is the issue's lifecycle (unstarted/started/done/closed) and is what
// GC decides on. Status is the legacy seven-value enum, still populated by the
// server for installed daemons and used here only when Category is absent —
// a server predating MUL-7364 — or unrecognized. See issueGCLifecycle in gc.go.
type IssueGCStatus struct {
	Status    string    `json:"status"`
	Category  string    `json:"category,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IssueGCCheckResult is one explicit issue result from the workspace batch
// endpoint. Found=false deliberately covers both a deleted issue and an ID
// outside the requested workspace, preserving the server's anti-enumeration
// contract. Err is only populated by the legacy per-issue fallback.
type IssueGCCheckResult struct {
	ID        string    `json:"id"`
	Found     bool      `json:"found"`
	Status    string    `json:"status,omitempty"`
	Category  string    `json:"category,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Err       error     `json:"-"`
}

type issueGCBatchResponse struct {
	Issues []IssueGCCheckResult `json:"issues"`
}

// isIssueGCBatchUnsupported distinguishes chi's unmatched-route response on an
// older server from the JSON 404 returned by a current server when the caller
// cannot access the requested workspace. Only the former is a compatibility
// signal; falling back on an authorization 404 would turn one denied request
// into hundreds of legacy probes.
func isIssueGCBatchUnsupported(err error) bool {
	var reqErr *requestError
	return errors.As(err, &reqErr) &&
		reqErr.StatusCode == http.StatusNotFound &&
		strings.TrimSpace(reqErr.Body) == "404 page not found"
}

// GetIssueGCChecks reconciles a workspace's issue IDs in one request. When a
// new daemon reaches an older server that does not have the batch route, the
// first 404 permanently switches this client process to the legacy per-issue
// endpoint. Other batch failures are returned without fan-out so a transient
// server problem cannot amplify request volume.
func (c *Client) GetIssueGCChecks(ctx context.Context, workspaceID string, issueIDs []string) (map[string]IssueGCCheckResult, error) {
	c.issueGCBatchMu.Lock()
	defer c.issueGCBatchMu.Unlock()

	if c.legacyIssueGCBatchEnabled {
		return c.getLegacyIssueGCChecks(ctx, issueIDs), nil
	}

	path := fmt.Sprintf("/api/daemon/workspaces/%s/issues/gc-check", workspaceID)
	var resp issueGCBatchResponse
	err := c.postJSON(ctx, path, map[string]any{"issue_ids": issueIDs}, &resp)
	if err != nil {
		if !isIssueGCBatchUnsupported(err) {
			return nil, err
		}
		c.legacyIssueGCBatchEnabled = true
		return c.getLegacyIssueGCChecks(ctx, issueIDs), nil
	}

	results := make(map[string]IssueGCCheckResult, len(resp.Issues))
	for _, result := range resp.Issues {
		results[result.ID] = result
	}
	return results, nil
}

func (c *Client) getLegacyIssueGCChecks(ctx context.Context, issueIDs []string) map[string]IssueGCCheckResult {
	results := make(map[string]IssueGCCheckResult, len(issueIDs))
	for _, issueID := range issueIDs {
		status, err := c.GetIssueGCCheck(ctx, issueID)
		if err != nil {
			var reqErr *requestError
			if errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusNotFound {
				results[issueID] = IssueGCCheckResult{ID: issueID, Found: false}
			} else {
				results[issueID] = IssueGCCheckResult{ID: issueID, Err: err}
			}
			continue
		}
		results[issueID] = IssueGCCheckResult{
			ID:        issueID,
			Found:     true,
			Status:    status.Status,
			Category:  status.Category,
			UpdatedAt: status.UpdatedAt,
		}
	}
	return results
}

// GetIssueGCCheck returns the status and updated_at of an issue for GC decisions.
func (c *Client) GetIssueGCCheck(ctx context.Context, issueID string) (*IssueGCStatus, error) {
	var resp IssueGCStatus
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/issues/%s/gc-check", issueID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ChatSessionGCStatus mirrors IssueGCStatus for chat sessions.
type ChatSessionGCStatus struct {
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GetChatSessionGCCheck returns the status of a chat session for GC decisions.
// A 404 from this endpoint indicates the session row was hard-deleted (the
// user explicitly removed it), which the caller treats as an immediate-clean
// signal.
func (c *Client) GetChatSessionGCCheck(ctx context.Context, sessionID string) (*ChatSessionGCStatus, error) {
	var resp ChatSessionGCStatus
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/chat-sessions/%s/gc-check", sessionID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TaskGCStatus carries the agent_task_queue status for task-keyed GC (the
// stable-root index prune), which keys directly on the task itself.
type TaskGCStatus struct {
	Status      string    `json:"status"`
	CompletedAt time.Time `json:"completed_at"`
}

// GetTaskGCCheck returns the status of an agent task for GC decisions.
func (c *Client) GetTaskGCCheck(ctx context.Context, taskID string) (*TaskGCStatus, error) {
	var resp TaskGCStatus
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/gc-check", taskID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RuntimeOfflineCodeNotExecutable marks a runtime taken offline because the OS
// refuses to execute its agent CLI. It is the one deregistration cause that no
// amount of waiting fixes, which is what the server needs to know: work for an
// offline machine may queue until the machine returns, but work for this one
// must be refused with an explanation (MUL-6164).
const RuntimeOfflineCodeNotExecutable = "not_executable"

// RuntimeOfflineCodeDshProfile marks a runtime taken offline because the DSH
// runtime profile it depends on is not installed. Like not_executable it is not
// something waiting fixes on its own — a human installs a bundle, or configures
// the daemon to — so work for it is refused with an explanation rather than
// queued forever. The exception is an install the daemon is running right now,
// which Installing states explicitly.
//
// Only an ABSENT profile reaches this code. A profile that is present but
// answers with a protocol this daemon does not drive never takes a live runtime
// offline at all: the daemon can be the stale side of that skew, so it reports
// the incompatibility and leaves the runtime alone.
const RuntimeOfflineCodeDshProfile = "dsh_profile"

// RuntimeOfflineReason is why a runtime went offline, in the form clients can
// act on: a stable code they switch on and localize, and the command that
// repairs the install. Prose stays in Detail for logs — never as the thing a
// client parses.
type RuntimeOfflineReason struct {
	Code   string                  `json:"code"`
	Detail string                  `json:"detail,omitempty"`
	Repair *agent.ExecFormatRepair `json:"repair,omitempty"`
	// Installing reports that the daemon has an automatic install in flight for
	// this runtime. It is the difference between "a human has to act" and "this
	// comes back by itself", which the server cannot infer from the code alone:
	// without it, a successful install that is still running would look exactly
	// like a machine waiting on an operator who was never going to be told.
	Installing bool `json:"installing,omitempty"`
}

// Deregister takes runtimes offline. reasons is optional and keyed by runtime
// id: a daemon shutting down has nothing to explain, while one that condemned a
// broken CLI does.
func (c *Client) Deregister(ctx context.Context, runtimeIDs []string, reasons map[string]RuntimeOfflineReason) error {
	body := map[string]any{"runtime_ids": runtimeIDs}
	if len(reasons) > 0 {
		body["offline_reasons"] = reasons
	}
	return c.postJSON(ctx, "/api/daemon/deregister", body, nil)
}

// ReportMagpieRoute stores a runtime's magpie route in its metadata; nil
// removes it.
func (c *Client) ReportMagpieRoute(ctx context.Context, runtimeID string, route *MagpieRoute) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/magpie", runtimeID), map[string]any{"magpie": route}, nil)
}

// RegisterResponse holds the server's response to a daemon registration.
type RegisterResponse struct {
	Runtimes     []Runtime       `json:"runtimes"`
	Repos        []RepoData      `json:"repos"`
	ReposVersion string          `json:"repos_version"`
	Settings     json.RawMessage `json:"settings,omitempty"`
}

func (c *Client) Register(ctx context.Context, req map[string]any) (*RegisterResponse, error) {
	var resp RegisterResponse
	if err := c.postJSON(ctx, "/api/daemon/register", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type WorkspaceReposResponse struct {
	WorkspaceID  string          `json:"workspace_id"`
	Repos        []RepoData      `json:"repos"`
	ReposVersion string          `json:"repos_version"`
	Settings     json.RawMessage `json:"settings,omitempty"`
}

func (c *Client) GetWorkspaceRepos(ctx context.Context, workspaceID string) (*WorkspaceReposResponse, error) {
	var resp WorkspaceReposResponse
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/workspaces/%s/repos", workspaceID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RuntimeProfile mirrors the server's workspace custom runtime profile
// (MUL-3284). runtime_type selects the compatibility target, while
// protocol_family identifies its execution backend. command_name is the executable
// the daemon resolves on PATH and launches. fixed_args are launch arguments
// every agent on this runtime inherits.
type RuntimeProfile struct {
	ID             string   `json:"id"`
	WorkspaceID    string   `json:"workspace_id"`
	DisplayName    string   `json:"display_name"`
	ProtocolFamily string   `json:"protocol_family"`
	RuntimeType    string   `json:"runtime_type"`
	CommandName    string   `json:"command_name"`
	Description    *string  `json:"description"`
	FixedArgs      []string `json:"fixed_args"`
	Visibility     string   `json:"visibility"`
	Enabled        bool     `json:"enabled"`
}

// RuntimeProfilesResponse is the body of
// GET /api/daemon/workspaces/{workspaceID}/runtime-profiles. The server only
// returns enabled profiles for the workspace.
type RuntimeProfilesResponse struct {
	WorkspaceID     string           `json:"workspace_id"`
	RuntimeProfiles []RuntimeProfile `json:"runtime_profiles"`
}

// GetRuntimeProfiles fetches the workspace's enabled custom runtime profiles.
// Mirrors GetWorkspaceRepos. Callers must treat this as best-effort: an older
// server with no profiles route returns 404, which the daemon swallows and
// continues with built-in runtimes only.
func (c *Client) GetRuntimeProfiles(ctx context.Context, workspaceID string) (*RuntimeProfilesResponse, error) {
	var resp RuntimeProfilesResponse
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/workspaces/%s/runtime-profiles", workspaceID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// defaultTerminalRetrySchedule is the backoff used by postJSONWithRetry for
// terminal task callbacks (CompleteTask / FailTask). N entries → N+1 attempts
// in the worst case (one immediate + N retries). Five backoffs totalling
// 124s is wide enough to ride out the short upstream blips we've seen
// (MUL-2780) without leaving the task stuck if the outage outlives the
// window.
var defaultTerminalRetrySchedule = []time.Duration{
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
	64 * time.Second,
}

// retrySleep is the sleep used between retry attempts. Pulled into a package
// variable so tests can swap in an instant sleep without rewriting the
// caller's schedule.
var retrySleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isTransientError reports whether err looks like a hiccup that's likely to
// resolve on retry: connection / TLS / I/O errors at the transport layer
// (including client timeouts surfacing as context.DeadlineExceeded inside
// http.Client.Do), 5xx server responses, and 408/429 rate-limit-style 4xx
// codes. Response validation errors marked with errInvalidResponseBody and
// other 4xx codes are permanent.
//
// The caller is responsible for separately bailing on parent-context
// cancellation; this predicate cannot distinguish "the daemon is shutting
// down" from "the HTTP client timed out a single attempt" because both
// reach here as context errors wrapped by net/http.
func isTransientError(err error) bool {
	if err == nil || errors.Is(err, errInvalidResponseBody) {
		return false
	}
	var reqErr *requestError
	if errors.As(err, &reqErr) {
		if reqErr.StatusCode >= 500 {
			return true
		}
		if reqErr.StatusCode == http.StatusRequestTimeout || reqErr.StatusCode == http.StatusTooManyRequests {
			return true
		}
		return false
	}
	return true
}

// postJSONWithRetry posts a JSON body with bounded exponential backoff,
// intended for "must reach the server" terminal callbacks (CompleteTask /
// FailTask). It retries transient errors per isTransientError and stops
// immediately on permanent 4xx responses so we don't burn the schedule on
// requests the server has already rejected.
//
// schedule controls the sleeps between attempts. With N entries the helper
// performs N+1 attempts in the worst case (one initial + N retries). The
// returned error is the last response from the server, so callers can still
// inspect it with isTransientError to decide whether to fall back to a
// different terminal call (e.g. complete → fail on permanent error only).
//
// The server-side CompleteTask / FailTask treat "already terminal" as an
// idempotent success (see service/task.go), so a duplicate replay from a
// retry is safe even if the server's prior response was lost in transit.
func (c *Client) postJSONWithRetry(ctx context.Context, path string, reqBody any, respBody any, schedule []time.Duration) error {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return lastErr
			}
			return err
		}
		err := c.postJSON(ctx, path, reqBody, respBody)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientError(err) {
			return err
		}
		if attempt >= len(schedule) {
			return err
		}
		if sleepErr := retrySleep(ctx, schedule[attempt]); sleepErr != nil {
			return err
		}
	}
}

// responseDecoder customizes successful-response decoding inside each attempt.
type responseDecoder func(io.Reader) error

// postJSON performs one POST on the control-plane client. respBody can be a
// JSON destination or a responseDecoder that decodes a successful response
// inside each attempt, before retry decisions are made.
func (c *Client) postJSON(ctx context.Context, path string, reqBody any, respBody any) error {
	var body io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	c.setIdentityHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &requestError{Method: http.MethodPost, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	respReader := io.Reader(resp.Body)
	if respBody == nil {
		io.Copy(io.Discard, respReader)
		return nil
	}
	if decode, ok := respBody.(responseDecoder); ok {
		return decode(respReader)
	}
	return json.NewDecoder(respReader).Decode(respBody)
}

func (c *Client) getJSON(ctx context.Context, path string, respBody any) error {
	return c.getJSONWithToken(ctx, path, c.token, respBody)
}

// getJSONWithToken performs one GET with an explicit credential.
func (c *Client) getJSONWithToken(ctx context.Context, path, token string, respBody any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	c.setIdentityHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &requestError{Method: http.MethodGet, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if respBody == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(respBody)
}
