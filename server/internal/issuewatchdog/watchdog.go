package issuewatchdog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	watchdogCommentMarker = "<!-- office-issue-watchdog -->"
	defaultHeartbeatFile  = "/var/lib/multica/issue-watchdog/last-run"
)

type Config struct {
	Enabled          bool
	WorkspaceID      string
	WebhookURL       string
	WebhookKey       string
	TickInterval     time.Duration
	TodoStartDelay   time.Duration
	IdleInterval     time.Duration
	ManagerWait      time.Duration
	MaxEmployeeRound int
	HeartbeatFile    string
}

func ConfigFromEnv() (Config, error) {
	enabled := false
	if raw := strings.TrimSpace(os.Getenv("MULTICA_ISSUE_WATCHDOG_ENABLED")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, errors.New("MULTICA_ISSUE_WATCHDOG_ENABLED must be true or false")
		}
		enabled = parsed
	}
	parseDuration := func(name string, fallback time.Duration) (time.Duration, error) {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return fallback, nil
		}
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 {
			return 0, fmt.Errorf("%s must be a positive duration", name)
		}
		return value, nil
	}
	tick, err := parseDuration("MULTICA_ISSUE_WATCHDOG_TICK", time.Minute)
	if err != nil {
		return Config{}, err
	}
	todoDelay, err := parseDuration("MULTICA_ISSUE_WATCHDOG_TODO_DELAY", 10*time.Minute)
	if err != nil {
		return Config{}, err
	}
	idleInterval, err := parseDuration("MULTICA_ISSUE_WATCHDOG_IDLE_INTERVAL", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}
	managerWait, err := parseDuration("MULTICA_ISSUE_WATCHDOG_MANAGER_WAIT", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	rounds := 2
	if raw := strings.TrimSpace(os.Getenv("MULTICA_ISSUE_WATCHDOG_EMPLOYEE_ROUNDS")); raw != "" {
		rounds, err = strconv.Atoi(raw)
		if err != nil || rounds <= 0 {
			return Config{}, errors.New("MULTICA_ISSUE_WATCHDOG_EMPLOYEE_ROUNDS must be a positive integer")
		}
	}
	cfg := Config{
		Enabled:          enabled,
		WorkspaceID:      strings.TrimSpace(os.Getenv("MULTICA_ISSUE_WATCHDOG_WORKSPACE_ID")),
		WebhookURL:       strings.TrimSpace(os.Getenv("MULTICA_ISSUE_WATCHDOG_WEBHOOK_URL")),
		WebhookKey:       os.Getenv("MULTICA_ISSUE_WATCHDOG_WEBHOOK_KEY"),
		TickInterval:     tick,
		TodoStartDelay:   todoDelay,
		IdleInterval:     idleInterval,
		ManagerWait:      managerWait,
		MaxEmployeeRound: rounds,
		HeartbeatFile:    strings.TrimSpace(os.Getenv("MULTICA_ISSUE_WATCHDOG_HEARTBEAT_FILE")),
	}
	if cfg.HeartbeatFile == "" {
		cfg.HeartbeatFile = defaultHeartbeatFile
	}
	if cfg.Enabled {
		if _, err := uuid.Parse(cfg.WorkspaceID); err != nil {
			return Config{}, errors.New("MULTICA_ISSUE_WATCHDOG_WORKSPACE_ID must be a UUID when enabled")
		}
		if cfg.WebhookURL == "" || cfg.WebhookKey == "" {
			return Config{}, errors.New("issue watchdog webhook URL and key must both be configured when enabled")
		}
		u, err := url.Parse(cfg.WebhookURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname()))) {
			return Config{}, errors.New("issue watchdog webhook must use HTTPS (HTTP is allowed only for loopback test receivers)")
		}
		if cfg.TickInterval <= 0 || cfg.TodoStartDelay <= 0 || cfg.IdleInterval <= 0 || cfg.ManagerWait <= 0 || cfg.MaxEmployeeRound < 1 {
			return Config{}, errors.New("issue watchdog thresholds must be positive")
		}
	}
	return cfg, nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

