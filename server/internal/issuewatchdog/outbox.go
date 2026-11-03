package issuewatchdog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const registrationOwner = "基建管家·扫帚"

// OwnerTable is read directly from mono's public (no credentials) registration.
type OwnerTable struct {
	ProjectManagers map[string]string `json:"project_managers"`
	Unowned         []string          `json:"unowned_project_ids"`
	Managers        []string          `json:"manager_doorbells_enabled_for"`
}

func ReadOwners(path string) (OwnerTable, error) {
	var table OwnerTable
	data, err := os.ReadFile(path)
	if err != nil {
		return table, errors.New("cannot read watchdog owner registration")
	}
	if json.Unmarshal(data, &table) != nil || len(table.ProjectManagers) == 0 {
		return table, errors.New("invalid watchdog owner registration")
	}
	return table, nil
}

func (t OwnerTable) Resolve(explicit, projectID string) (string, bool) {
	if explicit = strings.TrimSpace(explicit); explicit != "" && explicit != "COO·Grok" {
		for _, manager := range append(t.Managers, registrationOwner) {
			if explicit == manager {
				return explicit, false
			}
		}
		for _, manager := range t.ProjectManagers {
			if explicit == manager {
				return explicit, false
			}
		}
		return registrationOwner, true
	}
	for _, id := range t.Unowned {
		if id == projectID {
			return registrationOwner, true
		}
	}
	if manager := strings.TrimSpace(t.ProjectManagers[projectID]); manager != "" && manager != "COO·Grok" {
		return manager, false
	}
	return registrationOwner, true
}

var waitingKunTitle = regexp.MustCompile(`(?i)^\s*(?:[【\[]?\s*(?:待|等)\s*kun|\[paused\]|【暂停】)`)

func stallEligible(issue Issue) bool {
	return (issue.Status == "in_progress" || issue.Status == "in_review" || issue.Status == "blocked") &&
		!issue.KunLabel && !issue.WaitingKun && !waitingKunTitle.MatchString(issue.Title)
}

func stallCause(reason string) string {
	for _, code := range []string{"provider_quota_limit", "runtime_offline", "provider_auth", "provider_unavailable"} {
		if strings.Contains(reason, code) {
			return code
		}
	}
	return "no_live_run"
}

