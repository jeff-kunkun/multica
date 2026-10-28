package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// MetaKeyBacklogWaitingFor is the one line saying what a backlog ticket waits
// for (DENE-1638). Leaving backlog drops it.
const MetaKeyBacklogWaitingFor = "backlog.waiting_for"

// MetaKeyBacklogAfter names, as comma-separated ids, the sibling sub-issues a
// parked sub-issue starts after (DENE-2671). The fact lives on the ticket that
// waits, like its waiting line, rather than as a dependency edge between two
// tickets. StartReadyDependents drops it once the started issue's run is
// queued; a person moving the issue out of backlog drops it too.
const MetaKeyBacklogAfter = "backlog.after"

// activitySourceAfter marks the status change StartReadyDependents makes, so
// the timeline can say the ticket started because the one before it finished.
const activitySourceAfter = "after"

func afterValue(ids []pgtype.UUID) []byte {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = util.UUIDToString(id)
	}
	raw, _ := json.Marshal(strings.Join(parts, ","))
	return raw
}

// issues is the issue service over the same task service, for the sub-issue
// event processing that runs from both the handler and the scheduler sweep.
func (s *IssueWakeupService) issues() *IssueService {
	t := s.Tasks
	return &IssueService{Queries: t.Queries, TxStarter: t.TxStarter, Bus: t.Bus, Analytics: t.Analytics, Metrics: t.Metrics, TaskService: t, Entitlements: t.Entitlements}
}

// StartReadyDependents starts sub-issues with an order (DENE-2671) once every
// sibling they name is done. It looks at the ones ordered after
// predecessorIDs, which just closed, and at dependentIDs themselves, which
// were just attached: a sibling that closed while one was being created had
// no dependent to find, so the new sub-issue's own event checks it. It moves each from
// backlog to todo in one transaction, then queues its assignee's run and only
// then drops backlog.after. A run that could not be queued leaves the key on
// the todo issue and fails the call, so the sub-issue event stays unprocessed
// and the sweep calls again: the second call finds the issue already in todo
// and only queues the run. Each dependent is locked and re-read, so the status
// moves once. A run that was queued while dropping the key failed is not
// queued again: the retry finds its task row and only drops the key.
func (s *IssueService) StartReadyDependents(ctx context.Context, predecessorIDs, dependentIDs []pgtype.UUID) ([]db.Issue, error) {
	if len(predecessorIDs) == 0 && len(dependentIDs) == 0 {
		return nil, nil
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	ready, err := qtx.LockAfterDependents(ctx, db.LockAfterDependentsParams{PredecessorIds: nonNilUUIDs(predecessorIDs), DependentIds: nonNilUUIDs(dependentIDs)})
	if err != nil || len(ready) == 0 {
		return nil, err
	}
	owed := make([]db.Issue, 0, len(ready))
	started := map[int]wakeupActivity{}
	details, _ := json.Marshal(map[string]any{"from": issuestatus.Backlog, "to": issuestatus.Todo, "source": activitySourceAfter})
	for _, dependent := range ready {
		if dependent.Status != issuestatus.Backlog {
			owed = append(owed, dependent)
			continue
		}
		issue, err := qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: dependent.ID, WorkspaceID: dependent.WorkspaceID, Status: issuestatus.Todo})
		if err != nil {
			return nil, fmt.Errorf("start dependent: %w", err)
		}
		row, err := qtx.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: MetaKeyBacklogWaitingFor})
		switch {
		case err == nil:
			issue.Metadata, issue.Revision = row.Metadata, row.Revision
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, fmt.Errorf("drop waiting line: %w", err)
		}
		activity, err := qtx.CreateActivity(ctx, db.CreateActivityParams{
			ID: dbid.NewV7(), WorkspaceID: issue.WorkspaceID, IssueID: issue.ID,
			ActorType: util.StrToText("system"), Action: "status_changed", Details: details,
		})
		if err != nil {
			return nil, fmt.Errorf("record start: %w", err)
		}
		started[len(owed)] = wakeupActivity{row: activity, actorType: "system", details: details}
		owed = append(owed, issue)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	var errs []error
	for i, issue := range owed {
		activity, moved := started[i]
		if err := s.queueAfterRun(ctx, issue, !moved); err != nil {
			errs = append(errs, fmt.Errorf("queue run for %s: %w", util.UUIDToString(issue.ID), err))
		} else {
			row, err := s.Queries.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: MetaKeyBacklogAfter})
			switch {
			case err == nil:
				owed[i].Metadata, owed[i].Revision = row.Metadata, row.Revision
			case !errors.Is(err, pgx.ErrNoRows):
				errs = append(errs, fmt.Errorf("drop order of %s: %w", util.UUIDToString(issue.ID), err))
			}
		}
		if !moved {
			continue
		}
		slog.Info("dependent sub-issue started after its predecessors finished", "issue_id", util.UUIDToString(issue.ID))
		if s.TaskService != nil && s.TaskService.Bus != nil {
			s.TaskService.broadcastIssueUpdated(ctx, owed[i], issuestatus.Backlog)
		}
		(&IssueWakeupService{Tasks: s.TaskService}).publishWakeupActivities(activity)
	}
	result := make([]db.Issue, 0, len(started))
	for i := range owed {
		if _, moved := started[i]; moved {
			result = append(result, owed[i])
		}
	}
	return result, errors.Join(errs...)
}