type NudgeFunc func(context.Context, string, string, string, string) error

type Watchdog struct {
	pool   *pgxpool.Pool
	cfg    Config
	nudge  NudgeFunc
	client *http.Client
	now    func() time.Time
}

func New(pool *pgxpool.Pool, cfg Config, nudge NudgeFunc) *Watchdog {
	return &Watchdog{
		pool:  pool,
		cfg:   cfg,
		nudge: nudge,
		client: &http.Client{
			Timeout: 8 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: time.Now,
	}
}

type Issue struct {
	ID              string
	WorkspaceID     string
	Number          int32
	Identifier      string
	Title           string
	Status          string
	AssigneeType    string
	AssigneeID      string
	EmployeeName    string
	ManagerName     string
	ProjectName     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FirstExecutedAt *time.Time
	ProgressAt      time.Time
	IssueRevision   int64
}

type webhookEvent struct {
	EventID     string    `json:"event_id"`
	EventType   string    `json:"event_type"`
	IssueID     string    `json:"issue_id"`
	IssueNumber string    `json:"issue_number"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	Employee    string    `json:"employee"`
	Manager     string    `json:"manager"`
	ProjectName string    `json:"project_name"`
	WakeTarget  string    `json:"wake_target"`
	Reason      string    `json:"reason"`
	OccurredAt  time.Time `json:"occurred_at"`
}

type issueState struct {
	AssigneeType     string
	AssigneeID       string
	AssignedAt       *time.Time
	LastProgressAt   time.Time
	EmployeeRounds   int
	LastNudgeAt      *time.Time
	ManagerEscalated bool
}

func (w *Watchdog) HandleEvent(e events.Event) {
	if !w.cfg.Enabled || e.WorkspaceID != w.cfg.WorkspaceID {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch e.Type {
	case protocol.EventIssueUpdated:
		payload, ok := payloadMap(e.Payload)
		if !ok {
			return
		}
		if changed, _ := payload["status_changed"].(bool); changed {
			issue, ok := w.issueFromPayload(ctx, e.WorkspaceID, payload["issue"])
			if !ok {
				return
			}
			w.resolveManagerWaits(ctx, issue.ID)
			switch issue.Status {
			case "in_review", "blocked", "done":
				rev := issue.IssueRevision
				if rev == 0 {
					rev = w.now().UnixNano()
				}
				_ = w.notifyManager(ctx, issue, "status_"+issue.Status,
					fmt.Sprintf("status:%s:%d:%s", issue.ID, rev, issue.Status),
					"负责人状态变更为 "+issue.Status, "manager", w.now())
			}
		}
		if changed, _ := payload["assignee_changed"].(bool); changed {
			issue, ok := w.issueFromPayload(ctx, e.WorkspaceID, payload["issue"])
			if ok {
				w.rebaselineAssignment(ctx, issue, w.now())
			}
		}
	case protocol.EventCommentCreated:
		payload, ok := payloadMap(e.Payload)
		if !ok {
			return
		}
		comment, ok := payloadMap(payload["comment"])
		if !ok {
			return
		}
		issueID, _ := comment["issue_id"].(string)
		authorType, _ := comment["author_type"].(string)
		authorID, _ := comment["author_id"].(string)
		content, _ := comment["content"].(string)
		commentType, _ := comment["type"].(string)
		commentID, _ := comment["id"].(string)
		if issueID == "" || authorType == "system" || commentType != "comment" || isWatchdogComment(content) {
			return
		}
		issue, err := w.loadIssue(ctx, e.WorkspaceID, issueID)
		if err != nil {
			return
		}
		if authorType == "member" && isManagerReply(content, issue.ManagerName) {
			w.resolveManagerWaitsForReply(ctx, issue.ID, content)
		}
		if issue.AssigneeType != authorType || issue.AssigneeID == "" || issue.AssigneeID != authorID || isPureAcknowledgment(content) {
			return
		}
		if commentID == "" {
			commentID = w.now().Format(time.RFC3339Nano)
		}
		_ = w.notifyManager(ctx, issue, "employee_comment",
			"employee-comment:"+commentID,
			"负责员工在单子里发了新评论，等待负责经理回应", "manager", w.now())
	}
}

func payloadMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	return m, true
}

func (w *Watchdog) issueFromPayload(ctx context.Context, workspaceID string, raw any) (Issue, bool) {
	fields, ok := payloadMap(raw)
	if !ok {
		return Issue{}, false
	}
	id, _ := fields["id"].(string)
	if id == "" {
		return Issue{}, false
	}
	issue, err := w.loadIssue(ctx, workspaceID, id)
	return issue, err == nil
}

func (w *Watchdog) loadIssue(ctx context.Context, workspaceID, issueID string) (Issue, error) {
	var out Issue
	var firstExecutedAt pgtype.Timestamptz
	err := w.pool.QueryRow(ctx, `
SELECT i.id::text, i.workspace_id::text, i.number, w.issue_prefix || '-' || i.number::text,
       i.title, i.status, COALESCE(i.assignee_type, ''), COALESCE(i.assignee_id::text, ''),
       COALESCE(a.name, u.name, ''), COALESCE(manager_option.value->>'name', 'CTO·扳手'),
       COALESCE(p.title, ''), i.created_at, i.updated_at, i.first_executed_at,
       GREATEST(
         i.created_at,
         COALESCE((SELECT max(c.created_at)
                   FROM comment c
                   WHERE c.workspace_id = i.workspace_id AND c.issue_id = i.id
                     AND c.author_type IN ('member', 'agent') AND c.type = 'comment'
                     AND c.content NOT LIKE '%' || $3 || '%'
                     AND regexp_replace(lower(trim(c.content)),
                         '[[:space:][:punct:]，。！？、；：…]+', '', 'g') <> '收到'), i.created_at),
         COALESCE((SELECT max(e.occurred_at)
                   FROM office_event e
                   WHERE e.workspace_id = i.workspace_id AND e.issue_id = i.id
                     AND e.event_type = 'issue.status_changed'), i.created_at)
       ) AS progress_at,
       i.revision
FROM issue i
JOIN workspace w ON w.id = i.workspace_id
LEFT JOIN project p ON p.id = i.project_id AND p.workspace_id = i.workspace_id
LEFT JOIN agent a ON i.assignee_type = 'agent' AND a.id = i.assignee_id AND a.workspace_id = i.workspace_id
LEFT JOIN member m ON i.assignee_type = 'member' AND m.user_id = i.assignee_id AND m.workspace_id = i.workspace_id
LEFT JOIN "user" u ON u.id = m.user_id
LEFT JOIN issue_property prop ON prop.workspace_id = i.workspace_id
     AND prop.name = '负责经理' AND prop.archived_at IS NULL
LEFT JOIN LATERAL (
    SELECT value
    FROM jsonb_array_elements(COALESCE(prop.config->'options', '[]'::jsonb)) AS options(value)
    WHERE value->>'id' = i.properties->>prop.id::text
    LIMIT 1
) manager_option ON true
WHERE i.workspace_id = $1::uuid AND i.id = $2::uuid`,
		workspaceID, issueID, watchdogCommentMarker,
	).Scan(&out.ID, &out.WorkspaceID, &out.Number, &out.Identifier, &out.Title, &out.Status,
		&out.AssigneeType, &out.AssigneeID, &out.EmployeeName, &out.ManagerName, &out.ProjectName,
		&out.CreatedAt, &out.UpdatedAt, &firstExecutedAt, &out.ProgressAt, &out.IssueRevision)
	if err != nil {
		return Issue{}, err
	}
	if firstExecutedAt.Valid {
		out.FirstExecutedAt = &firstExecutedAt.Time
	}
	if out.EmployeeName == "" {
		out.EmployeeName = "未指派"
	}
	return out, nil
}

func (w *Watchdog) Run(ctx context.Context, now time.Time) error {
	if !w.cfg.Enabled {
		return nil
	}
	if now.IsZero() {
		now = w.now()
	}
	if err := writeHeartbeat(w.cfg.HeartbeatFile, now); err != nil {
		return fmt.Errorf("write issue watchdog heartbeat")
	}
	rows, err := w.pool.Query(ctx, `
SELECT id::text
FROM issue
WHERE workspace_id = $1::uuid
  AND status IN ('todo', 'in_progress', 'in_review', 'blocked', 'done')
ORDER BY updated_at ASC
LIMIT 5000`, w.cfg.WorkspaceID)
	if err != nil {
		return fmt.Errorf("list issue watchdog candidates")
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("read issue watchdog candidate")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read issue watchdog candidates")
	}
	rows.Close()

	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		issue, err := w.loadIssue(ctx, w.cfg.WorkspaceID, id)
		if err != nil {
			continue
		}
		if err := w.checkIssue(ctx, issue, now); err != nil {
			slog.WarnContext(ctx, "issue watchdog scan item failed", "issue_id", issue.ID)
		}
	}
	if err := w.escalateExpiredManagerWaits(ctx, now); err != nil {
		return fmt.Errorf("process issue watchdog manager waits")
	}
	return nil
}

func (w *Watchdog) checkIssue(ctx context.Context, issue Issue, now time.Time) error {
	state, err := w.loadOrUpdateState(ctx, issue, now)
	if err != nil {
		return err
	}
	if issue.Status != "todo" && issue.Status != "in_progress" {
		return nil
	}
	if issue.AssigneeID == "" || (issue.AssigneeType != "agent" && issue.AssigneeType != "member") {
		return nil
	}
	if issue.Status == "todo" && issue.FirstExecutedAt != nil {
		return nil
	}
	if state.EmployeeRounds >= w.cfg.MaxEmployeeRound {
		if !state.ManagerEscalated {
			key := fmt.Sprintf("stalled:%s:%d", issue.ID, state.LastProgressAt.UnixNano())
			if err := w.notifyManager(ctx, issue, "employee_unresponsive", key,
				"连续两轮唤醒负责员工后，单子仍无有效进展", "manager", now); err != nil {
				return err
			}
			_, err := w.pool.Exec(ctx, `
UPDATE office_issue_watchdog_state
SET manager_escalated_at = COALESCE(manager_escalated_at, $2), updated_at = now()
WHERE issue_id = $1::uuid`, issue.ID, now)
			return err
		}
		return nil
	}

	dueAt := state.LastProgressAt.Add(w.cfg.IdleInterval)
	if issue.Status == "todo" && state.EmployeeRounds == 0 && state.AssignedAt != nil {
		dueAt = state.AssignedAt.Add(w.cfg.TodoStartDelay)
	} else if state.EmployeeRounds > 0 && state.LastNudgeAt != nil {
		dueAt = state.LastNudgeAt.Add(w.cfg.IdleInterval)
	}
	if now.Before(dueAt) {
		return nil
	}
	round := state.EmployeeRounds + 1
	eventKey := fmt.Sprintf("employee:%s:%d:%d", issue.ID, state.LastProgressAt.UnixNano(), round)
	attemptID, claimed, err := w.claimEmployeeRound(ctx, issue, eventKey, round, now)
	if err != nil || !claimed {
		return err
	}
	age := now.Sub(state.LastProgressAt)
	if issue.Status == "todo" && round == 1 && state.AssignedAt != nil {
		age = now.Sub(*state.AssignedAt)
	}
	reason := employeeNudgeReason(issue, round, age)
	nudgeErr := error(nil)
	if w.nudge == nil {
		nudgeErr = errors.New("employee wake path is unavailable")
	} else {
		nudgeErr = w.nudge(ctx, issue.ID, issue.AssigneeType, issue.AssigneeID, reason)
	}
	w.finishAttempt(ctx, attemptID, nudgeErr == nil)
	if round == w.cfg.MaxEmployeeRound {
		key := fmt.Sprintf("stalled:%s:%d", issue.ID, state.LastProgressAt.UnixNano())
		if err := w.notifyManager(ctx, issue, "employee_unresponsive", key,
			"连续两轮唤醒负责员工后，单子仍无有效进展", "manager", now); err != nil {
			return err
		}
		_, err = w.pool.Exec(ctx, `
UPDATE office_issue_watchdog_state
SET manager_escalated_at = COALESCE(manager_escalated_at, $2), updated_at = now()
WHERE issue_id = $1::uuid`, issue.ID, now)
		if err != nil {
			return err
		}
	}
	if nudgeErr != nil {
		return errors.New("employee wake attempt failed")
	}
	return nil
}

func (w *Watchdog) claimEmployeeRound(ctx context.Context, issue Issue, eventKey string, round int, now time.Time) (int64, bool, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `
INSERT INTO office_issue_watchdog_attempt
    (workspace_id, issue_id, event_key, event_type, target)
VALUES ($1::uuid, $2::uuid, $3, 'employee_nudge', 'employee')
ON CONFLICT (workspace_id, issue_id, event_key) DO NOTHING
RETURNING id`, issue.WorkspaceID, issue.ID, eventKey).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(ctx, `
UPDATE office_issue_watchdog_state
SET employee_rounds = $2, last_nudge_at = $3, updated_at = now()
WHERE issue_id = $1::uuid`, issue.ID, round, now); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func employeeNudgeReason(issue Issue, round int, age time.Duration) string {
	if issue.Status == "todo" {
		return fmt.Sprintf("已派给你，但 %d 分钟仍未开工（第 %d 轮提醒）。请开始处理并在单子里写进展。", int(age.Minutes()), round)
	}
	return fmt.Sprintf("这张单已 %d 分钟没有有效进展（第 %d 轮提醒）。请继续处理并在单子里更新进展或状态。", int(age.Minutes()), round)
}

func (w *Watchdog) loadOrUpdateState(ctx context.Context, issue Issue, now time.Time) (issueState, error) {
	assignedAt := any(nil)
	if issue.Status == "todo" && issue.AssigneeID != "" {
		assignedAt = issue.UpdatedAt
		if issue.UpdatedAt.IsZero() {
			assignedAt = now
		}
	}
	lastProgressAt := issue.ProgressAt
	if lastProgressAt.IsZero() {
		lastProgressAt = now
	}
	_, err := w.pool.Exec(ctx, `
INSERT INTO office_issue_watchdog_state (workspace_id, issue_id, assignee_type, assignee_id,
    assigned_at, last_progress_at)
VALUES ($1::uuid, $2::uuid, $3, NULLIF($4, '')::uuid, $5::timestamptz, $6::timestamptz)
ON CONFLICT (issue_id) DO NOTHING`,
		issue.WorkspaceID, issue.ID, issue.AssigneeType, issue.AssigneeID, assignedAt, lastProgressAt)
	if err != nil {
		return issueState{}, err
	}
	var state issueState
	err = w.pool.QueryRow(ctx, `
SELECT assignee_type, COALESCE(assignee_id::text, ''), assigned_at,
       last_progress_at, employee_rounds, last_nudge_at, manager_escalated_at IS NOT NULL
FROM office_issue_watchdog_state
WHERE issue_id = $1::uuid`, issue.ID).Scan(&state.AssigneeType, &state.AssigneeID, &state.AssignedAt,
		&state.LastProgressAt, &state.EmployeeRounds, &state.LastNudgeAt, &state.ManagerEscalated)
	if err != nil {
		return issueState{}, err
	}
	if state.AssigneeType != issue.AssigneeType || state.AssigneeID != issue.AssigneeID {
		assignedAt := any(nil)
		if issue.Status == "todo" && issue.AssigneeID != "" {
			assignedAt = now
		}
		_, err = w.pool.Exec(ctx, `
UPDATE office_issue_watchdog_state
SET assignee_type = $2, assignee_id = NULLIF($3, '')::uuid, assigned_at = $4::timestamptz,
    last_progress_at = $5::timestamptz, employee_rounds = 0, last_nudge_at = NULL,
    manager_escalated_at = NULL, updated_at = now()
WHERE issue_id = $1::uuid`, issue.ID, issue.AssigneeType, issue.AssigneeID, assignedAt, now)
		if err != nil {
			return issueState{}, err
		}
		state = issueState{AssigneeType: issue.AssigneeType, AssigneeID: issue.AssigneeID,
			AssignedAt: timePtrOrNil(assignedAt), LastProgressAt: now}
		return state, nil
	}
	if issue.ProgressAt.After(state.LastProgressAt) {
		_, err = w.pool.Exec(ctx, `
UPDATE office_issue_watchdog_state
SET last_progress_at = $2, employee_rounds = 0, last_nudge_at = NULL,
    manager_escalated_at = NULL, updated_at = now()
WHERE issue_id = $1::uuid`, issue.ID, issue.ProgressAt)
		if err != nil {
			return issueState{}, err
		}
		state.LastProgressAt = issue.ProgressAt
		state.EmployeeRounds = 0
		state.LastNudgeAt = nil
		state.ManagerEscalated = false
	}
	return state, nil
}

func timePtrOrNil(v any) *time.Time {
	if t, ok := v.(time.Time); ok {
		return &t
	}
	return nil
}

func (w *Watchdog) rebaselineAssignment(ctx context.Context, issue Issue, now time.Time) {
	assignedAt := any(nil)
	if issue.Status == "todo" && issue.AssigneeID != "" {
		assignedAt = now
	}
	_, _ = w.pool.Exec(ctx, `
INSERT INTO office_issue_watchdog_state (workspace_id, issue_id, assignee_type, assignee_id,
    assigned_at, last_progress_at)
VALUES ($1::uuid, $2::uuid, $3, NULLIF($4, '')::uuid, $5::timestamptz, $6::timestamptz)
ON CONFLICT (issue_id) DO UPDATE
SET assignee_type = EXCLUDED.assignee_type, assignee_id = EXCLUDED.assignee_id,
    assigned_at = EXCLUDED.assigned_at, last_progress_at = EXCLUDED.last_progress_at,
    employee_rounds = 0, last_nudge_at = NULL, manager_escalated_at = NULL, updated_at = now()`,
		issue.WorkspaceID, issue.ID, issue.AssigneeType, issue.AssigneeID, assignedAt, now)
}

func (w *Watchdog) notifyManager(ctx context.Context, issue Issue, kind, key, reason, target string, now time.Time) error {
	waitUntil := now.Add(w.cfg.ManagerWait)
	id, claimed, err := w.claim(ctx, issue, key, kind, target, issue.ManagerName, &waitUntil)
	if err != nil || !claimed {
		return err
	}
	event := webhookEvent{
		EventID:     key,
		EventType:   kind,
		IssueID:     issue.ID,
		IssueNumber: issue.Identifier,
		Title:       issue.Title,
		Status:      issue.Status,
		Employee:    issue.EmployeeName,
		Manager:     issue.ManagerName,
		ProjectName: issue.ProjectName,
		WakeTarget:  target,
		Reason:      reason,
		OccurredAt:  now.UTC(),
	}
	err = w.post(ctx, event)
	w.finishAttempt(ctx, id, err == nil)
	if err != nil {
		return errors.New("issue watchdog webhook attempt failed")
	}
	return nil
}

func (w *Watchdog) notifyCOO(ctx context.Context, issue Issue, key, reason string, now time.Time) error {
	id, claimed, err := w.claim(ctx, issue, key, "manager_timeout", "COO·Grok", issue.ManagerName, nil)
	if err != nil || !claimed {
		return err
	}
	event := webhookEvent{
		EventID: key, EventType: "manager_timeout", IssueID: issue.ID,
		IssueNumber: issue.Identifier, Title: issue.Title, Status: issue.Status,
		Employee: issue.EmployeeName, Manager: issue.ManagerName, ProjectName: issue.ProjectName,
		WakeTarget: "COO·Grok", Reason: reason, OccurredAt: now.UTC(),
	}
	err = w.post(ctx, event)
	w.finishAttempt(ctx, id, err == nil)
	if err != nil {
		return errors.New("issue watchdog COO escalation attempt failed")
	}
	return nil
}

func (w *Watchdog) claim(ctx context.Context, issue Issue, key, kind, target, manager string, waitUntil *time.Time) (int64, bool, error) {
	var id int64
	err := w.pool.QueryRow(ctx, `
INSERT INTO office_issue_watchdog_attempt
    (workspace_id, issue_id, event_key, event_type, target, manager_name, manager_wait_until)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)
ON CONFLICT (workspace_id, issue_id, event_key) DO NOTHING
RETURNING id`, issue.WorkspaceID, issue.ID, key, kind, target, manager, waitUntil).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func (w *Watchdog) finishAttempt(ctx context.Context, id int64, sent bool) {
	outcome := "failed"
	if sent {
		outcome = "sent"
	}
	_, _ = w.pool.Exec(ctx, `
UPDATE office_issue_watchdog_attempt SET outcome = $2
WHERE id = $1 AND outcome = 'claimed'`, id, outcome)
}

func (w *Watchdog) post(ctx context.Context, event webhookEvent) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.WebhookURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.cfg.WebhookKey)
	resp, err := w.client.Do(req)
	if err != nil {
		slog.WarnContext(ctx, "issue watchdog webhook attempt failed", "event_type", event.EventType, "issue_id", event.IssueID)
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.WarnContext(ctx, "issue watchdog webhook returned non-success", "event_type", event.EventType,
			"issue_id", event.IssueID, "http_status", resp.StatusCode)
		return fmt.Errorf("webhook status %d", resp.StatusCode)
	}
	return nil
}

func (w *Watchdog) resolveManagerWaits(ctx context.Context, issueID string) {
	_, _ = w.pool.Exec(ctx, `
UPDATE office_issue_watchdog_attempt
SET manager_resolved_at = now()
WHERE issue_id = $1::uuid AND manager_wait_until IS NOT NULL
  AND manager_resolved_at IS NULL AND manager_escalated_at IS NULL`, issueID)
}

func (w *Watchdog) resolveManagerWaitsForReply(ctx context.Context, issueID, content string) {
	rows, err := w.pool.Query(ctx, `
SELECT id, manager_name
FROM office_issue_watchdog_attempt
WHERE issue_id = $1::uuid AND manager_wait_until IS NOT NULL
  AND manager_resolved_at IS NULL AND manager_escalated_at IS NULL`,
		issueID)
	if err != nil {
		return
	}
	type waiting struct {
		id      int64
		manager string
	}
	var waits []waiting
	for rows.Next() {
		var wait waiting
		if rows.Scan(&wait.id, &wait.manager) == nil {
			waits = append(waits, wait)
		}
	}
	rows.Close()
	for _, wait := range waits {
		if isManagerReply(content, wait.manager) {
			_, _ = w.pool.Exec(ctx, `
UPDATE office_issue_watchdog_attempt SET manager_resolved_at = now()
WHERE id = $1 AND manager_resolved_at IS NULL`, wait.id)
		}
	}
}

func (w *Watchdog) escalateExpiredManagerWaits(ctx context.Context, now time.Time) error {
	rows, err := w.pool.Query(ctx, `
SELECT a.id, a.issue_id::text, a.workspace_id::text, a.event_key, a.manager_name
FROM office_issue_watchdog_attempt a
WHERE a.workspace_id = $1::uuid AND a.manager_wait_until <= $2
  AND a.manager_wait_until IS NOT NULL
  AND a.manager_resolved_at IS NULL AND a.manager_escalated_at IS NULL
ORDER BY a.manager_wait_until, a.id
LIMIT 200`, w.cfg.WorkspaceID, now)
	if err != nil {
		return err
	}
	type pending struct {
		id                             int64
		issue, workspace, key, manager string
	}
	var pendingRows []pending
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.id, &item.issue, &item.workspace, &item.key, &item.manager); err != nil {
			rows.Close()
			return err
		}
		pendingRows = append(pendingRows, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range pendingRows {
		if w.managerResponded(ctx, item.issue, item.workspace, item.manager, item.id) {
			_, _ = w.pool.Exec(ctx, `
UPDATE office_issue_watchdog_attempt SET manager_resolved_at = $2
WHERE id = $1 AND manager_resolved_at IS NULL`, item.id, now)
			continue
		}
		issue, err := w.loadIssue(ctx, item.workspace, item.issue)
		if err != nil {
			continue
		}
		key := fmt.Sprintf("coo-escalation:%d", item.id)
		if err := w.notifyCOO(ctx, issue, key,
			"负责经理「"+item.manager+"」收到提醒后 15 分钟内没有在单子里有效回复，请 COO·Grok 本人接手处理", now); err != nil {
			continue
		}
		_, _ = w.pool.Exec(ctx, `
UPDATE office_issue_watchdog_attempt SET manager_escalated_at = $2
WHERE id = $1 AND manager_escalated_at IS NULL`, item.id, now)
	}
	return nil
}

func (w *Watchdog) managerResponded(ctx context.Context, issueID, workspaceID, manager string, attemptID int64) bool {
	var statusChanged bool
	err := w.pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM office_event e
    JOIN office_issue_watchdog_attempt a ON a.id = $3
    WHERE e.issue_id = $1::uuid AND e.workspace_id = $2::uuid
      AND e.event_type = 'issue.status_changed' AND e.occurred_at > a.attempted_at
)`, issueID, workspaceID, attemptID).Scan(&statusChanged)
	if err != nil || statusChanged {
		return statusChanged
	}
	rows, err := w.pool.Query(ctx, `
SELECT c.content
FROM comment c
JOIN office_issue_watchdog_attempt a ON a.id = $3
WHERE c.issue_id = $1::uuid AND c.workspace_id = $2::uuid
  AND c.author_type = 'member' AND c.type = 'comment'
  AND c.created_at > a.attempted_at
ORDER BY c.created_at ASC
LIMIT 200`, issueID, workspaceID, attemptID)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var content string
		if rows.Scan(&content) == nil && isManagerReply(content, manager) {
			return true
		}
	}
	return false
}