func (w *Watchdog) hasAnyRun(ctx context.Context, issue Issue) (bool, error) {
	return hasAnyRunUsing(ctx, w.pool, issue, w.now())
}
func hasAnyRunUsing(ctx context.Context, store QueryStore, issue Issue, now time.Time) (bool, error) {
	var active bool
	err := store.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM agent_task_queue t LEFT JOIN agent_runtime rt ON rt.id=t.runtime_id
 WHERE t.issue_id=$1::uuid AND t.status=ANY($2)
 AND NOT (COALESCE(rt.status,'online')='offline'
          AND GREATEST(COALESCE(rt.last_seen_at,rt.updated_at),rt.updated_at)<=$3)
 )`, issue.ID, activeTaskStatuses, now.Add(-30*time.Minute)).Scan(&active)
	return active, err
}

type Reminder struct {
	ID                 int64     `json:"id"`
	EventID            string    `json:"event_id"`
	IssueID            string    `json:"issue_id"`
	IssueNumber        string    `json:"issue_number"`
	Title              string    `json:"title"`
	Status             string    `json:"status"`
	Manager            string    `json:"manager"`
	ProjectName        string    `json:"project_name"`
	Cause              string    `json:"cause"`
	Reason             string    `json:"reason"`
	StallAt            time.Time `json:"stall_at"`
	OccurredAt         time.Time `json:"occurred_at"`
	RegistrationNeeded bool      `json:"registration_needed"`
	DeliveryCount      int       `json:"delivery_count"`
	ClaimToken         string    `json:"claim_token"`
}

// Claim reserves a bounded batch for one durable worker request. Repeating the
// token returns the same batch, including after a response was lost. Claims are
// never reassigned on timeout: an unknown delivery must not wake an owner twice.
func (w *Watchdog) Claim(ctx context.Context, token string) ([]Reminder, error) {
	result := []Reminder{}
	if _, err := uuid.Parse(token); err != nil {
		return nil, errors.New("claim_token must be a UUID")
	}
	if !w.cfg.Enabled {
		return result, nil
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "owner-reminder:"+w.cfg.WorkspaceID); err != nil {
		return nil, err
	}

	inserted, err := tx.Exec(ctx, `INSERT INTO office_issue_watchdog_claim(workspace_id,claim_token)
    VALUES($1::uuid,$2::uuid) ON CONFLICT DO NOTHING`, w.cfg.WorkspaceID, token)
	if err != nil {
		return nil, err
	}
	first := inserted.RowsAffected() > 0
	rows, err := tx.Query(ctx, `
 SELECT id, payload, delivery_count, claim_token::text
 FROM office_issue_watchdog_attempt
 WHERE workspace_id=$1::uuid AND event_type='owner_stalled' AND outcome='claimed'
   AND (claim_token=$2::uuid OR ($3 AND (claim_token IS NULL OR delivery_result='not_sent') AND available_at<=now() AND delivery_count<2))
 ORDER BY id LIMIT 50 FOR UPDATE SKIP LOCKED`, w.cfg.WorkspaceID, token, first)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id    int64
		data  []byte
		count int
		token *string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.data, &c.count, &c.token); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, c := range candidates {
		var event Reminder
		if json.Unmarshal(c.data, &event) != nil {
			return nil, errors.New("invalid reminder payload")
		}
		// Re-read current issue and all runs immediately before delivery admission.
		issue, loadErr := w.loadIssueUsing(ctx, tx, w.cfg.WorkspaceID, event.IssueID)
		if loadErr != nil && !errors.Is(loadErr, pgx.ErrNoRows) {
			return nil, loadErr
		}
		eligible := loadErr == nil && stallEligible(issue) && issue.ProgressAt.Equal(event.StallAt)
		if eligible {
			active, runErr := hasAnyRunUsing(ctx, tx, issue, w.now())
			if runErr != nil {
				return nil, runErr
			}
			eligible = !active && w.now().Sub(issue.ProgressAt) >= 30*time.Minute
		}
		if !eligible {
			if _, err = tx.Exec(ctx, `UPDATE office_issue_watchdog_attempt SET outcome='failed',delivery_result='resolved_before_delivery' WHERE id=$1`, c.id); err != nil {
				return nil, err
			}
			continue
		}
		event.Manager, event.RegistrationNeeded = w.cfg.Owners.Resolve(issue.ManagerName, issue.ProjectID)
		event.ID, event.ClaimToken = c.id, token
		event.DeliveryCount = c.count
		if c.token == nil || *c.token != token {
			event.DeliveryCount++
		}
		payload, _ := json.Marshal(event)
		if _, err = tx.Exec(ctx, `UPDATE office_issue_watchdog_attempt
   SET claim_token=$2::uuid,delivery_count=$3,payload=$4::jsonb,manager_name=$5
   WHERE id=$1`, c.id, token, event.DeliveryCount, payload, event.Manager); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// Receipt only retries a proven pre-send failure. Timeouts and crashes remain
// visible as uncertain; replaying those could notify the owner twice.
func (w *Watchdog) Receipt(ctx context.Context, id int64, token, result string) error {
	if _, err := uuid.Parse(token); err != nil {
		return errors.New("claim_token must be a UUID")
	}
	if result != "sent" && result != "not_sent" && result != "uncertain" {
		return errors.New("result must be sent, not_sent or uncertain")
	}
	tag, err := w.pool.Exec(ctx, `UPDATE office_issue_watchdog_attempt
 SET outcome=CASE WHEN $4='sent' THEN 'sent' WHEN $4='not_sent' AND delivery_count<2 THEN 'claimed' ELSE 'failed' END,
     delivery_result=$4,
     claim_token=CASE WHEN $4='not_sent' AND delivery_count<2 THEN NULL ELSE claim_token END,
     available_at=now()+interval '30 seconds', delivered_at=CASE WHEN $4='sent' THEN COALESCE(delivered_at,now()) ELSE delivered_at END
 WHERE workspace_id=$1::uuid AND id=$2 AND event_type='owner_stalled' AND claim_token=$3::uuid
   AND (outcome='claimed' OR delivery_result=$4)`, w.cfg.WorkspaceID, id, token, result)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// A repeated not_sent receipt after its token was released is harmless.
		var previous string
		err = w.pool.QueryRow(ctx, `SELECT delivery_result FROM office_issue_watchdog_attempt
   WHERE workspace_id=$1::uuid AND id=$2 AND event_type='owner_stalled'`, w.cfg.WorkspaceID, id).Scan(&previous)
		if err == nil && previous == result && result == "not_sent" {
			return nil
		}
		return fmt.Errorf("reminder not found or claim no longer matches")
	}
	return nil
}
