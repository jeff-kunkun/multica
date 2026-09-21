package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logexport"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Task log export. One builder (internal/logexport) sits behind three
// callers — the Web/Desktop dialog, the report action and the CLI — so they
// cannot produce different artifacts.

const (
	// logExportAttachmentMaxBytes caps a bundle that travels as a comment
	// attachment. A self-hosted instance behind a slow uplink and a proxy
	// timeout cannot move much more than this in one request, so the builder
	// trims the oldest entries to fit instead of producing an attachment
	// nobody can download.
	logExportAttachmentMaxBytes = 4 << 20
	// logExportRepoMaxBytes caps a bundle committed to the log repository.
	logExportRepoMaxBytes = 25 << 20
)

// LogExportPreviewResponse is the JSON form of an export: what the dialog
// renders before anything is downloaded.
type LogExportPreviewResponse struct {
	Empty             bool            `json:"empty"`
	Filename          string          `json:"filename"`
	SizeBytes         int             `json:"size_bytes"`
	Summary           string          `json:"summary"`
	Meta              *logexport.Meta `json:"meta"`
	LogRepoConfigured bool            `json:"log_repo_configured"`
}

// LogExportReportResponse says where a report landed.
type LogExportReportResponse struct {
	CommentID       string `json:"comment_id"`
	IssueID         string `json:"issue_id"`
	IssueIdentifier string `json:"issue_identifier"`
	// Delivery is "git" when the bundle was committed to the log repository
	// and "attachment" when it rides on the comment.
	Delivery string `json:"delivery"`
	Link     string `json:"link"`
	// FallbackReason is set when a repository was configured but the push
	// failed and the report fell back to an attachment.
	FallbackReason string `json:"fallback_reason"`
	Filename       string `json:"filename"`
	SizeBytes      int    `json:"size_bytes"`
	Mentioned      string `json:"mentioned"`
}

type logExportRequest struct {
	Scope        string `json:"scope"`
	Hours        int    `json:"hours"`
	AllowPartial bool   `json:"allow_partial"`
}

// logExportContext is what both endpoints resolve before building.
type logExportContext struct {
	task      db.AgentTaskQueue
	workspace db.Workspace
	issue     *db.Issue
	input     logexport.Input
}

// loadLogExportTask resolves the anchor run and proves it belongs to the
// caller's workspace. A run in another workspace is a 404, indistinguishable
// from one that does not exist.
func (h *Handler) loadLogExportTask(w http.ResponseWriter, r *http.Request) (db.AgentTaskQueue, db.Workspace, bool) {
	taskUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "taskId"), "task_id")
	if !ok {
		return db.AgentTaskQueue{}, db.Workspace{}, false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "task not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load task")
		}
		return db.AgentTaskQueue{}, db.Workspace{}, false
	}
	wsID, err := h.TaskService.ResolveTaskWorkspaceIDChecked(r.Context(), task)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load task")
		return db.AgentTaskQueue{}, db.Workspace{}, false
	}
	if wsID == "" || wsID != middleware.WorkspaceIDFromContext(r.Context()) {
		writeError(w, http.StatusNotFound, "task not found")
		return db.AgentTaskQueue{}, db.Workspace{}, false
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), parseUUID(wsID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workspace")
		return db.AgentTaskQueue{}, db.Workspace{}, false
	}
	return task, ws, true
}