// nonNilUUIDs keeps an empty list an empty array: pgx sends nil as NULL, and
// "= ANY(NULL)" would read as unknown rather than false.
func nonNilUUIDs(ids []pgtype.UUID) []pgtype.UUID {
	if ids == nil {
		return []pgtype.UUID{}
	}
	return ids
}

// queueAfterRun queues the run a started dependent's assignee owes, the way
// an assignment would. It returns an error only when trying again can help.
// An assignee that cannot take work, and a refusal that would refuse again (a
// chain budget, the bot-wake limit, attribution failing closed), end the duty
// as they do for an assignment; a run already pending counts as queued. For
// an issue an earlier call already started (retry), a run queued since that
// start counts as queued too, even once it has finished, so dropping the key
// failing after the queue cannot queue a second run.
func (s *IssueService) queueAfterRun(ctx context.Context, issue db.Issue, retry bool) error {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return nil
	}
	var err error
	switch issue.AssigneeType.String {
	case "agent":
		verdict, admitted := agentAssigneeVerdict(ctx, s.runtimeLookup(s.Queries), issue)
		if !admitted {
			if RuntimeBlockedNeedsNotice(verdict.Reason) {
				s.noteRuntimeUnusable(ctx, issue, verdict)
			}
			return nil
		}
		if queued, checkErr := s.runQueuedSinceStart(ctx, issue, issue.AssigneeID, retry); checkErr != nil || queued {
			return checkErr
		}
		_, err = s.TaskService.EnqueueTaskForIssue(ctx, issue)
	case "squad":
		if !s.isSquadLeaderReady(ctx, issue) {
			return nil
		}
		squad, lookupErr := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: issue.AssigneeID, WorkspaceID: issue.WorkspaceID})
		if lookupErr != nil {
			return lookupErr
		}
		if queued, checkErr := s.runQueuedSinceStart(ctx, issue, squad.LeaderID, retry); checkErr != nil || queued {
			return checkErr
		}
		_, err = s.TaskService.EnqueueTaskForSquadLeader(ctx, issue, squad.LeaderID, squad.ID, pgtype.UUID{}, OriginDerived)
	default:
		return nil
	}
	switch {
	case err == nil, errors.Is(err, ErrDuplicatePendingTask):
		return nil
	case errors.Is(err, ErrAgentChainBudgetExceeded), errors.Is(err, ErrBotWakeChainLimit), errors.Is(err, ErrAttributionFailClosed):
		slog.Warn("dependent sub-issue started but its run was refused", "issue_id", util.UUIDToString(issue.ID), "error", err)
		return nil
	}
	return err
}

// runQueuedSinceStart reports, for a retry, whether agentID already has a run
// for the issue queued since its start. A first call has nothing to find.
func (s *IssueService) runQueuedSinceStart(ctx context.Context, issue db.Issue, agentID pgtype.UUID, retry bool) (bool, error) {
	if !retry {
		return false, nil
	}
	return s.Queries.HasAfterRunSinceStart(ctx, db.HasAfterRunSinceStartParams{IssueID: issue.ID, AgentID: agentID})
}