func writeHeartbeat(path string, now time.Time) error {
	if path == "" {
		path = defaultHeartbeatFile
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".last-run-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0640); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(now.UTC().Format(time.RFC3339Nano) + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func isWatchdogComment(content string) bool {
	return strings.Contains(content, watchdogCommentMarker)
}

func isPureAcknowledgment(content string) bool {
	normalized := strings.TrimSpace(content)
	normalized = strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "",
		"。", "", "！", "", "!", "", "？", "", "?", "", "，", "", ",", "",
		"；", "", ";", "", "：", "", ":", "", "…", "", ".", "").Replace(normalized)
	return normalized == "收到"
}

func isManagerReply(content, manager string) bool {
	if isPureAcknowledgment(content) {
		return false
	}
	for _, prefix := range managerSignatures(manager) {
		if !strings.HasPrefix(strings.TrimSpace(content), prefix) {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), prefix))
		body = strings.TrimLeft(body, " \t\r\n:：-—，,。")
		return body != "" && !isPureAcknowledgment(body)
	}
	return false
}

func managerSignatures(manager string) []string {
	names := map[string][]string{
		"CTO·扳手":   {"扳手"},
		"内容总监·小喇叭": {"小喇叭"},
		"选品经理·放大镜": {"放大镜"},
		"论文负责人·学霸": {"学霸"},
		"COO·Grok": {"COO·Grok"},
	}
	if aliases, ok := names[manager]; ok {
		out := make([]string, 0, len(aliases)*6)
		for _, alias := range aliases {
			for _, prefix := range []string{"负责经理·" + alias, "负责经理" + alias} {
				out = append(out, "【"+prefix+"】", "["+prefix+"]", prefix)
			}
		}
		if manager == "COO·Grok" {
			out = append(out, "【COO·Grok】", "[COO·Grok]", "COO·Grok")
		}
		return out
	}
	return nil
}
