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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
)

const (
	watchdogCommentMarker = "<!-- office-issue-watchdog -->"
	defaultHeartbeatFile  = "/var/lib/multica/issue-watchdog/last-run"

	// RunningSilentEventType is the project-group reminder for an assignee
	// whose run is alive but has not reported in the issue. The issue detail
	// endpoint reads the same value back from the attempt log.
	RunningSilentEventType = "employee_running_silent"
	projectGroupTarget     = "project_group"

	// KunDecisionEventType asks the issue's chat group (kunGroup) for Kun's
	// decision when an issue takes the Kun-decision label (Config.KunLabel).
	KunDecisionEventType = "kun_decision"
	officeGroupTarget    = "office_group"
	defaultKunLabel      = "等 Kun 拍板"
	defaultKunGroup      = "Multica 办公室"

	// An issue's 「回哪个群」 is its reply_group metadata, a chat group name or
	// one of the two values below (DENE-2587). A sub-issue may defer to its
	// parent; an issue with none falls back to its project's group.
	replyGroupKey          = "reply_group"
	replyGroupFollowParent = "随上级单"
	replyGroupNone         = "不发群"
	unprojectedGroup       = "杂项"
	// maxReplyGroupHops bounds the walk up 随上级单 parents.
	maxReplyGroupHops = 10

	// liveCommandFreshness is how recent the run's live report must be for a
	// reminder to name its command (DENE-2667 PR 4). The daemon reports every
	// 15 seconds, so an older report means it stopped reporting and the
	// command may have finished long ago.
	liveCommandFreshness = 2 * time.Minute
	// reasonCommandMaxRunes keeps the reminder line readable in a group chat;
	// current_command carries the command as reported.
	reasonCommandMaxRunes = 120
)

type Config struct {
	Owners     OwnerTable
	OwnersFile string
	// AppURL is the web origin (MULTICA_APP_URL, else FRONTEND_ORIGIN) used
	// for the issue link in owner reminders; empty leaves the link out.
	AppURL string

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
	// InReviewWait is how long an in_review issue, which only its responsible
	// manager moves on, can go with no progress and no run before the manager
	// is alerted; TodoStartDelay does the same for a started issue put back in
	// todo. Zero turns the in_review alert off.
	InReviewWait time.Duration
	// An issue carrying the KunLabel label waits on Kun: it is pushed to the
	// KunGroup chat group once each time it takes the label, and nobody is
	// woken on it until the label comes off. Empty turns both off.
	KunLabel string
	KunGroup string
	// RunAware stops issue-comment wakes while the assignee's run is alive;
	// a run that stays silent in the issue for SilentInterval gets one
	// project-group reminder per interval instead.
	RunAware       bool
	SilentInterval time.Duration
	// Review wakes, each off by default. HoldWake wakes the developer when a
	// hold does not @mention them, at once and again after HoldWait with no
	// run. ReviewWake wakes the reviewer once when a review request has no
	// verdict after ReviewWait. StuckWake routes an in_progress issue with no
	// run for NoRunWait to whoever it waits on. ChildWake wakes the parent's
	// owner when a stalled sub-issue has nobody looking after it.
	HoldWake   bool
	HoldWait   time.Duration
	ReviewWake bool
	ReviewWait time.Duration
	StuckWake  bool
	NoRunWait  time.Duration
	ChildWake  bool
	ChildWait  time.Duration
	// RoundStop, off by default: once RoundLimit review rounds in a row end in
	// hold, the watchdog stops waking anyone on the issue and lays the case out
	// as options for Kun, until the developer asks for another review or a
	// pass lands.
	RoundStop  bool
	RoundLimit int
	// StopRouting, off by default, routes each ended run of an issue's
	// assignee by where it left the issue (run_end.go), and calls the
	// responsible manager about a run whose runtime has been offline for
	// OfflineWait.
	StopRouting bool
	OfflineWait time.Duration
}

func ConfigFromEnv() (Config, error) {
	cfg := Config{TickInterval: time.Minute, NoRunWait: 30 * time.Minute, KunLabel: defaultKunLabel,
		HeartbeatFile: envOr("MULTICA_ISSUE_WATCHDOG_HEARTBEAT_FILE", defaultHeartbeatFile),
		WorkspaceID:   strings.TrimSpace(os.Getenv("MULTICA_ISSUE_WATCHDOG_WORKSPACE_ID")),
		OwnersFile:    strings.TrimSpace(os.Getenv("MULTICA_ISSUE_WATCHDOG_OWNERS_FILE")),
		AppURL:        strings.TrimRight(strings.TrimSpace(envOr("MULTICA_APP_URL", os.Getenv("FRONTEND_ORIGIN"))), "/")}
	var err error
	if value := strings.TrimSpace(os.Getenv("MULTICA_ISSUE_WATCHDOG_ENABLED")); value != "" {
		cfg.Enabled, err = strconv.ParseBool(value)
		if err != nil {
			return Config{}, errors.New("MULTICA_ISSUE_WATCHDOG_ENABLED must be true or false")
		}
	}
	if value := strings.TrimSpace(os.Getenv("MULTICA_ISSUE_WATCHDOG_TICK")); value != "" {
		cfg.TickInterval, err = time.ParseDuration(value)
		if err != nil || cfg.TickInterval <= 0 {
			return Config{}, errors.New("MULTICA_ISSUE_WATCHDOG_TICK must be a positive duration")
		}
	}
	if cfg.Enabled {
		if _, err = uuid.Parse(cfg.WorkspaceID); err != nil {
			return Config{}, errors.New("MULTICA_ISSUE_WATCHDOG_WORKSPACE_ID must be a UUID when enabled")
		}
		if cfg.OwnersFile == "" {
			return Config{}, errors.New("MULTICA_ISSUE_WATCHDOG_OWNERS_FILE must point to mono watch-lists.json")
		}
		if cfg.Owners, err = ReadOwners(cfg.OwnersFile); err != nil {
			return Config{}, err
		}
	}
	// Legacy escalation/wake/timeouts are intentionally not read. They cannot
	// reactivate the retired chain, even in an old deployment environment.
	return cfg, nil
}

// projectGroups is the project table of the office rules (第二大脑.md
// 「规矩」, 2026-10-24, DENE-2587), keyed by project title. A project not
// listed has no group (replyGroupNone).
var projectGroups = map[string]string{
	"Multica 魔改":     "Multica 办公室",
	"game-multica":   "Multica 办公室",
	"Agent mono仓库管理": "Agent mono",
	"出海独立站":          "出海独立站/新词站",
	"新词雷达":           "新词雷达",
	"tarot":          "塔罗站",
	"自媒体":            "自媒体",
	"消息汇总":           "消息汇总",
	"game":           "游戏",
	"game-relay":     "game relay",
	"RelayVPS":       "game relay",
	"硕士论文":           "硕士论文",
	"AI Rap Video":   "AI Rap Video",
}

// lookupEnvOr is the trimmed value of name, or fallback only when it is
// unset, so an explicit empty value turns its feature off.
func lookupEnvOr(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

// envOr is the trimmed value of name, or fallback when it is unset or blank.
func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

type NudgeFunc func(context.Context, string, string, string, string) error

type QueryStore interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type Store interface {
	QueryStore
	Begin(context.Context) (pgx.Tx, error)
}

type Watchdog struct {
	pool   Store
	cfg    Config
	nudge  NudgeFunc
	wake   WakeFunc
	note   NoteFunc
	label  LabelFunc
	client *http.Client
	now    func() time.Time
}

func New(pool Store, cfg Config, nudge NudgeFunc) *Watchdog {
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
	ParentID        string
	Reviewer        actor
	WorkspaceID     string
	WorkspaceSlug   string
	Number          int32
	Identifier      string
	Title           string
	Status          string
	AssigneeType    string
	AssigneeID      string
	EmployeeName    string
	ManagerName     string
	ProjectID       string
	WaitingKun      bool
	Cause           string
	ProjectName     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FirstExecutedAt *time.Time
	ProgressAt      time.Time
	IssueRevision   int64
	// KunLabel: the issue carries Config.KunLabel and waits on Kun.
	KunLabel bool
	// ReplyGroup is the issue's own reply_group metadata, trimmed.
	ReplyGroup string
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
	// Set only on project-group reminders for a live run.
	TaskID        string `json:"task_id,omitempty"`
	SilentMinutes int    `json:"silent_minutes,omitempty"`
	// CurrentCommand is the command the run has in flight, from its live
	// report; omitted when it runs none or reports no live status.
	CurrentCommand string `json:"current_command,omitempty"`
	// Group is the chat group to post to (kunGroup); set only on
	// kun_decision events.
	Group string `json:"group,omitempty"`
}

// activeRun is the assignee's live run on an issue and the last time the
// assignee reported in the issue during or after its start.
type activeRun struct {
	TaskID       string
	LastReportAt time.Time
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

// Events do not independently wake employees, reviewers or COO. The periodic
// scan is the only stall decision path; normal dispatch and mentions are separate.
func (w *Watchdog) HandleEvent(e events.Event) {}

// statusChangeReason is the manager alert for a status change. An issue that
// enters review with no reviewer waits on the manager to accept it.
func statusChangeReason(issue Issue) string {
	if issue.Status == "in_review" && issue.Reviewer.ID == "" {
		return "负责人状态变更为 in_review，单子没填审查员，请负责经理验收，通过就关单"
	}
	return "负责人状态变更为 " + issue.Status
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
	return w.loadIssueUsing(ctx, w.pool, workspaceID, issueID)
}
func (w *Watchdog) loadIssueUsing(ctx context.Context, store QueryStore, workspaceID, issueID string) (Issue, error) {
	var out Issue
	var firstExecutedAt pgtype.Timestamptz
	err := store.QueryRow(ctx, `
SELECT i.id::text, i.workspace_id::text, i.number, w.issue_prefix || '-' || i.number::text,
       i.title, i.status, COALESCE(i.assignee_type, ''), COALESCE(i.assignee_id::text, ''),
       COALESCE(a.name, u.name, ''), COALESCE(NULLIF(btrim(i.metadata->>'manager_bot'), ''), manager_option.value->>'name', ''),
       COALESCE(p.title, ''), i.created_at, i.updated_at, i.first_executed_at,
       GREATEST(
         i.created_at, i.updated_at,
         COALESCE((SELECT max(GREATEST(COALESCE(t.completed_at,t.started_at,t.dispatched_at,t.created_at),
                     CASE WHEN t.status=ANY($6) AND rt.status='offline'
                          THEN GREATEST(COALESCE(rt.last_seen_at,rt.updated_at),rt.updated_at)
                          ELSE t.created_at END))
                   FROM agent_task_queue t LEFT JOIN agent_runtime rt ON rt.id=t.runtime_id
                   WHERE t.issue_id = i.id), i.created_at),
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
       i.revision, COALESCE(i.parent_issue_id::text, ''),
       COALESCE(i.reviewer_type, ''), COALESCE(i.reviewer_id::text, ''),
       $4 <> '' AND EXISTS (
         SELECT 1 FROM issue_to_label il
         JOIN issue_label l ON l.id = il.label_id
         WHERE il.issue_id = i.id AND l.workspace_id = i.workspace_id
           AND l.resource_type = 'issue' AND lower(l.name) = lower($4)
       ) AS kun_label,
       COALESCE(btrim(i.metadata->>$5::text), ''), COALESCE(i.project_id::text, ''), w.slug,
       COALESCE(i.metadata->>'close.block_kind', i.metadata->>'block_kind', '') IN ('decision','permission') OR
       COALESCE(i.metadata->>'waiting_for_kun', '') = 'true',
       COALESCE((SELECT CASE WHEN t.status=ANY($6) AND rt.status='offline' THEN 'runtime_offline'
                            ELSE t.failure_reason END
                 FROM agent_task_queue t LEFT JOIN agent_runtime rt ON rt.id=t.runtime_id WHERE t.issue_id = i.id
                 ORDER BY t.created_at DESC, t.id DESC LIMIT 1), '')
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
		workspaceID, issueID, watchdogCommentMarker, w.cfg.KunLabel, replyGroupKey, activeTaskStatuses,
	).Scan(&out.ID, &out.WorkspaceID, &out.Number, &out.Identifier, &out.Title, &out.Status,
		&out.AssigneeType, &out.AssigneeID, &out.EmployeeName, &out.ManagerName, &out.ProjectName,
		&out.CreatedAt, &out.UpdatedAt, &firstExecutedAt, &out.ProgressAt, &out.IssueRevision,
		&out.ParentID, &out.Reviewer.Type, &out.Reviewer.ID, &out.KunLabel, &out.ReplyGroup, &out.ProjectID, &out.WorkspaceSlug, &out.WaitingKun, &out.Cause)
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
  AND status IN ('in_progress', 'in_review', 'blocked')
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

	// Status reminders first: an issue just delivered or blocked should not
	// wait behind the stall scan. The event path normally got there already.
	entered, err := w.recentStatusEntries(ctx, now)
	if err != nil {
		return fmt.Errorf("list issue watchdog status entries")
	}
	for _, id := range entered {
		if err := ctx.Err(); err != nil {
			return err
		}
		issue, err := w.loadIssue(ctx, w.cfg.WorkspaceID, id)
		if err != nil {
			continue
		}
		if err := w.enqueueStatusWake(ctx, issue, now); err != nil {
			slog.WarnContext(ctx, "issue watchdog status reminder failed", "issue_id", issue.ID)
		}
	}

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

	return nil
}

func (w *Watchdog) checkIssue(ctx context.Context, issue Issue, now time.Time) error {
	if !stallEligible(issue) || now.Sub(issue.ProgressAt) < 30*time.Minute {
		return nil
	}
	active, err := w.hasAnyRun(ctx, issue)
	if err != nil || active {
		return err
	}
	// One owner wake per entry of in_review / blocked: the status reminder,
	// or this fallback when that one provably never reached the owner.
	var entry statusEntry
	if WakesOnStatus(issue.Status) {
		if entry, err = currentStatusEntry(ctx, w.pool, issue); err != nil {
			return err
		}
		woken, err := entryAlreadyWoken(ctx, w.pool, issue, entry)
		if err != nil || woken {
			return err
		}
	}
	manager, registration := w.cfg.Owners.Resolve(issue.ManagerName, issue.ProjectID)
	key := fmt.Sprintf("owner-stall:%d", issue.ProgressAt.UnixMicro())
	event := Reminder{
		Kind: reminderKindStall, EventID: issue.ID + ":" + key, IssueID: issue.ID, IssueNumber: issue.Identifier,
		Title: issue.Title, Status: issue.Status, Manager: manager, ProjectName: issue.ProjectName,
		Cause: stallCause(issue.Cause), StallAt: issue.ProgressAt, OccurredAt: now,
		RegistrationNeeded: registration, StatusEntry: entry.EventID,
	}
	event.Reason = "无运行且无进展已满 30 分钟，请负责人在原单安排后续。"
	if registration {
		event.Reason += " 项目归属缺失，请扫帚补登记。"
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = w.pool.Exec(ctx, `
INSERT INTO office_issue_watchdog_attempt
(workspace_id, issue_id, event_key, event_type, target, manager_name, payload)
VALUES ($1::uuid,$2::uuid,$3,$4,'manager',$5,$6::jsonb)
ON CONFLICT (workspace_id,issue_id,event_key) DO NOTHING`, issue.WorkspaceID, issue.ID, key, ownerStalledEvent, manager, payload)
	return err
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

const (
	inReviewOverdue = "in_review_overdue"
	todoOverdue     = "todo_overdue"
)

// remindManagerIdle alerts the responsible manager, once per stall, when an
// issue only the manager moves on has had no progress and no run for wait:
// an in_review issue, or a started issue put back in todo. The ordinary
// manager wait and COO escalation follow.
func (w *Watchdog) remindManagerIdle(ctx context.Context, issue Issue, state issueState, kind string, wait time.Duration, now time.Time) error {
	if wait <= 0 {
		return nil
	}
	runs, err := w.loadRuns(ctx, issue.ID)
	if err != nil || runs.Active > 0 {
		return err
	}
	anchor := later(state.LastProgressAt, runs.LastActivityAt)
	idle := now.Sub(anchor)
	if idle < wait {
		return nil
	}
	key := fmt.Sprintf("%s:%s:%d", kind, issue.ID, anchor.UnixNano())
	return w.notifyManager(ctx, issue, kind, key, w.managerIdleReason(kind, idle), "manager", now)
}

func (w *Watchdog) managerIdleReason(kind string, idle time.Duration) string {
	minutes := int(idle.Minutes())
	reason := fmt.Sprintf("单子待审已 %d 分钟没有动静。请验收，通过就关单", minutes)
	if kind == todoOverdue {
		reason = fmt.Sprintf("单子开工后放回了待办，已 %d 分钟没有动静。请决定恢复还是继续暂停", minutes)
	}
	if w.cfg.KunLabel != "" {
		reason += "；要 Kun 看或拍板，就给单子打上「" + w.cfg.KunLabel + "」标签"
	}
	return reason
}

// checkKunDecision pushes an issue to its chat group (kunGroup) once each time
// it takes the Kun-decision label. A push stays open, and blocks the next one,
// until a scan or a label event sees the label gone; the push's
// manager_resolved_at records that, and the idle clock restarts so Kun's
// answer gets fresh time.
func (w *Watchdog) checkKunDecision(ctx context.Context, issue Issue, now time.Time) error {
	if w.cfg.KunLabel == "" {
		return nil
	}
	var (
		pushes int
		open   int64
	)
	if err := w.pool.QueryRow(ctx, `
SELECT count(*), COALESCE(max(id) FILTER (WHERE manager_resolved_at IS NULL), 0)
FROM office_issue_watchdog_attempt
WHERE workspace_id = $1::uuid AND issue_id = $2::uuid AND event_type = $3`,
		issue.WorkspaceID, issue.ID, KunDecisionEventType).Scan(&pushes, &open); err != nil {
		return err
	}
	if !issue.KunLabel {
		if open == 0 {
			return nil
		}
		if _, err := w.pool.Exec(ctx, `
UPDATE office_issue_watchdog_attempt SET manager_resolved_at = $2
WHERE id = $1 AND manager_resolved_at IS NULL`, open, now); err != nil {
			return err
		}
		w.deferIdleWake(ctx, issue.ID, now)
		return nil
	}
	if open != 0 || issue.Status == "done" || issue.Status == "cancelled" {
		return nil
	}
	key := fmt.Sprintf("kun-decision:%s:%d", issue.ID, pushes+1)
	id, claimed, err := w.claim(ctx, issue, key, KunDecisionEventType, officeGroupTarget, issue.ManagerName, nil)
	if err != nil || !claimed {
		return err
	}
	group, route := w.kunGroup(ctx, issue)
	err = w.post(ctx, webhookEvent{
		EventID: key, EventType: KunDecisionEventType, IssueID: issue.ID,
		IssueNumber: issue.Identifier, Title: issue.Title, Status: issue.Status,
		Employee: issue.EmployeeName, Manager: issue.ManagerName, ProjectName: issue.ProjectName,
		WakeTarget: officeGroupTarget, Group: group,
		Reason:     fmt.Sprintf("单子打上了「%s」标签，%s，请推到「%s」群请 Kun 拍板", w.cfg.KunLabel, route, group),
		OccurredAt: now.UTC(),
	})
	w.finishAttempt(ctx, id, err == nil)
	if err != nil {
		return errors.New("issue watchdog Kun-decision push failed")
	}
	return nil
}

// kunGroup is the chat group an issue waiting on Kun is pushed to, and how it
// was found: the issue's 「回哪个群」; for 随上级单, the nearest ancestor that
// names a group, skipping parents with none or 随上级单; with none, the project
// table (no project is 杂项), for 随上级单 by the direct parent's project. Kun
// still has to see an issue whose group is 不发群, so it goes to
// Config.KunGroup. A failed parent read ends the walk with no group found.
func (w *Watchdog) kunGroup(ctx context.Context, issue Issue) (string, string) {
	group, project, parentID := issue.ReplyGroup, issue.ProjectName, issue.ParentID
	source := "单上的「回哪个群」"
	if group == replyGroupFollowParent {
		group = ""
		for hops := 0; group == "" && parentID != "" && hops < maxReplyGroupHops; hops++ {
			var nextGroup, nextParent, nextProject string
			if err := w.pool.QueryRow(ctx, `
SELECT COALESCE(btrim(i.metadata->>$3::text), ''), COALESCE(i.parent_issue_id::text, ''), COALESCE(p.title, '')
FROM issue i
LEFT JOIN project p ON p.id = i.project_id AND p.workspace_id = i.workspace_id
WHERE i.workspace_id = $1::uuid AND i.id = $2::uuid`,
				issue.WorkspaceID, parentID, replyGroupKey).Scan(&nextGroup, &nextParent, &nextProject); err != nil {
				slog.WarnContext(ctx, "issue watchdog: read parent reply group failed", "issue_id", issue.ID, "parent_id", parentID)
				break
			}
			if hops == 0 {
				project = nextProject
			}
			if nextGroup != replyGroupFollowParent {
				group = nextGroup
			}
			parentID = nextParent
		}
		if group != "" {
			source = "上级单的「回哪个群」"
		}
	}
	if group == "" {
		source = "项目对照表"
		switch {
		case project == "":
			group = unprojectedGroup
		case projectGroups[project] != "":
			group = projectGroups[project]
		default:
			group = replyGroupNone
		}
	}
	if group == replyGroupNone {
		return w.cfg.KunGroup, source + "是「" + replyGroupNone + "」"
	}
	return group, "按" + source
}

// loadActiveRun finds the assignee agent's running task on the issue. A
// report is the assignee's own substantive comment, its latest non-empty line
// in the progress history (the issue's summary is overwritten by whoever
// writes next, so it can hide the assignee's report), or any status change;
// the run's start is the floor, so a fresh run is never silent.
func (w *Watchdog) loadActiveRun(ctx context.Context, issue Issue) (activeRun, bool, error) {
	var run activeRun
	err := w.pool.QueryRow(ctx, `
SELECT t.id::text,
       GREATEST(
         COALESCE(t.started_at, t.dispatched_at, t.created_at),
         COALESCE((SELECT max(c.created_at)
                   FROM comment c
                   WHERE c.workspace_id = i.workspace_id AND c.issue_id = i.id
                     AND c.author_type = 'agent' AND c.author_id = t.agent_id AND c.type = 'comment'
                     AND regexp_replace(lower(trim(c.content)),
                         '[[:space:][:punct:]，。！？、；：…]+', '', 'g') <> '收到'), '-infinity'::timestamptz),
         COALESCE((SELECT max(p.created_at)
                   FROM issue_progress p
                   WHERE p.workspace_id = i.workspace_id AND p.issue_id = i.id
                     AND p.author_type = 'agent' AND p.author_id = t.agent_id
                     AND btrim(p.text) <> ''), '-infinity'::timestamptz),
         COALESCE((SELECT max(e.occurred_at)
                   FROM office_event e
                   WHERE e.workspace_id = i.workspace_id AND e.issue_id = i.id
                     AND e.event_type = 'issue.status_changed'), '-infinity'::timestamptz)
       )
FROM agent_task_queue t
JOIN issue i ON i.id = t.issue_id
WHERE i.workspace_id = $1::uuid AND t.issue_id = $2::uuid
  AND t.agent_id = $3::uuid AND t.status = 'running'
ORDER BY t.started_at DESC NULLS LAST, t.id DESC
LIMIT 1`, issue.WorkspaceID, issue.ID, issue.AssigneeID).Scan(&run.TaskID, &run.LastReportAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return activeRun{}, false, nil
	}
	if err != nil {
		return activeRun{}, false, err
	}
	return run, true, nil
}

// remindSilentRun never comments on the issue: a comment would be steered
// into the live run and interrupt it. A run silent for SilentInterval gets a
// project-group reminder, and one run gets at most one reminder per interval
// whether or not it reported in between. The key numbers the run's reminders,
// so two scanners racing on the same run claim the same key.
func (w *Watchdog) remindSilentRun(ctx context.Context, issue Issue, run activeRun, now time.Time) error {
	silentFor := now.Sub(run.LastReportAt)
	if silentFor < w.cfg.SilentInterval {
		return nil
	}
	prefix := "running-silent:" + run.TaskID + ":"
	var (
		sent   int
		lastAt pgtype.Timestamptz
	)
	if err := w.pool.QueryRow(ctx, `
SELECT count(*), max(attempted_at)
FROM office_issue_watchdog_attempt
WHERE workspace_id = $1::uuid AND issue_id = $2::uuid
  AND event_type = $3 AND starts_with(event_key, $4)`,
		issue.WorkspaceID, issue.ID, RunningSilentEventType, prefix).Scan(&sent, &lastAt); err != nil {
		return err
	}
	if lastAt.Valid && now.Sub(lastAt.Time) < w.cfg.SilentInterval {
		return nil
	}
	// attempted_at is the scan's clock, not the database's, because the
	// interval guard above compares it against that clock.
	key := prefix + strconv.Itoa(sent+1)
	var id int64
	err := w.pool.QueryRow(ctx, `
INSERT INTO office_issue_watchdog_attempt
    (workspace_id, issue_id, event_key, event_type, target, manager_name, attempted_at)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)
ON CONFLICT (workspace_id, issue_id, event_key) DO NOTHING
RETURNING id`, issue.WorkspaceID, issue.ID, key,
		RunningSilentEventType, projectGroupTarget, issue.ManagerName, now).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	minutes := int(silentFor.Minutes())
	reason := fmt.Sprintf("负责员工正在跑，但已 %d 分钟没在单子里汇报", minutes)
	command := w.runCommand(ctx, run.TaskID, now)
	if command != "" {
		reason += "；当前命令：" + clipRunes(command, reasonCommandMaxRunes)
	}
	err = w.post(ctx, webhookEvent{
		EventID: key, EventType: RunningSilentEventType, IssueID: issue.ID,
		IssueNumber: issue.Identifier, Title: issue.Title, Status: issue.Status,
		Employee: issue.EmployeeName, Manager: issue.ManagerName, ProjectName: issue.ProjectName,
		WakeTarget: projectGroupTarget,
		Reason:     reason,
		OccurredAt: now.UTC(), TaskID: run.TaskID, SilentMinutes: minutes,
		CurrentCommand: command,
	})
	w.finishAttempt(ctx, id, err == nil)
	if err != nil {
		return errors.New("issue watchdog project-group reminder failed")
	}
	return nil
}

// runCommand returns the command the run's daemon last reported in flight,
// already redacted by the daemon and the server. It is empty when the run
// runs no command, its daemon reports no live status, or the report is
// older than liveCommandFreshness at the scan's clock. A failed read only
// leaves the command out of the reminder.
func (w *Watchdog) runCommand(ctx context.Context, taskID string, now time.Time) string {
	var command string
	err := w.pool.QueryRow(ctx, `
SELECT command FROM task_live_status
WHERE task_id = $1::uuid AND tools_in_flight > 0 AND reported_at >= $2::timestamptz`,
		taskID, now.Add(-liveCommandFreshness)).Scan(&command)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Debug("issue watchdog: read run command failed", "task_id", taskID, "error", err)
		}
		return ""
	}
	return command
}

// clipRunes keeps at most n runes of s, marking a cut with an ellipsis.
func clipRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
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

// managerSignatures lists the prefixes that sign a comment as manager. A
// manager signs with the name after the last 「·」 of its "负责经理" option
// (CTO·扳手 signs 【负责经理·扳手】, 基建管家·扫帚 signs 【负责经理·扫帚】);
// COO·Grok signs with its full name.
func managerSignatures(manager string) []string {
	manager = strings.TrimSpace(manager)
	alias := manager
	if i := strings.LastIndex(manager, "·"); i >= 0 && manager != "COO·Grok" {
		alias = manager[i+len("·"):]
	}
	if alias == "" {
		return nil
	}
	out := make([]string, 0, 9)
	for _, prefix := range []string{"负责经理·" + alias, "负责经理" + alias} {
		out = append(out, "【"+prefix+"】", "["+prefix+"]", prefix)
	}
	if manager == "COO·Grok" {
		out = append(out, "【COO·Grok】", "[COO·Grok]", "COO·Grok")
	}
	return out
}
