package issuewatchdog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	watchdogCommentMarker = "<!-- office-issue-watchdog -->"
	defaultHeartbeatFile  = "/var/lib/multica/issue-watchdog/last-run"
	defaultKunLabel       = "等 Kun 拍板"
)

// Config is the one owner-reminder chain (DENE-2981): an employee's progress
// on an issue (a new comment, delivery, a block, done), 10 quiet minutes in
// progress, or a stall
// wakes the issue's responsible owner through the outbox, and the owner says
// one line in the project group. The retired review, hold, run-end, silent-run
// and COO escalation wakes are gone, and their old variables are not read.
type Config struct {
	Owners     OwnerTable
	OwnersFile string
	// AppURL is the web origin (MULTICA_APP_URL, else FRONTEND_ORIGIN) used
	// for the issue link in owner reminders; empty leaves the link out.
	AppURL string

	Enabled       bool
	WorkspaceID   string
	TickInterval  time.Duration
	HeartbeatFile string
	// An issue carrying the KunLabel label waits on Kun and wakes nobody.
	KunLabel string
}

func ConfigFromEnv() (Config, error) {
	cfg := Config{TickInterval: time.Minute, KunLabel: defaultKunLabel,
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
	return cfg, nil
}

// envOr is the trimmed value of name, or fallback when it is unset or blank.
func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

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
	pool Store
	cfg  Config
	now  func() time.Time
}

func New(pool Store, cfg Config) *Watchdog {
	return &Watchdog{pool: pool, cfg: cfg, now: time.Now}
}

type Issue struct {
	ID            string
	WorkspaceID   string
	WorkspaceSlug string
	Number        int32
	Identifier    string
	Title         string
	Status        string
	AssigneeType  string
	AssigneeID    string
	EmployeeName  string
	ManagerName   string
	ProjectID     string
	WaitingKun    bool
	Cause         string
	ProjectName   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ProgressAt    time.Time
	// KunLabel: the issue carries Config.KunLabel and waits on Kun.
	KunLabel bool
}

func (w *Watchdog) loadIssue(ctx context.Context, workspaceID, issueID string) (Issue, error) {
	return w.loadIssueUsing(ctx, w.pool, workspaceID, issueID)
}
func (w *Watchdog) loadIssueUsing(ctx context.Context, store QueryStore, workspaceID, issueID string) (Issue, error) {
	var out Issue
	err := store.QueryRow(ctx, `
SELECT i.id::text, i.workspace_id::text, i.number, w.issue_prefix || '-' || i.number::text,
       i.title, i.status, COALESCE(i.assignee_type, ''), COALESCE(i.assignee_id::text, ''),
       COALESCE(a.name, u.name, ''), COALESCE(NULLIF(btrim(i.metadata->>'manager_bot'), ''), manager_option.value->>'name', ''),
       COALESCE(p.title, ''), i.created_at, i.updated_at,
       GREATEST(
         i.created_at, i.updated_at,
         COALESCE((SELECT max(GREATEST(COALESCE(t.completed_at,t.started_at,t.dispatched_at,t.created_at),
                     CASE WHEN t.status=ANY($5) AND rt.status='offline'
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
       $4 <> '' AND EXISTS (
         SELECT 1 FROM issue_to_label il
         JOIN issue_label l ON l.id = il.label_id
         WHERE il.issue_id = i.id AND l.workspace_id = i.workspace_id
           AND l.resource_type = 'issue' AND lower(l.name) = lower($4)
       ) AS kun_label,
       COALESCE(i.project_id::text, ''), w.slug,
       COALESCE(i.metadata->>'close.block_kind', i.metadata->>'block_kind', '') IN ('decision','permission') OR
       COALESCE(i.metadata->>'waiting_for_kun', '') = 'true',
       COALESCE((SELECT CASE WHEN t.status=ANY($5) AND rt.status='offline' THEN 'runtime_offline'
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
		workspaceID, issueID, watchdogCommentMarker, w.cfg.KunLabel, activeTaskStatuses,
	).Scan(&out.ID, &out.WorkspaceID, &out.Number, &out.Identifier, &out.Title, &out.Status,
		&out.AssigneeType, &out.AssigneeID, &out.EmployeeName, &out.ManagerName, &out.ProjectName,
		&out.CreatedAt, &out.UpdatedAt, &out.ProgressAt,
		&out.KunLabel, &out.ProjectID, &out.WorkspaceSlug, &out.WaitingKun, &out.Cause)
	if err != nil {
		return Issue{}, err
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
		if err := w.checkProgress(ctx, issue, now); err != nil {
			slog.WarnContext(ctx, "issue watchdog progress reminder failed", "issue_id", issue.ID)
		}
		if err := w.checkIssue(ctx, issue, now); err != nil {
			slog.WarnContext(ctx, "issue watchdog scan item failed", "issue_id", issue.ID)
		}
	}

	return nil
}

// stallDue: 30 minutes with neither progress nor a live or queued run.
func (w *Watchdog) stallDue(ctx context.Context, issue Issue, now time.Time) (bool, error) {
	if !stallEligible(issue) || now.Sub(issue.ProgressAt) < 30*time.Minute {
		return false, nil
	}
	active, err := w.hasAnyRun(ctx, issue)
	return err == nil && !active, err
}

func (w *Watchdog) checkIssue(ctx context.Context, issue Issue, now time.Time) error {
	due, err := w.stallDue(ctx, issue, now)
	if err != nil || !due {
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

// clipRunes keeps at most n runes of s, marking a cut with an ellipsis.
func clipRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
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