// collectLogExport loads every row the builder needs. Runs whose messages
// cannot be read become warnings; the caller decides whether a partial bundle
// is acceptable.
func (h *Handler) collectLogExport(ctx context.Context, task db.AgentTaskQueue, ws db.Workspace, scope logexport.Scope, hours int) (logExportContext, error) {
	out := logExportContext{task: task, workspace: ws}
	in := logexport.Input{
		WorkspaceID:  uuidToString(ws.ID),
		AnchorTaskID: uuidToString(task.ID),
		Scope:        scope,
		Hours:        hours,
		GeneratedAt:  time.Now().UTC(),
	}

	tasks := []db.AgentTaskQueue{task}
	if task.IssueID.Valid {
		issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: task.IssueID, WorkspaceID: ws.ID})
		if err == nil {
			out.issue = &issue
			in.IssueID = uuidToString(issue.ID)
			in.IssueTitle = issue.Title
			in.IssueIdentifier = issueIdentifier(ws.IssuePrefix, issue.Number)
		} else if !isNotFound(err) {
			return out, fmt.Errorf("load issue: %w", err)
		}
		if scope != logexport.ScopeRun {
			all, err := h.Queries.ListTasksByIssue(ctx, task.IssueID)
			if err != nil {
				return out, fmt.Errorf("list runs: %w", err)
			}
			tasks = all
		}
	}

	agents := map[string]db.Agent{}
	for _, t := range tasks {
		run := logexport.Run{
			TaskID:        uuidToString(t.ID),
			AgentID:       uuidToString(t.AgentID),
			Status:        t.Status,
			FailureReason: t.FailureReason.String,
			Error:         t.Error.String,
			ExitCode:      exitCodeFromTaskResult(t.Result),
			Attempt:       int(t.Attempt),
			WorkDir:       t.WorkDir.String,
			CreatedAt:     t.CreatedAt.Time,
			StartedAt:     timestamptzToTimePtr(t.StartedAt),
			CompletedAt:   timestamptzToTimePtr(t.CompletedAt),
		}
		if t.AgentID.Valid {
			agent, seen := agents[run.AgentID]
			if !seen {
				if loaded, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: t.AgentID, WorkspaceID: ws.ID}); err == nil {
					agent = loaded
					// An agent's environment is where its credentials live,
					// and their names are whatever the operator chose — so the
					// literal values are masked, not only the ones a pattern
					// would recognise.
					in.Secrets = append(in.Secrets, agentEnvValues(loaded.CustomEnv)...)
				}
				agents[run.AgentID] = agent
			}
			run.AgentName = agent.Name
		}

		messages, err := h.Queries.ListTaskMessages(ctx, t.ID)
		if err != nil {
			in.Warnings = append(in.Warnings, fmt.Sprintf("run %s: log entries could not be read", run.TaskID))
			slog.Warn("log export: list task messages failed", "task_id", run.TaskID, "error", err)
		}
		for _, m := range messages {
			e := logexport.Entry{
				TaskID:          run.TaskID,
				Seq:             int(m.Seq),
				Type:            m.Type,
				Tool:            m.Tool.String,
				Content:         m.Content.String,
				Output:          m.Output.String,
				OutputTruncated: m.OutputTruncated.Bool,
				CreatedAt:       m.CreatedAt.Time,
			}
			if len(m.Input) > 0 {
				_ = json.Unmarshal(m.Input, &e.Input)
			}
			run.Entries = append(run.Entries, e)
		}
		in.Runs = append(in.Runs, run)
	}
	out.input = in
	return out, nil
}

func timestamptzToTimePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// exitCodeFromTaskResult reads an exit code out of a run's result payload
// when the runtime reported one. Most runtimes do not; nil is the honest
// answer then, and status/failure_reason carry the outcome.
func exitCodeFromTaskResult(result []byte) *int {
	if len(result) == 0 {
		return nil
	}
	var payload struct {
		ExitCode *int `json:"exit_code"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil
	}
	return payload.ExitCode
}

func agentEnvValues(customEnv []byte) []string {
	if len(customEnv) == 0 {
		return nil
	}
	var env map[string]string
	if err := json.Unmarshal(customEnv, &env); err != nil {
		return nil
	}
	values := make([]string, 0, len(env))
	for _, v := range env {
		values = append(values, v)
	}
	return values
}

func parseLogExportParams(scopeRaw string, hoursRaw int) (logexport.Scope, int, error) {
	scope, err := logexport.ParseScope(scopeRaw)
	if err != nil {
		return "", 0, err
	}
	hours := 0
	if scope == logexport.ScopeHours {
		hours = logexport.NormalizeHours(hoursRaw)
	}
	return scope, hours, nil
}

// ExportTaskLogs — GET /api/tasks/{taskId}/log-export
//
//	?scope=run|hours|task  (default run)
//	&hours=N               (scope=hours only, default 6)
//	&format=json|zip       (default json: preview + summary; zip: the bundle)
//	&allow_partial=true    (ship what was collected when some runs fail to load)
func (h *Handler) ExportTaskLogs(w http.ResponseWriter, r *http.Request) {
	task, ws, ok := h.loadLogExportTask(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	hoursRaw, _ := strconv.Atoi(q.Get("hours"))
	scope, hours, err := parseLogExportParams(q.Get("scope"), hoursRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	format := q.Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "zip" {
		writeError(w, http.StatusBadRequest, "invalid format (want json or zip)")
		return
	}

	bundle, _, ok := h.buildLogExport(w, r, task, ws, scope, hours, q.Get("allow_partial") == "true", 0)
	if !ok {
		return
	}
	_, repoConfigured := h.logExportTarget(ws)
	if bundle == nil {
		if format == "zip" {
			writeErrorCode(w, http.StatusNotFound, "no_logs", logexport.ErrNoLogs.Error())
			return
		}
		writeJSON(w, http.StatusOK, LogExportPreviewResponse{Empty: true, LogRepoConfigured: repoConfigured})
		return
	}
	if format == "zip" {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, bundle.Filename))
		w.Header().Set("Content-Length", strconv.Itoa(len(bundle.Zip)))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bundle.Zip)
		return
	}
	writeJSON(w, http.StatusOK, LogExportPreviewResponse{
		Filename:          bundle.Filename,
		SizeBytes:         len(bundle.Zip),
		Summary:           bundle.Summary,
		Meta:              &bundle.Meta,
		LogRepoConfigured: repoConfigured,
	})
}

// buildLogExport collects and builds. A nil bundle with ok=true means the
// scope is empty. On ok=false the response has been written.
func (h *Handler) buildLogExport(w http.ResponseWriter, r *http.Request, task db.AgentTaskQueue, ws db.Workspace, scope logexport.Scope, hours int, allowPartial bool, maxBytes int) (*logexport.Bundle, logExportContext, bool) {
	lc, err := h.collectLogExport(r.Context(), task, ws, scope, hours)
	if err != nil {
		slog.Warn("log export: collect failed", "task_id", uuidToString(task.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to collect logs")
		return nil, lc, false
	}
	if len(lc.input.Warnings) > 0 && !allowPartial {
		collected := 0
		for _, run := range lc.input.Runs {
			collected += len(run.Entries)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":             "some runs could not be read: " + strings.Join(lc.input.Warnings, "; "),
			"code":              "partial_available",
			"collected_entries": collected,
		})
		return nil, lc, false
	}
	lc.input.MaxBytes = maxBytes
	bundle, err := logexport.Build(lc.input)
	if errors.Is(err, logexport.ErrNoLogs) {
		return nil, lc, true
	}
	if err != nil {
		slog.Warn("log export: build failed", "task_id", uuidToString(task.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to build the log bundle")
		return nil, lc, false
	}
	return &bundle, lc, true
}

// ReportTaskLogs — POST /api/tasks/{taskId}/log-export/report
//
// Builds the bundle, delivers it, and posts ONE comment on the run's issue
// that @-mentions the assignee. Delivery prefers the workspace log repository
// (the comment then carries only a link); any push failure falls back to a
// comment attachment, so the report itself is never lost to a bad token.
func (h *Handler) ReportTaskLogs(w http.ResponseWriter, r *http.Request) {
	task, ws, ok := h.loadLogExportTask(w, r)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req logExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	scope, hours, err := parseLogExportParams(req.Scope, req.Hours)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !task.IssueID.Valid {
		writeErrorCode(w, http.StatusBadRequest, "no_issue", "this run is not attached to an issue, so there is nowhere to report it")
		return
	}

	target, repoConfigured := h.logExportTarget(ws)
	maxBytes := logExportAttachmentMaxBytes
	if repoConfigured {
		maxBytes = logExportRepoMaxBytes
	}
	bundle, lc, ok := h.buildLogExport(w, r, task, ws, scope, hours, req.AllowPartial, maxBytes)
	if !ok {
		return
	}
	if bundle == nil {
		writeErrorCode(w, http.StatusNotFound, "no_logs", logexport.ErrNoLogs.Error())
		return
	}
	if lc.issue == nil {
		writeErrorCode(w, http.StatusBadRequest, "no_issue", "the issue this run belongs to no longer exists")
		return
	}

	resp := LogExportReportResponse{
		IssueID:         lc.input.IssueID,
		IssueIdentifier: lc.input.IssueIdentifier,
	}

	if repoConfigured {
		link, pushErr := h.logExportPusher().Push(r.Context(), target,
			logexport.RepoPath(bundle.Meta, bundle.Filename),
			fmt.Sprintf("logs: %s run %s (%s)", lc.input.IssueIdentifier, lc.input.AnchorTaskID, scope),
			bundle.Zip)
		if pushErr == nil {
			resp.Delivery = "git"
			resp.Link = link
		} else {
			slog.Warn("log export: push to log repository failed; falling back to attachment",
				"workspace_id", lc.input.WorkspaceID, "task_id", lc.input.AnchorTaskID, "error", pushErr)
			resp.FallbackReason = pushErr.Error()
			// The repository cap is looser than what an attachment can carry.
			if len(bundle.Zip) > logExportAttachmentMaxBytes {
				lc.input.MaxBytes = logExportAttachmentMaxBytes
				trimmed, buildErr := logexport.Build(lc.input)
				if buildErr != nil {
					writeError(w, http.StatusInternalServerError, "failed to build the log bundle")
					return
				}
				bundle = &trimmed
			}
		}
	}

	var attachmentIDs []string
	if resp.Delivery == "" {
		att, attErr := h.storeLogExportAttachment(r, userID, ws, *lc.issue, bundle)
		if attErr != nil {
			slog.Error("log export: attachment fallback failed", "task_id", lc.input.AnchorTaskID, "error", attErr)
			writeError(w, http.StatusInternalServerError, "failed to store the log bundle")
			return
		}
		resp.Delivery = "attachment"
		attachmentIDs = []string{uuidToString(att.ID)}
	}
	resp.Filename = bundle.Filename
	resp.SizeBytes = len(bundle.Zip)

	actorType, actorID := h.resolveActor(r, userID, lc.input.WorkspaceID)
	mention, mentioned := h.logExportAssigneeMention(r.Context(), *lc.issue, actorType, actorID)
	resp.Mentioned = mentioned

	status, body := h.createCommentInternal(r, uuidToString(lc.issue.ID), CreateCommentRequest{
		Content:       logExportCommentBody(mention, *bundle, resp),
		AttachmentIDs: attachmentIDs,
	})
	if status != http.StatusCreated && status != http.StatusOK {
		// Pass the comment endpoint's own refusal through: it already speaks
		// the API's error shape.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &created)
	resp.CommentID = created.ID
	if resp.Delivery == "attachment" {
		resp.Link = ""
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) logExportPusher() logexport.Pusher {
	return logexport.Pusher{APIBase: h.LogExportGitHubAPIBase}
}

// storeLogExportAttachment writes the bundle through the same storage and
// attachment rows a client upload uses; the comment then links it by id.
func (h *Handler) storeLogExportAttachment(r *http.Request, userID string, ws db.Workspace, issue db.Issue, bundle *logexport.Bundle) (db.Attachment, error) {
	if h.Storage == nil {
		return db.Attachment{}, errors.New("file storage is not configured")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return db.Attachment{}, err
	}
	wsID := uuidToString(ws.ID)
	key := "workspaces/" + wsID + "/" + id.String() + ".zip"
	link, err := h.Storage.Upload(r.Context(), key, bundle.Zip, "application/zip", bundle.Filename)
	if err != nil {
		return db.Attachment{}, err
	}
	uploaderType, uploaderID := h.resolveActor(r, userID, wsID)
	row, err := h.Queries.CreateAttachment(r.Context(), db.CreateAttachmentParams{
		ID:           pgtype.UUID{Bytes: id, Valid: true},
		WorkspaceID:  ws.ID,
		IssueID:      issue.ID,
		UploaderType: uploaderType,
		UploaderID:   parseUUID(uploaderID),
		Filename:     bundle.Filename,
		Url:          link,
		ContentType:  "application/zip",
		SizeBytes:    int64(len(bundle.Zip)),
	})
	if err != nil {
		h.deleteS3Objects(r.Context(), []string{link})
		return db.Attachment{}, err
	}
	return row.Attachment(), nil
}

// logExportAssigneeMention returns the mention markdown for the issue's
// assignee and the name it used. It is empty when there is no assignee, the
// assignee cannot be resolved, or the assignee is the one reporting — an agent
// mentioning itself would only queue a run to read its own report.
func (h *Handler) logExportAssigneeMention(ctx context.Context, issue db.Issue, actorType, actorID string) (string, string) {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return "", ""
	}
	kind := issue.AssigneeType.String
	id := uuidToString(issue.AssigneeID)
	if kind == actorType && id == actorID {
		return "", ""
	}
	var label string
	switch kind {
	case "member":
		user, err := h.Queries.GetUser(ctx, issue.AssigneeID)
		if err != nil {
			return "", ""
		}
		label = sanitizeMentionLabel(user.Name)
	default:
		resolved, ok := h.resolveAssigneeMentionLabel(ctx, issue.WorkspaceID, kind, issue.AssigneeID)
		if !ok {
			return "", ""
		}
		label = resolved
	}
	return fmt.Sprintf("[@%s](mention://%s/%s)", label, kind, id), label
}

func logExportCommentBody(mention string, bundle logexport.Bundle, resp LogExportReportResponse) string {
	m := bundle.Meta
	var b strings.Builder
	if mention != "" {
		b.WriteString(mention + " ")
	}
	b.WriteString("Task logs reported for this issue.\n\n")
	fmt.Fprintf(&b, "- Run: `%s`\n", m.AnchorTaskID)
	switch m.Scope {
	case logexport.ScopeHours:
		fmt.Fprintf(&b, "- Scope: last %d hours\n", m.Hours)
	case logexport.ScopeTask:
		b.WriteString("- Scope: entire task\n")
	default:
		b.WriteString("- Scope: this run\n")
	}
	fmt.Fprintf(&b, "- Runs: %d, log entries: %d\n", m.RunCount, m.EntryCount)
	if m.Window.From != nil && m.Window.To != nil {
		fmt.Fprintf(&b, "- Window: %s → %s\n", m.Window.From.UTC().Format(time.RFC3339), m.Window.To.UTC().Format(time.RFC3339))
	}
	for _, run := range m.Runs {
		if run.TaskID != m.AnchorTaskID {
			continue
		}
		line := "- Status: " + run.Status
		if run.FailureReason != "" {
			line += " (" + run.FailureReason + ")"
		}
		if run.ExitCode != nil {
			line += fmt.Sprintf(", exit code %d", *run.ExitCode)
		}
		b.WriteString(line + "\n")
	}
	if m.DroppedEntries > 0 {
		fmt.Fprintf(&b, "- Trimmed: the %d oldest entries were dropped to fit the size limit\n", m.DroppedEntries)
	}
	if m.Partial {
		b.WriteString("- Partial: some runs could not be read\n")
	}
	b.WriteString("- Redacted: tokens, passwords and environment variable values removed\n\n")
	if resp.Delivery == "git" {
		fmt.Fprintf(&b, "Log bundle: [%s](%s)\n", bundle.Filename, resp.Link)
	} else {
		fmt.Fprintf(&b, "Log bundle attached: `%s`\n", bundle.Filename)
		if resp.FallbackReason != "" {
			b.WriteString("\n_The workspace log repository could not be reached, so the bundle is attached here instead._\n")
		}
	}
	return b.String()
}

// createCommentInternal posts a comment through the real CreateComment
// handler, as the caller. Re-entering the handler — rather than inserting a
// row — is what keeps a reported comment identical to a typed one: mention
// parsing, notifications, agent triggers, attachment linking and realtime
// events all live in that one path and must not grow a second copy here.
func (h *Handler) createCommentInternal(r *http.Request, issueID string, body CreateCommentRequest) (int, []byte) {
	payload, err := json.Marshal(body)
	if err != nil {
		return http.StatusInternalServerError, []byte(`{"error":"failed to encode comment"}`)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", issueID)
	inner := r.Clone(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	inner.Method = http.MethodPost
	inner.Body = io.NopCloser(bytes.NewReader(payload))
	inner.ContentLength = int64(len(payload))
	inner.Header.Set("Content-Type", "application/json")

	rec := &bufferedResponse{header: http.Header{}, status: http.StatusOK}
	h.CreateComment(rec, inner)
	return rec.status, rec.body.Bytes()
}

// bufferedResponse is the minimal http.ResponseWriter createCommentInternal
// needs to read a handler's answer.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) WriteHeader(status int)      { b.status = status }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }
