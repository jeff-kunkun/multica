package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/chattitle"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// TxStarter abstracts transaction creation (satisfied by pgxpool.Pool).
type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type TaskService struct {
	Queries   *db.Queries
	TxStarter TxStarter
	Hub       *realtime.Hub
	Bus       *events.Bus
	Analytics analytics.Client
	Metrics   *obsmetrics.BusinessMetrics
	Wakeup    TaskWakeupNotifier
	// PRSnapshotsEnabled reports whether the server can refresh GitHub check snapshots.
	PRSnapshotsEnabled bool
	// SourceContextStorage is used only by the bounded 30-day cleanup pass for
	// terminal source-context captures. Nil disables it where storage is absent.
	SourceContextStorage SourceContextObjectStore
	// FeatureFlags is the server-side toggle router. Nil is valid and returns
	// each call site's default.
	FeatureFlags *featureflag.Service
	// EmptyClaim caches "this runtime has no queued task" so the daemon
	// poll path can skip a Postgres scan on the steady-state empty case.
	// Optional — a nil cache disables the fast path and every claim
	// goes through the DB. Wired in router.go from the shared Redis
	// client.
	EmptyClaim *EmptyClaimCache
	// ReclaimCheck schedules the next time a runtime can plausibly contain a
	// stale dispatched task. It removes the unconditional reclaim UPDATE from
	// idle claim polls while missing/error states preserve the DB fallback.
	ReclaimCheck *ReclaimCheckCache

	analyticsContextMu    sync.Mutex
	analyticsContextCache map[string]analytics.TaskContext
	analyticsContextOrder []string
}

type SourceContextObjectStore interface {
	DeleteObject(ctx context.Context, key string) error
	KeyFromURL(rawURL string) string
}

type TaskWakeupNotifier interface {
	NotifyTaskAvailable(runtimeID, taskID string)
}

// triggerSummaryMaxLen caps the snapshot length so the row stays cheap to
// transmit (it ends up in every task list response). 200 is enough for a
// recognisable preview of a one-paragraph comment.
const triggerSummaryMaxLen = 200

// truncateForSummary returns s shortened to maxRunes, with a trailing
// `…` when truncated. Operates on runes (not bytes) so multibyte characters
// — Chinese / emoji — count as one each. Strips surrounding whitespace
// first so a leading newline doesn't waste budget.
func truncateForSummary(s string, maxRunes int) string {
	// strings.Builder + Grow avoids the O(N²) realloc cycle of `+=` in
	// a loop. Grow uses byte length, which is an upper bound for the
	// rune-equivalent output (replacing \n/\r/\t with space is byte-equal
	// for ASCII whitespace), so we never reallocate.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	rs := []rune(strings.TrimSpace(b.String()))
	if len(rs) <= maxRunes {
		return string(rs)
	}
	return string(rs[:maxRunes]) + "…"
}

// maxSynthesizedFallbackCommentRunes bounds the completion-fallback comment that
// CompleteTask synthesizes from a task's final output when the agent left no
// comment of its own during the run. A real final assistant message is at most
// a few thousand words; anything larger is a runaway raw-stream dump — every
// streamed text delta concatenated together plus a literal `tool call` line per
// tool_use event — which some runtimes/providers emit as the task's Output on
// long, tool-heavy runs. Such a dump (observed at 190–264 KB) must never be
// posted, even partially, to the issue thread (GH #5455).
const maxSynthesizedFallbackCommentRunes = 8000

const oversizedFallbackCommentNotice = "This task completed, but its output was too large to post safely. The raw output was not posted. Review the task in this issue's Execution log."

// truncateFallbackCommentBody bounds a synthesized completion-fallback comment
// body. Unlike truncateForSummary (which flattens newlines for a one-line row
// snapshot), it preserves genuine final messages below the cap verbatim. Output
// above the cap is untrusted: the reported failure mode puts process narration
// and tool traces at the head, so retaining any excerpt can expose execution
// details and still discard the final answer. Replace the entire body with a
// fixed notice instead. Callers pass the already-redacted body.
func truncateFallbackCommentBody(body string, maxRunes int) string {
	if utf8.RuneCountInString(body) <= maxRunes {
		return body
	}
	return oversizedFallbackCommentNotice
}

const (
	taskAnalyticsContextCacheMax = 4096
	// RuntimeClaimFreshnessSeconds is the maximum DB heartbeat age accepted by
	// every task release path (deferred promotion, stale-dispatch reclaim, and
	// fresh claim). It must exceed the 60s DB heartbeat flush interval, one ~15s
	// daemon heartbeat, and the ~30s batch scheduler tick. 150s leaves a 45s
	// buffer above that 105s worst-case age. Keep the runtime sweeper threshold
	// aligned: a runtime must not start work after it is stale enough to be
	// marked offline.
	RuntimeClaimFreshnessSeconds = 150.0
	// claimResponseRecoveryWindow must exceed daemon client.Timeout for
	// /tasks/claim (30s) plus /tasks/{id}/start (30s) plus scheduling slack.
	// Longer pre-start work is protected by prepareLeaseDuration instead of
	// stretching this global crash-recovery window.
	claimResponseRecoveryWindow = 90 * time.Second
	prepareLeaseDuration        = 45 * time.Second
)

func (s *TaskService) trackTaskForReclaim(task db.AgentTaskQueue, checkAfter time.Time) {
	if !task.RuntimeID.Valid || !task.ID.Valid || task.Status != "dispatched" {
		return
	}
	// The Redis hint is advisory and intentionally uses an application-clock
	// elapsed deadline captured beside the DB command. Comparing PostgreSQL's
	// absolute dispatched_at/lease timestamps with time.Now would mix clocks;
	// PostgreSQL remains authoritative when the reclaim query actually runs.
	// A bounded background context lets this best-effort write outlive a request
	// cancellation without allowing Redis to stall the hot path indefinitely.
	s.ReclaimCheck.Track(
		context.Background(),
		util.UUIDToString(task.RuntimeID),
		util.UUIDToString(task.ID),
		checkAfter,
	)
}

func (s *TaskService) extendTaskReclaimHint(task db.AgentTaskQueue, checkAfter time.Time) {
	if !task.RuntimeID.Valid || !task.ID.Valid || task.Status != "dispatched" {
		return
	}
	// Preserve a later initial recovery deadline; a short prepare lease should
	// only move the hint when repeated extensions actually protect the task for
	// longer. See trackTaskForReclaim for the clock/context rationale.
	s.ReclaimCheck.TrackLater(
		context.Background(),
		util.UUIDToString(task.RuntimeID),
		util.UUIDToString(task.ID),
		checkAfter,
	)
}

func (s *TaskService) forgetTaskReclaim(task db.AgentTaskQueue) {
	if !task.RuntimeID.Valid || !task.ID.Valid {
		return
	}
	// This cleanup is best-effort and uses the cache's bounded timeout; request
	// cancellation must not make a committed task transition leave a stale hint.
	s.ReclaimCheck.Forget(
		context.Background(),
		util.UUIDToString(task.RuntimeID),
		util.UUIDToString(task.ID),
	)
}

// buildCommentTriggerSummary fetches the comment content and truncates
// it for storage on the task row. Returns an invalid pgtype.Text when
// the comment is missing (deleted / wrong workspace / etc) so the column
// stays NULL — front-end falls back to a structural label in that case.
//
// workspaceID scopes the fetch to the task's own workspace: the summary is
// later returned in claim / task-history responses, so a foreign comment UUID
// reaching an enqueue/merge path must NOT leak another workspace's text even in
// truncated form (MUL-4252).
func (s *TaskService) buildCommentTriggerSummary(ctx context.Context, workspaceID, commentID pgtype.UUID) pgtype.Text {
	if !commentID.Valid {
		return pgtype.Text{}
	}
	comment, err := s.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID:          commentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return pgtype.Text{}
	}
	summary := truncateForSummary(comment.Content, triggerSummaryMaxLen)
	if summary == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: summary, Valid: true}
}

// ResolveOriginatorFromTriggerComment is the exported wrapper used by the
// comment-merge path (MUL-4195) to compute the top-of-chain human originator
// for a newly-arrived comment, so a merge can be gated on the originator being
// unchanged. workspaceID scopes the comment lookup to the task's workspace
// (MUL-4252). See resolveOriginatorFromTriggerComment for the chain rules.
func (s *TaskService) ResolveOriginatorFromTriggerComment(ctx context.Context, workspaceID, commentID pgtype.UUID) pgtype.UUID {
	return s.resolveOriginatorFromTriggerComment(ctx, workspaceID, commentID)
}

// AttributionForMergedComment resolves the FULL attribution snapshot for a comment
// being coalesced into an already-queued task (MUL-4302). A merge re-attributes the
// run to the newly-arrived comment's human, so the whole snapshot — source, evidence,
// delegation lineage, and both person columns — must move together as one
// attribution.Result; re-stamping only the person columns would leave a run showing
// B accountable while still pointing at A's stale source / evidence / level. isMention
// picks the agent-authored label (delegation for a mention / thread-parent, otherwise
// comment_source), matching the fresh-enqueue routing.
//
// The merge re-opens the same fail-closed decision the original enqueue faced: a merge
// swaps the effective trigger, responsible human, and evidence to the NEW comment, so
// "the enqueue already checked" does not carry over. It runs the comment through
// applyAttributionFallback — the identical fail-closed gate the fresh-enqueue path uses
// — and returns ErrAttributionFailClosed when the new comment cannot be attributed
// precisely and the workspace forbids the owner_fallback degrade. The caller must then
// REFUSE the merge and keep the original (precisely-attributed) task snapshot rather
// than re-stamp a queued run to a degraded owner_fallback (Elon must-fix).
func (s *TaskService) AttributionForMergedComment(ctx context.Context, workspaceID, commentID pgtype.UUID, isMention bool, agent db.Agent) (attribution.Result, error) {
	agentAuthoredSource := attribution.SourceCommentSource
	if isMention {
		agentAuthoredSource = attribution.SourceDelegation
	}
	attr := s.attributionFromTriggerComment(ctx, workspaceID, commentID, agentAuthoredSource)
	return s.applyAttributionFallback(ctx, attr, agent)
}

// BuildCommentTriggerSummary is the exported wrapper used by the comment-merge
// path (MUL-4195) to refresh a coalesced task's trigger_summary to the newest
// trigger comment's snapshot. workspaceID scopes the lookup (MUL-4252).
func (s *TaskService) BuildCommentTriggerSummary(ctx context.Context, workspaceID, commentID pgtype.UUID) pgtype.Text {
	return s.buildCommentTriggerSummary(ctx, workspaceID, commentID)
}

func NewTaskService(q *db.Queries, tx TxStarter, hub *realtime.Hub, bus *events.Bus, wakeups ...TaskWakeupNotifier) *TaskService {
	var wakeup TaskWakeupNotifier
	if len(wakeups) > 0 {
		wakeup = wakeups[0]
	}
	return &TaskService{Queries: q, TxStarter: tx, Hub: hub, Bus: bus, Wakeup: wakeup}
}

var trivialDoneMarkers = []string{
	"done",
	"готово",
	"готова",
	"сделано",
	"完成",
	"完了",
}

func isTrivialDoneOutput(output string) bool {
	normalized := strings.TrimSpace(strings.ToLower(output))
	normalized = strings.Trim(normalized, ".!！。… ")
	for _, marker := range trivialDoneMarkers {
		if normalized == marker {
			return true
		}
	}
	return false
}

func (s *TaskService) captureTaskQueued(ctx context.Context, task db.AgentTaskQueue) {
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskEnqueued(source, runtimeMode)
	}
}

// resolveOriginatorFromTriggerComment returns the top-of-chain HUMAN user
// id for a comment that triggered an Enqueue* path. The chain rules
// (MUL-3869):
//
//   - trigger comment authored by a member → originator = author_id (that
//     member IS the top-of-chain human).
//   - trigger comment authored by an agent → read the parent task via
//     comment.source_task_id and inherit its originator_user_id. This is
//     the load-bearing case for agent fan-out: agent A @-mentions agent B,
//     comment author is A, but we MUST surface the human who originally
//     told A to run, not lose the originator at the first agent hop.
//   - missing comment / unknown source task / NULL parent originator →
//     invalid pgtype.UUID. BuildTaskOverlay treats that as "no overlay"
//     (gate 1).
//
// A nil receiver / nil Queries falls through to invalid so unit-test
// setups that don't wire a DB stay safe. workspaceID scopes the comment lookup
// to the task's workspace so a foreign comment UUID cannot resolve an
// originator from another tenant (MUL-4252).
func (s *TaskService) resolveOriginatorFromTriggerComment(ctx context.Context, workspaceID, commentID pgtype.UUID) pgtype.UUID {
	// The originator VALUE is independent of the agent-authored source label, so
	// any label works here; comment_source is passed only as a placeholder.
	return s.attributionFromTriggerComment(ctx, workspaceID, commentID, attribution.SourceCommentSource).UserID
}

// attributionFromTriggerComment resolves the full attribution (accountable
// human + provenance label + delegation lineage + evidence) for a
// comment-triggered run. It performs the DB reads and hands the gathered facts
// to the pure attribution.ClassifyComment rules so the classification stays
// side-effect-free and unit-tested. The returned UserID is byte-identical to
// the pre-MUL-4302 originator resolution, so authorization behavior (the
// canInvokeAgent A2A gate) is unchanged. workspaceID scopes the comment
// lookup to the task's workspace (MUL-4252).
//
// agentAuthoredSource selects the label for an agent-authored trigger comment:
// attribution.SourceCommentSource for the issue-assignee-reacting path,
// attribution.SourceDelegation for an explicit mention / thread-parent path.
func (s *TaskService) attributionFromTriggerComment(ctx context.Context, workspaceID, commentID pgtype.UUID, agentAuthoredSource attribution.Source) attribution.Result {
	if s == nil || s.Queries == nil || !commentID.Valid {
		return attribution.Result{Source: attribution.SourceUnattributed}
	}
	comment, err := s.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID:          commentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return attribution.Result{Source: attribution.SourceUnattributed}
	}
	return s.attributionFromComment(ctx, comment, agentAuthoredSource)
}

// attributionFromComment classifies a run from an already-loaded trigger comment,
// so a caller that already has the row (e.g. to inspect author_type) does not
// re-read it. Kept byte-identical to the inline logic attributionFromTriggerComment
// used before, so authorization behavior is unchanged.
func (s *TaskService) attributionFromComment(ctx context.Context, comment db.Comment, agentAuthoredSource attribution.Source) attribution.Result {
	facts := attribution.CommentFacts{
		CommentID:  comment.ID,
		AuthorType: comment.AuthorType,
		AuthorID:   comment.AuthorID,
	}
	// For an agent-authored comment, walk comment.source_task_id → parent task →
	// parent.originator_user_id (set by every agent comment-write path since
	// migration 120). A NULL/missing source task leaves ParentOriginator
	// invalid, which ClassifyComment maps to unattributed.
	if comment.AuthorType == "agent" && comment.SourceTaskID.Valid {
		facts.SourceTaskID = comment.SourceTaskID
		if parent, err := s.Queries.GetAgentTask(ctx, comment.SourceTaskID); err == nil {
			facts.ParentOriginator = parent.OriginatorUserID
			facts.ParentAccountable = parent.AccountableUserID
		}
	}
	return attribution.ClassifyComment(facts, agentAuthoredSource)
}

// resolveOriginatorForIssueTask returns the top-of-chain human for issue-backed
// dispatches. Comment-triggered runs keep the existing comment-chain semantics;
// direct issue assignment/creation falls back to the issue's member creator.
// Agent-created issues that carry an explicit task-origin link — agent_create
// (an agent's ordinary `issue create`, MUL-4305) or a historical quick_create
// row — inherit that origin task's originator, since origin_id
// points at the agent_task_queue row that created the issue. Other
// agent/system origins deliberately remain unattributed.
func (s *TaskService) resolveOriginatorForIssueTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID) pgtype.UUID {
	return s.attributionForIssueTask(ctx, issue, triggerCommentID, attribution.SourceCommentSource, pgtype.UUID{}).UserID
}

// attributionForIssueTask resolves the full attribution for an issue-backed
// enqueue. Comment-triggered runs keep the comment-chain semantics; direct
// assignment/creation falls back to the issue's member creator; origin-linked
// agent-created issues inherit the origin task's human as a delegation. The
// accountable-human value is byte-identical to resolveOriginatorForIssueTask,
// which now delegates here — so there is a single source of truth and
// authorization is unaffected. agentAuthoredSource labels the agent-authored
// trigger comment case (see attributionFromTriggerComment).
func (s *TaskService) attributionForIssueTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, agentAuthoredSource attribution.Source, actorUserID pgtype.UUID) attribution.Result {
	// A direct member action is the accountable human AND originator, ahead of any
	// trigger comment, origin, or rule (MUL-4302 §4/§5). This covers assign/promote
	// and a manual rerun — the last of which may INHERIT
	// a triggerCommentID for the daemon's prompt context, but must still attribute to
	// the member who clicked rerun, not the original comment's human. So the actor is
	// checked before the trigger-comment / origin branches.
	if actorUserID.Valid {
		return attribution.ClassifyDirect(attribution.DirectFacts{IssueID: issue.ID, ActorUserID: actorUserID})
	}
	if triggerCommentID.Valid {
		if s == nil || s.Queries == nil {
			return attribution.Result{Source: attribution.SourceUnattributed}
		}
		// workspace-scoped so a foreign comment UUID cannot resolve a human from
		// another tenant (MUL-4252).
		comment, err := s.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
			ID:          triggerCommentID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return attribution.Result{Source: attribution.SourceUnattributed}
		}
		// A member/agent trigger comment resolves the human (direct_human / delegation
		// / comment_source). A SYSTEM-authored comment — historically the
		// Stage-completion child-done comment, which woke the parent assignee
		// and threaded no actor — carries no human and is not part of any delegation
		// chain. Classifying it would degrade straight to owner_fallback (the agent's
		// own owner), which is wrong for a Stage cascade: the woken run should be
		// accountable to whoever caused the PARENT issue to exist. So for a system
		// comment we skip the comment branch and fall through to the parent issue's
		// own provenance below — the same creator / agent_create-origin chain a
		// direct enqueue resolves — reaching owner_fallback
		// only if that provenance itself has no human (MUL-4302; raised by Bohan on
		// the stage-cascade fallback).
		if comment.AuthorType != "system" {
			return s.attributionFromComment(ctx, comment, agentAuthoredSource)
		}
	}
	facts := attribution.DirectFacts{
		IssueID:     issue.ID,
		CreatorType: issue.CreatorType,
		CreatorID:   issue.CreatorID,
	}
	// Member-created issues resolve without a DB read. Only origin-linked
	// agent-created issues (agent_create, historical quick_create) need to load the origin
	// task to inherit its human, and only when the DB is wired (nil Queries keeps
	// unit-test setups safe and yields unattributed). Both origin types stamp
	// origin_id with the agent_task_queue row that created the issue, so the
	// top-of-chain human is that task's originator_user_id (MUL-4305).
	if !(issue.CreatorType == "member" && issue.CreatorID.Valid) &&
		s != nil && s.Queries != nil && issue.OriginType.Valid && issue.OriginID.Valid &&
		(issue.OriginType.String == "quick_create" || issue.OriginType.String == "agent_create") {
		facts.OriginType = issue.OriginType.String
		facts.OriginTaskID = issue.OriginID
		if task, err := s.Queries.GetAgentTask(ctx, issue.OriginID); err == nil {
			facts.OriginOriginator = task.OriginatorUserID
			facts.OriginAccountable = task.AccountableUserID
		}
	}
	return attribution.ClassifyDirect(facts)
}

// ErrAttributionFailClosed signals that a run resolved to no precise accountable
// human and the enqueue is REFUSED rather than started. It covers three cases, all
// of which mean "we cannot guarantee an accountable human for this run" (MUL-4302
// §1/§3.5): the workspace opted into fail-closed; the workspace policy could not be
// read (so we cannot confirm fallback is allowed — fail closed, don't run); or
// owner_fallback has no agent owner to fall back to. Enqueue paths surface it so the
// run never starts.
var ErrAttributionFailClosed = errors.New("attribution: no precise accountable human and enqueue refused (fail-closed policy, policy read failed, or no agent owner)")

// ErrDuplicatePendingTask means a fresh enqueue lost the race to a concurrent
// one: a queued/dispatched task for the same (issue, agent) already exists, so
// the pending-task unique index rejected the insert (#5914). This is a benign
// outcome — a sibling run already covers this target
// — not a server fault. Enqueue paths return it so callers can report a
// success-shaped coalesced outcome / structured 409 instead of surfacing the
// raw Postgres constraint as a 500. It is returned BARE (the raw driver text,
// including the index name, is logged once at debug and never wrapped in) so no
// upper-layer log or response can leak the constraint name (#5914, Elon review).
var ErrDuplicatePendingTask = errors.New("a pending task for this issue and agent already exists")

// isDuplicatePendingTaskErr reports whether err is the pending-task unique-index
// violation (a concurrent enqueue won the race). Accept both names while v1 and
// v2 can coexist during a rolling deploy.
func isDuplicatePendingTaskErr(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	switch pgErr.ConstraintName {
	case "idx_one_pending_task_per_issue_agent", "idx_one_pending_task_per_issue_agent_v2", "idx_one_pending_task_per_issue_agent_thread":
		return true
	default:
		return false
	}
}

// pendingSlotTakenErr reports whether err means "the (issue, agent) pending slot
// was already occupied when we tried to enqueue".
//
// Both paths behind RerunIssue now normalize the unique violation into the bare
// ErrDuplicatePendingTask sentinel: enqueueMentionTaskWithCommentPlan since
// #5958, and enqueueIssueTaskWithCommentPlan as of #5914 above. The sentinel
// half is therefore what matches in practice, and it has to be here — the
// mention path is the one taken by EVERY rerun whose target is not the issue's
// current agent assignee (a displaced agent re-fired by task_id, a mentioned
// agent), and matching only the raw pgconn error meant the
// reclaim never ran for those, so a system retry winning the slot surfaced as a
// hard error instead.
//
// The raw-violation half is kept deliberately, as a cheap defensive net: one
// errors.As, and it keeps this predicate honest for any caller that reaches it
// without passing through one of those two normalizing paths.
func pendingSlotTakenErr(err error) bool {
	return isDuplicatePendingTaskErr(err) || errors.Is(err, ErrDuplicatePendingTask)
}

// applyAttributionFallback applies the workspace's degraded-attribution policy to a
// resolved attribution whose source came back unattributed (no precise human). A
// PRECISE attribution passes through untouched (no policy read at all). For an
// unattributed run the accountable-never-null guarantee is enforced fail-closed —
// we never silently enqueue a task that could run with a NULL accountable_user_id:
//
//   - policy read fails (or no workspace) → REFUSE. We cannot confirm the workspace
//     permits fallback, so we do not run an unattributable task on a transient DB
//     hiccup. (Only the rare unattributed path pays this; precise runs never read.)
//   - fail-closed workspace → REFUSE.
//   - otherwise → owner_fallback (accountable = agent owner, audit-only, originator
//     untouched). If there is no valid agent owner, owner_fallback stays
//     unattributed → REFUSE rather than enqueue a NULL-accountable task.
//
// Keeping this at the enqueue boundary (not inside the pure classifiers) means
// owner_fallback needs the agent owner, which every enqueue path has in hand.
func (s *TaskService) applyAttributionFallback(ctx context.Context, attr attribution.Result, agent db.Agent) (attribution.Result, error) {
	if attr.Source != attribution.SourceUnattributed {
		return attr, nil
	}
	if s == nil || s.Queries == nil || !agent.WorkspaceID.Valid {
		return attr, fmt.Errorf("%w: workspace policy unavailable", ErrAttributionFailClosed)
	}
	failClosed, err := s.Queries.GetWorkspaceAttributionFailClosed(ctx, agent.WorkspaceID)
	if err != nil {
		// Cannot confirm the workspace allows fallback → fail closed rather than
		// silently run an unattributable task.
		return attr, fmt.Errorf("%w: policy read failed: %v", ErrAttributionFailClosed, err)
	}
	if failClosed {
		return attr, ErrAttributionFailClosed
	}
	fallback := attribution.OwnerFallback(attr, agent.OwnerID)
	if fallback.Source == attribution.SourceUnattributed {
		// owner_fallback could not resolve an accountable human (no valid agent
		// owner): refuse rather than enqueue a NULL-accountable task.
		return attr, fmt.Errorf("%w: no agent owner to attribute", ErrAttributionFailClosed)
	}
	return fallback, nil
}

// attributionCreateParams maps a resolved attribution onto the CreateAgentTask
// provenance columns. originator_source is always stamped (never NULL for a new
// row); delegation lineage and evidence are stamped only when present.
func attributionCreateParams(attr attribution.Result) (source pgtype.Text, delegatedFrom pgtype.UUID, evidenceKind pgtype.Text, evidenceRef pgtype.UUID) {
	source = pgtype.Text{String: attr.Source.String(), Valid: true}
	delegatedFrom = attr.DelegatedFromTaskID
	evidenceKind = pgtype.Text{String: string(attr.EvidenceKind), Valid: attr.EvidenceKind != ""}
	evidenceRef = attr.EvidenceRefID
	return
}

// OriginatorForIssueTask exposes resolveOriginatorForIssueTask to callers
// outside the service package so an access gate judges the top-of-chain human
// with the exact same resolution the enqueue path persists on the task row
// (MUL-4305).
func (s *TaskService) OriginatorForIssueTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID) pgtype.UUID {
	return s.resolveOriginatorForIssueTask(ctx, issue, triggerCommentID)
}

func (s *TaskService) captureTaskDispatched(ctx context.Context, task db.AgentTaskQueue) {
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskDispatched(util.UUIDToString(task.ID), source, runtimeMode, taskQueueWaitSeconds(task), taskClaimableWaitSeconds(task))
	}
}

func (s *TaskService) AnalyticsContextForTask(ctx context.Context, task db.AgentTaskQueue) analytics.TaskContext {
	return s.taskAnalyticsContext(ctx, task)
}

func (s *TaskService) captureTaskStarted(ctx context.Context, task db.AgentTaskQueue) {
	if s.Metrics != nil {
		source, runtimeMode, provider := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskStarted(source, runtimeMode, provider)
	}
}

func (s *TaskService) captureTaskCompleted(ctx context.Context, task db.AgentTaskQueue) {
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskTerminal(util.UUIDToString(task.ID), source, runtimeMode, task.Status, taskRunSeconds(task), taskTotalSeconds(task), task.Attempt)
	}
}

func (s *TaskService) captureTaskFailed(ctx context.Context, task db.AgentTaskQueue) {
	failureReason := taskFailureReason(task)
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskTerminal(util.UUIDToString(task.ID), source, runtimeMode, task.Status, taskRunSeconds(task), taskTotalSeconds(task), task.Attempt)
		s.Metrics.RecordTaskFailed(source, runtimeMode, failureReason)
	}
}

func (s *TaskService) captureTaskCancelled(ctx context.Context, task db.AgentTaskQueue) {
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskTerminal(util.UUIDToString(task.ID), source, runtimeMode, task.Status, taskRunSeconds(task), taskTotalSeconds(task), task.Attempt)
	}
	// Revoke any mat_ task tokens minted for this task. Cancellation is
	// a terminal transition, so the running agent process no longer
	// needs to call back; eagerly deleting the token closes the
	// window where a compromised process could keep authenticating
	// against the API until the 24h expiry. Failure is non-fatal — the
	// expiry / FK cascade are the durable guards. MUL-2600.
	if err := s.Queries.DeleteTaskTokensByTask(ctx, task.ID); err != nil {
		slog.Warn("cancel task: failed to revoke task tokens",
			"task_id", util.UUIDToString(task.ID), "error", err)
	}
}

// costUSDTicks is the provider's own price for this usage in 1e-10 USD, or 0
// when it reported none — the metrics layer prefers it over its rate table.
func (s *TaskService) CaptureTaskUsage(ctx context.Context, task db.AgentTaskQueue, provider, model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, costUSDTicks int64) {
	if s.Metrics == nil {
		return
	}
	source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
	s.Metrics.RecordLLMUsage(source, runtimeMode, provider, model, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, costUSDTicks)
}

func (s *TaskService) CaptureQueuedExpiredTasks(ctx context.Context, tasks []db.AgentTaskQueue) {
	if s.Metrics == nil {
		return
	}
	for _, task := range tasks {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskQueuedExpired(source, runtimeMode)
	}
}

func (s *TaskService) CaptureLeaseExpiredTasks(ctx context.Context, tasks []db.AgentTaskQueue) {
	if s.Metrics == nil {
		return
	}
	for _, task := range tasks {
		source, _, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskLeaseExpired(source)
	}
}

func (s *TaskService) cachedTaskAnalyticsContext(task db.AgentTaskQueue) (analytics.TaskContext, bool) {
	key := taskAnalyticsContextKey(task)
	if key == "" {
		return analytics.TaskContext{}, false
	}
	s.analyticsContextMu.Lock()
	defer s.analyticsContextMu.Unlock()
	if s.analyticsContextCache == nil {
		return analytics.TaskContext{}, false
	}
	tc, ok := s.analyticsContextCache[key]
	return tc, ok
}

func (s *TaskService) storeTaskAnalyticsContext(task db.AgentTaskQueue, tc analytics.TaskContext) {
	if tc.WorkspaceID == "" {
		return
	}
	key := taskAnalyticsContextKey(task)
	if key == "" {
		return
	}
	s.analyticsContextMu.Lock()
	defer s.analyticsContextMu.Unlock()
	if s.analyticsContextCache == nil {
		s.analyticsContextCache = make(map[string]analytics.TaskContext)
	}
	if _, ok := s.analyticsContextCache[key]; !ok {
		s.analyticsContextOrder = append(s.analyticsContextOrder, key)
		if len(s.analyticsContextOrder) > taskAnalyticsContextCacheMax {
			oldest := s.analyticsContextOrder[0]
			s.analyticsContextOrder = s.analyticsContextOrder[1:]
			delete(s.analyticsContextCache, oldest)
		}
	}
	s.analyticsContextCache[key] = tc
}

func taskAnalyticsContextKey(task db.AgentTaskQueue) string {
	taskID := util.UUIDToString(task.ID)
	if taskID == "" {
		return ""
	}
	return strings.Join([]string{
		taskID,
		util.UUIDToString(task.RuntimeID),
		util.UUIDToString(task.IssueID),
		util.UUIDToString(task.ChatSessionID),
	}, "|")
}

func (s *TaskService) taskMetricsContext(ctx context.Context, task db.AgentTaskQueue) (source, runtimeMode, provider string) {
	tc := s.taskAnalyticsContext(ctx, task)
	source = "other"
	switch {
	case task.ChatSessionID.Valid:
		source = "chat"
	case task.IssueID.Valid:
		source = "issue"
	default:
		if tc.Source != "" {
			source = tc.Source
		}
	}
	return source, tc.RuntimeMode, tc.Provider
}

func (s *TaskService) taskAnalyticsContext(ctx context.Context, task db.AgentTaskQueue) analytics.TaskContext {
	if tc, ok := s.cachedTaskAnalyticsContext(task); ok {
		return tc
	}
	tc := analytics.TaskContext{
		AgentID: util.UUIDToString(task.AgentID),
		TaskID:  util.UUIDToString(task.ID),
		Source:  analytics.SourceManual,
	}
	if task.IssueID.Valid {
		tc.IssueID = util.UUIDToString(task.IssueID)
	}
	if task.ChatSessionID.Valid {
		tc.ChatSessionID = util.UUIDToString(task.ChatSessionID)
		tc.Source = analytics.SourceChat
	}

	if task.RuntimeID.Valid {
		if rt, err := s.runtimeLookup().Get(ctx, task.RuntimeID); err == nil {
			tc.WorkspaceID = util.UUIDToString(rt.WorkspaceID)
			tc.RuntimeMode = rt.RuntimeMode
			tc.Provider = rt.Provider
		}
	}
	if tc.WorkspaceID == "" || tc.RuntimeMode == "" {
		if agent, err := s.Queries.GetAgent(ctx, task.AgentID); err == nil {
			if tc.WorkspaceID == "" {
				tc.WorkspaceID = util.UUIDToString(agent.WorkspaceID)
			}
			if tc.RuntimeMode == "" {
				tc.RuntimeMode = agent.RuntimeMode
			}
		}
	}

	if task.IssueID.Valid {
		if issue, err := s.Queries.GetIssue(ctx, task.IssueID); err == nil {
			tc.WorkspaceID = util.UUIDToString(issue.WorkspaceID)
			if issue.CreatorType == "member" {
				tc.UserID = util.UUIDToString(issue.CreatorID)
			}
		}
	}
	if task.ChatSessionID.Valid {
		if cs, err := s.Queries.GetChatSession(ctx, task.ChatSessionID); err == nil {
			tc.WorkspaceID = util.UUIDToString(cs.WorkspaceID)
			tc.UserID = util.UUIDToString(cs.CreatorID)
		}
	}
	s.storeTaskAnalyticsContext(task, tc)
	return tc
}

func taskQueueWaitSeconds(task db.AgentTaskQueue) float64 {
	return durationSeconds(task.CreatedAt, task.DispatchedAt)
}

// taskClaimableWaitSeconds excludes an intentional deferred delay when fire_at
// is still available on the claimed row. Immediate tasks start at creation.
func taskClaimableWaitSeconds(task db.AgentTaskQueue) float64 {
	claimableAt := task.CreatedAt
	if task.FireAt.Valid && (!claimableAt.Valid || task.FireAt.Time.After(claimableAt.Time)) {
		claimableAt = task.FireAt
	}
	return durationSeconds(claimableAt, task.DispatchedAt)
}

func taskRunSeconds(task db.AgentTaskQueue) float64 {
	return durationSeconds(task.StartedAt, task.CompletedAt)
}

func taskTotalSeconds(task db.AgentTaskQueue) float64 {
	return durationSeconds(task.CreatedAt, task.CompletedAt)
}

func durationSeconds(start, end pgtype.Timestamptz) float64 {
	if !start.Valid || !end.Valid {
		return -1
	}
	seconds := end.Time.Sub(start.Time).Seconds()
	if seconds < 0 {
		return 0
	}
	return seconds
}

func taskFailureReason(task db.AgentTaskQueue) string {
	if task.FailureReason.Valid && task.FailureReason.String != "" {
		return task.FailureReason.String
	}
	return "agent_error"
}

func taskErrorType(reason string) string {
	switch reason {
	case "runtime_offline", "runtime_recovery":
		return "runtime"
	case "timeout", "codex_semantic_inactivity":
		return "timeout"
	case "iteration_limit", "agent_fallback_message":
		return "agent_output"
	case "cancelled", "user_cancelled":
		return "cancelled"
	default:
		return "agent_error"
	}
}

// EnqueueTaskForIssue creates a queued task for an agent-assigned issue.
// No context snapshot is stored — the agent fetches all data it needs at
// runtime via the multica CLI.
func (s *TaskService) EnqueueTaskForIssue(ctx context.Context, issue db.Issue, triggerCommentID ...pgtype.UUID) (db.AgentTaskQueue, error) {
	var commentID pgtype.UUID
	if len(triggerCommentID) > 0 {
		commentID = triggerCommentID[0]
	}
	return s.enqueueIssueTask(ctx, issue, commentID, false, "", pgtype.UUID{}, pgtype.UUID{})
}

// EnqueueTaskForIssueByActor is the assign/promote variant of
// EnqueueTaskForIssue. actorUserID is the member who performed the
// assign/promote and becomes the accountable human for the run (MUL-4302 §4);
// invalid when the caller has no member actor.
func (s *TaskService) EnqueueTaskForIssueByActor(ctx context.Context, issue db.Issue, actorUserID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueIssueTask(ctx, issue, pgtype.UUID{}, false, "", actorUserID, pgtype.UUID{})
}

// EnqueueTaskForIssueWithHandoff is the backward-compatible assign/promote
// variant used when an installed client still sends handoff_note. The note is
// persisted on the task so both old and current daemons can render it in the
// run's opening prompt. Empty text behaves like EnqueueTaskForIssueByActor.
func (s *TaskService) EnqueueTaskForIssueWithHandoff(ctx context.Context, issue db.Issue, handoffNote string, actorUserID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueIssueTask(ctx, issue, pgtype.UUID{}, false, handoffNote, actorUserID, pgtype.UUID{})
}

// enqueueIssueTask is the shared implementation behind EnqueueTaskForIssue
// and the manual rerun path. forceFreshSession=true marks the task so the
// daemon claim handler skips the (agent_id, issue_id) resume lookup — the
// user already judged the prior output bad, a fresh agent session is the
// expected behavior.
// ResolveIssueReviewSHA returns the head SHA of the commit currently under
// review for an issue (the head_sha of its most-relevant linked PR), or the
// empty string when the issue has no linked PR. Callers thread this into both
// the reviewer-loop dedup check and the enqueue path so a pending review task
// pinned to an old head does not satisfy a request after HEAD advanced
// (TEN-356). Empty string is the safe default: it makes dedup fall back to the
// pre-TEN-356 (issue_id, agent_id) key and leaves the task's context NULL.
//
// The lookup fails soft — any DB error (including "no linked PR") returns "" so
// a transient github-table hiccup can never over-dedup a review out of
// existence; the worst case is the pre-TEN-356 coalescing behavior.
func (s *TaskService) ResolveIssueReviewSHA(ctx context.Context, issueID pgtype.UUID) string {
	if !issueID.Valid {
		return ""
	}
	sha, err := s.Queries.GetIssueReviewHeadSha(ctx, issueID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("resolve issue review sha failed",
				"issue_id", util.UUIDToString(issueID), "error", err)
		}
		return ""
	}
	return sha
}

// headShaText wraps a resolved review SHA into the pgtype.Text the dedup/enqueue
// queries expect. Empty SHA marshals to an invalid (NULL) Text so the queries
// take their fall-back branch.
func headShaText(sha string) pgtype.Text {
	return pgtype.Text{String: sha, Valid: sha != ""}
}

// ResolveIssueReviewSHAParam is ResolveIssueReviewSHA wrapped as the pgtype.Text
// the dedup queries take, so both service- and handler-package call sites can
// key dedup on the reviewed head with a single call (TEN-356).
func (s *TaskService) ResolveIssueReviewSHAParam(ctx context.Context, issueID pgtype.UUID) pgtype.Text {
	return headShaText(s.ResolveIssueReviewSHA(ctx, issueID))
}

func (s *TaskService) enqueueIssueTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, forceFreshSession bool, handoffNote string, actorUserID pgtype.UUID, rerunOfTaskID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueIssueTaskWithCommentPlan(ctx, issue, triggerCommentID, nil, forceFreshSession, handoffNote, actorUserID, rerunOfTaskID)
}

func (s *TaskService) enqueueIssueTaskWithCommentPlan(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, coalescedCommentIDs []pgtype.UUID, forceFreshSession bool, handoffNote string, actorUserID pgtype.UUID, rerunOfTaskID pgtype.UUID) (db.AgentTaskQueue, error) {
	if !issue.AssigneeID.Valid {
		slog.Error("task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", "issue has no assignee")
		return db.AgentTaskQueue{}, fmt.Errorf("issue has no assignee")
	}

	agent, err := s.Queries.GetAgent(ctx, issue.AssigneeID)
	if err != nil {
		slog.Error("task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		slog.Debug("task enqueue skipped: agent is archived", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agent.ID))
		return db.AgentTaskQueue{}, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		slog.Error("task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", "agent has no runtime")
		return db.AgentTaskQueue{}, fmt.Errorf("agent has no runtime")
	}

	// The issue assignee reacting to an agent-authored comment is a
	// comment_source attribution (a special case of delegation); a member
	// comment or direct member assignment is direct_human. attr.UserID is the
	// same value the pre-MUL-4302 resolver produced, so overlay/authorization
	// are unchanged; the extra fields are audit provenance.
	attr := s.attributionForIssueTask(ctx, issue, triggerCommentID, attribution.SourceCommentSource, actorUserID)
	// No precise human resolved → owner_fallback (accountable = agent owner), or
	// refuse the enqueue if the workspace is fail-closed (MUL-4302 §3.5).
	attr, err = s.applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		slog.Warn("task enqueue refused: attribution fail-closed", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(issue.AssigneeID))
		return db.AgentTaskQueue{}, err
	}
	originatorUserID := attr.UserID
	attrSource, attrDelegatedFrom, attrEvidenceKind, attrEvidenceRef := attributionCreateParams(attr)
	createParams := db.CreateAgentTaskParams{
		ID:                   dbid.NewV7(),
		AgentID:              issue.AssigneeID,
		RuntimeID:            agent.RuntimeID,
		IssueID:              issue.ID,
		Priority:             priorityToInt(issue.Priority),
		TriggerCommentID:     triggerCommentID,
		CoalescedCommentIds:  coalescedCommentIDs,
		TriggerSummary:       s.buildCommentTriggerSummary(ctx, issue.WorkspaceID, triggerCommentID),
		ForceFreshSession:    pgtype.Bool{Bool: forceFreshSession, Valid: forceFreshSession},
		HandoffNote:          pgtype.Text{String: handoffNote, Valid: handoffNote != ""},
		OriginatorUserID:     originatorUserID,
		AccountableUserID:    attr.AccountableUserID,
		RuleVersionID:        attr.RuleVersionID,
		RerunOfTaskID:        rerunOfTaskID,
		OriginatorSource:     attrSource,
		DelegatedFromTaskID:  attrDelegatedFrom,
		TriggerEvidenceKind:  attrEvidenceKind,
		TriggerEvidenceRefID: attrEvidenceRef,
		// Stamp the reviewed head so dedup can distinguish this run's target
		// from a later request against a new HEAD (TEN-356).
		HeadSha: headShaText(s.ResolveIssueReviewSHA(ctx, issue.ID)),
	}
	task, err := s.Queries.CreateAgentTask(ctx, createParams)
	if err != nil {
		// A concurrent enqueue for the same (issue, agent) won the race and the
		// unique index rejected this insert. That is benign — a sibling run
		// already covers this target — so log it at debug and return a typed
		// sentinel the caller maps to a coalesced outcome / 409 rather than a
		// 500 that leaks the raw constraint name (#5914). Mirrors the mention
		// path in enqueueMentionTaskWithCommentPlan.
		if isDuplicatePendingTaskErr(err) {
			slog.Debug("task enqueue coalesced: pending task already exists", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(issue.AssigneeID))
			return db.AgentTaskQueue{}, ErrDuplicatePendingTask
		}
		slog.Error("task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("create task: %w", err)
	}

	slog.Info("task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"agent_id", util.UUIDToString(issue.AssigneeID),
		"force_fresh_session", forceFreshSession,
	)
	// Order matters: broadcast first, notify daemon second. notifyTaskAvailable
	// kicks an in-process channel that the daemon picks up over HTTP and
	// claims; the claim path then emits its own task:dispatch. Doing the
	// queued broadcast afterwards risks the dispatch event reaching clients
	// before the queued one (rare but unsafe-by-construction). Publishing
	// in the desired observe-order makes correctness independent of timing.
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

// EnqueueTaskForMention creates a queued task for a mentioned agent on an issue.
// Unlike EnqueueTaskForIssue, this takes an explicit agent ID rather than
// deriving it from the issue assignee.
func (s *TaskService) EnqueueTaskForMention(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTask(ctx, issue, agentID, triggerCommentID, false, "", pgtype.UUID{}, pgtype.UUID{})
}

// EnqueueTaskForThreadParent creates a queued task for the agent who authored
// the direct parent comment a member replied to.
func (s *TaskService) EnqueueTaskForThreadParent(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID) (db.AgentTaskQueue, error) {
	// Always named: the only caller is a member replying to what this agent said.
	return s.enqueueMentionTask(ctx, issue, agentID, triggerCommentID, false, "", pgtype.UUID{}, pgtype.UUID{})
}

func (s *TaskService) enqueueMentionTask(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID, forceFreshSession bool, handoffNote string, actorUserID pgtype.UUID, rerunOfTaskID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTaskWithCommentPlan(ctx, issue, agentID, triggerCommentID, nil, forceFreshSession, handoffNote, actorUserID, rerunOfTaskID)
}

func (s *TaskService) enqueueMentionTaskWithCommentPlan(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID, coalescedCommentIDs []pgtype.UUID, forceFreshSession bool, handoffNote string, actorUserID pgtype.UUID, rerunOfTaskID pgtype.UUID) (db.AgentTaskQueue, error) {
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		slog.Error("mention task enqueue failed: agent not found", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		slog.Debug("mention task enqueue skipped: agent is archived", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
		return db.AgentTaskQueue{}, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		slog.Error("mention task enqueue failed: agent has no runtime", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
		return db.AgentTaskQueue{}, fmt.Errorf("agent has no runtime")
	}

	// An explicit mention / thread-parent hop from an
	// agent-authored comment is a delegation (the parent task's human is
	// copied); a member mention is direct_human. attr.UserID matches the
	// pre-MUL-4302 value, so authorization is unchanged.
	attr := s.attributionForIssueTask(ctx, issue, triggerCommentID, attribution.SourceDelegation, actorUserID)
	// No precise human resolved → owner_fallback (accountable = agent owner), or
	// refuse the enqueue if the workspace is fail-closed (MUL-4302 §3.5).
	attr, err = s.applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		slog.Warn("mention task enqueue refused: attribution fail-closed", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
		return db.AgentTaskQueue{}, err
	}
	originatorUserID := attr.UserID
	attrSource, attrDelegatedFrom, attrEvidenceKind, attrEvidenceRef := attributionCreateParams(attr)
	task, err := s.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		ID:                   dbid.NewV7(),
		AgentID:              agentID,
		RuntimeID:            agent.RuntimeID,
		IssueID:              issue.ID,
		Priority:             priorityToInt(issue.Priority),
		TriggerCommentID:     triggerCommentID,
		CoalescedCommentIds:  coalescedCommentIDs,
		TriggerSummary:       s.buildCommentTriggerSummary(ctx, issue.WorkspaceID, triggerCommentID),
		ForceFreshSession:    pgtype.Bool{Bool: forceFreshSession, Valid: forceFreshSession},
		HandoffNote:          pgtype.Text{String: handoffNote, Valid: handoffNote != ""},
		OriginatorUserID:     originatorUserID,
		AccountableUserID:    attr.AccountableUserID,
		RuleVersionID:        attr.RuleVersionID,
		RerunOfTaskID:        rerunOfTaskID,
		OriginatorSource:     attrSource,
		DelegatedFromTaskID:  attrDelegatedFrom,
		TriggerEvidenceKind:  attrEvidenceKind,
		TriggerEvidenceRefID: attrEvidenceRef,
		// Stamp the reviewed head so dedup can distinguish this run's target
		// from a later request against a new HEAD (TEN-356).
		HeadSha: headShaText(s.ResolveIssueReviewSHA(ctx, issue.ID)),
	})
	if err != nil {
		// A concurrent enqueue for the same (issue, agent) won the race and the
		// unique index rejected this insert. That is benign — a sibling run
		// already covers this target — so log it at debug and return a typed
		// sentinel the caller maps to a coalesced outcome / 409 rather than a
		// 500 that leaks the raw constraint name (#5914).
		if isDuplicatePendingTaskErr(err) {
			slog.Debug("mention task enqueue coalesced: pending task already exists", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
			return db.AgentTaskQueue{}, ErrDuplicatePendingTask
		}
		slog.Error("mention task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("create task: %w", err)
	}

	slog.Info("mention task enqueued", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
	// See EnqueueTaskForIssue for ordering rationale.
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

// ErrChatTaskAgentArchived signals that EnqueueChatTask refused to
// queue work because the destination agent has been archived. This
// is a productizable state — surface it to the user as "this agent
// has been archived" rather than retrying.
var ErrChatTaskAgentArchived = errors.New("chat task: agent archived")

// ErrChatTaskAgentNoRuntime signals that EnqueueChatTask refused to
// queue work because the agent has never been associated with a
// runtime (agent.runtime_id IS NULL). This is the "agent has no
// daemon configured" case — productizable as "agent offline".
//
// IMPORTANT: this is NOT the same as "the daemon is currently
// disconnected". When agent.runtime_id IS set, EnqueueChatTask
// enqueues the task and the daemon claims it on next online; that
// path returns a task row, not this error.
var ErrChatTaskAgentNoRuntime = errors.New("chat task: agent has no runtime")

// ErrChatSessionArchived signals that a send lost a race with archiving the
// session and therefore must not persist a new turn.
var ErrChatSessionArchived = errors.New("chat task: session archived")

// PreparedChatTaskEnqueue is an opaque, side-effect-free input snapshot built
// before a caller opens a task-enqueue transaction.
type PreparedChatTaskEnqueue struct {
	accountableUser  pgtype.UUID
	attrSource       pgtype.Text
	attrEvidenceKind pgtype.Text
}

// PrepareChatTaskEnqueue performs the reads a chat enqueue needs before the
// caller opens a transaction.
func (s *TaskService) PrepareChatTaskEnqueue(
	ctx context.Context,
	agentID, initiatorUserID pgtype.UUID,
) (PreparedChatTaskEnqueue, error) {
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		slog.Error("chat task preparation failed", "agent_id", util.UUIDToString(agentID), "error", err)
		return PreparedChatTaskEnqueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return PreparedChatTaskEnqueue{}, ErrChatTaskAgentArchived
	}
	if !agent.RuntimeID.Valid {
		return PreparedChatTaskEnqueue{}, ErrChatTaskAgentNoRuntime
	}

	attr := attribution.DirectHumanRun(
		initiatorUserID, attribution.EvidenceChat, pgtype.UUID{},
	)
	attr, err = s.applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		slog.Warn("chat task enqueue refused: attribution fail-closed",
			"agent_id", util.UUIDToString(agentID))
		return PreparedChatTaskEnqueue{}, err
	}
	attrSource, _, attrEvidenceKind, _ := attributionCreateParams(attr)
	return PreparedChatTaskEnqueue{
		accountableUser: attr.AccountableUserID,
		attrSource:      attrSource, attrEvidenceKind: attrEvidenceKind,
	}, nil
}

// EnqueueChatTask creates a Direct Chat task.
func (s *TaskService) EnqueueChatTask(
	ctx context.Context,
	chatSession db.ChatSession,
	initiatorUserID pgtype.UUID,
	forceFreshSession bool,
) (db.AgentTaskQueue, error) {
	prepared, err := s.PrepareChatTaskEnqueue(ctx, chatSession.AgentID, initiatorUserID)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if s.TxStarter == nil {
		return db.AgentTaskQueue{}, errors.New("chat task enqueue: transaction starter is required")
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("begin chat task enqueue: %w", err)
	}
	defer tx.Rollback(ctx)
	task, err := s.enqueueChatTaskTx(
		ctx, s.Queries.WithTx(tx), chatSession, initiatorUserID,
		forceFreshSession, prepared,
	)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("commit chat task enqueue: %w", err)
	}
	s.FinalizeChatTaskEnqueue(ctx, task)
	return task, nil
}

func (s *TaskService) enqueueChatTaskTx(
	ctx context.Context,
	qtx *db.Queries,
	chatSession db.ChatSession,
	initiatorUserID pgtype.UUID,
	forceFreshSession bool,
	prepared PreparedChatTaskEnqueue,
) (db.AgentTaskQueue, error) {
	currentSession, err := qtx.LockChatSessionForEnqueue(ctx, chatSession.ID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("lock chat session: %w", err)
	}
	if currentSession.Status != "active" {
		return db.AgentTaskQueue{}, ErrChatSessionArchived
	}

	agent, err := qtx.GetAgentForClaimUpdate(ctx, chatSession.AgentID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("reload chat agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, ErrChatTaskAgentArchived
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, ErrChatTaskAgentNoRuntime
	}

	task, err := qtx.CreateChatTask(ctx, db.CreateChatTaskParams{
		ID:                   dbid.NewV7(),
		AgentID:              chatSession.AgentID,
		RuntimeID:            agent.RuntimeID,
		Priority:             2,
		ChatSessionID:        chatSession.ID,
		InitiatorUserID:      initiatorUserID,
		OriginatorUserID:     initiatorUserID,
		AccountableUserID:    prepared.accountableUser,
		ForceFreshSession:    pgtype.Bool{Bool: forceFreshSession, Valid: true},
		OriginatorSource:     prepared.attrSource,
		TriggerEvidenceKind:  prepared.attrEvidenceKind,
		TriggerEvidenceRefID: chatSession.ID,
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create chat task: %w", err)
	}
	task, err = qtx.SetChatTaskInputOwnerSelf(ctx, task.ID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("set chat task input owner: %w", err)
	}
	return task, nil
}

// FinalizeChatTaskEnqueue performs only post-commit effects.
func (s *TaskService) FinalizeChatTaskEnqueue(ctx context.Context, task db.AgentTaskQueue) {
	slog.Info("chat task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"chat_session_id", util.UUIDToString(task.ChatSessionID),
		"agent_id", util.UUIDToString(task.AgentID),
	)
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
}

// DirectChatSendResult carries the rows a transactional direct-chat send
// persisted, so the handler can broadcast the user message and shape its
// response without re-reading them.
type DirectChatSendResult struct {
	Task               db.AgentTaskQueue
	Message            db.ChatMessage
	BoundAttachmentIDs []pgtype.UUID
	Queued             bool
	InitialTitle       string
}

// SendDirectChatMessage atomically persists one web/mobile direct-chat turn:
// the owning task (which claims its own input batch via chat_input_task_id), the
// user message bound to that task, any attachment bindings, and the session
// touch all commit together (MUL-4351). The daemon is only notified after the
// commit, so it can never observe a message without a task or a task without its
// input owner — and a later claim reads exactly this task's user messages
// instead of scanning trailing history.
//
// The caller must have already gated the session and preflighted the agent
// (archived / no-runtime), passing the loaded agent in. Those checks are repeated
// under the transaction locks below because either row may change before enqueue.
func (s *TaskService) SendDirectChatMessage(
	ctx context.Context,
	session db.ChatSession,
	agent db.Agent,
	initiatorUserID pgtype.UUID,
	content string,
	attachmentIDs []pgtype.UUID,
	uploaderType string,
	uploaderID pgtype.UUID,
) (*DirectChatSendResult, error) {
	// Full attribution for the chat sender, resolved before the tx (the policy read
	// + fallback must not run with a transaction open) — the same direct_human stamp
	// EnqueueChatTask writes. Without this the direct-chat path was a bypass: it set
	// originator_user_id but left accountable_user_id / source / evidence NULL,
	// violating the one-way invariant and dropping the audit source (MUL-4302 §2).
	attr := attribution.DirectHumanRun(initiatorUserID, attribution.EvidenceChat, session.ID)
	attr, err := s.applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		return nil, err
	}
	attrSource, _, attrEvidenceKind, attrEvidenceRef := attributionCreateParams(attr)

	var out DirectChatSendResult
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		// Serialise this send against a concurrent runtime rebind of the same
		// session (MUL-5163). The lock must be taken first and the agent re-read
		// under it: the runtime_id the caller loaded can already be stale by the
		// time we get here, and a send blocked behind a rebind would otherwise
		// resume and stamp its task with the runtime the switch just moved away
		// from — leaving the user with a "switched" confirmation and a reply
		// running on the old runtime. Locking chat_session first also matches the
		// delete path's lock order, so the two cannot deadlock.
		if _, err := qtx.LockChatSessionForRuntimeBind(ctx, session.ID); err != nil {
			return fmt.Errorf("lock chat session: %w", err)
		}
		currentSession, err := qtx.GetChatSession(ctx, session.ID)
		if err != nil {
			return fmt.Errorf("reload chat session: %w", err)
		}
		if currentSession.Status != "active" {
			return ErrChatSessionArchived
		}
		carrier, err := qtx.GetAgentForClaimUpdate(ctx, session.AgentID)
		if err != nil {
			return fmt.Errorf("reload chat agent: %w", err)
		}
		if carrier.ArchivedAt.Valid {
			return ErrChatTaskAgentArchived
		}
		if !carrier.RuntimeID.Valid {
			return ErrChatTaskAgentNoRuntime
		}

		// The database status of every newly-created task is "queued" until a
		// daemon claims it. Product queue semantics are positional instead: this
		// send is a follow-up only when another visible task in the same session
		// is already ahead of it. Deferred retries count because they resume an
		// older turn before this one. The session + agent locks serialize sibling
		// sends, retries, terminal writes, and claims around this read.
		queued, err := qtx.HasPendingChatTurnForSession(ctx, session.ID)
		if err != nil {
			return fmt.Errorf("check direct chat queue position: %w", err)
		}
		out.Queued = queued

		task, err := qtx.CreateChatTask(ctx, db.CreateChatTaskParams{
			ID:                   dbid.NewV7(),
			AgentID:              session.AgentID,
			RuntimeID:            carrier.RuntimeID,
			Priority:             2, // medium priority for chat; matches EnqueueChatTask
			ChatSessionID:        session.ID,
			InitiatorUserID:      initiatorUserID,
			OriginatorUserID:     attr.UserID,
			AccountableUserID:    attr.AccountableUserID,
			ForceFreshSession:    pgtype.Bool{Bool: false, Valid: true},
			OriginatorSource:     attrSource,
			TriggerEvidenceKind:  attrEvidenceKind,
			TriggerEvidenceRefID: attrEvidenceRef,
		})
		if err != nil {
			return fmt.Errorf("create direct chat task: %w", err)
		}
		// Claim this task's own input batch (chat_input_task_id = id) in the same
		// transaction, before the user message is written with task_id = task.id.
		task, err = qtx.SetChatTaskInputOwnerSelf(ctx, task.ID)
		if err != nil {
			return fmt.Errorf("stamp direct chat input owner: %w", err)
		}
		out.Task = task

		// Initialize an explicitly empty Chat before inserting its first public
		// user message. The CAS query also protects a manual rename and competing
		// first send; keeping it in this transaction prevents a title-only commit.
		if title := chattitle.Derive(content); title != "" {
			if _, err := qtx.InitializeChatSessionTitle(ctx, db.InitializeChatSessionTitleParams{ID: session.ID, Title: title}); err == nil {
				out.InitialTitle = title
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("initialize direct chat title: %w", err)
			}
		}

		// Create the user message already owned by this task (task_id = task.id),
		// so it belongs to this immutable input batch the instant it exists.
		msg, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
			ID:            dbid.NewV7(),
			ChatSessionID: session.ID,
			Role:          "user",
			Content:       content,
			TaskID:        task.ID,
			MessageKind:   pgtype.Text{String: protocol.ChatMessageKindMessage, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("create user chat message: %w", err)
		}
		out.Message = msg

		if len(attachmentIDs) > 0 {
			bound, err := qtx.LinkAttachmentsToChatMessage(ctx, db.LinkAttachmentsToChatMessageParams{
				ChatMessageID: msg.ID,
				ChatSessionID: session.ID,
				WorkspaceID:   session.WorkspaceID,
				UploaderType:  uploaderType,
				UploaderID:    uploaderID,
				AttachmentIds: attachmentIDs,
			})
			if err != nil {
				return fmt.Errorf("link chat attachments: %w", err)
			}
			out.BoundAttachmentIDs = bound

			// An explicitly empty /new Chat can receive its first turn from a
			// first-party client. For an attachment-only turn, initialize the
			// title from the first attachment that was actually bound. The media
			// title CAS runs after the message insert by design and still refuses
			// to overwrite a manual rename or a competing first message.
			if out.InitialTitle == "" && strings.TrimSpace(content) == "" && len(bound) > 0 {
				attachments, err := qtx.ListAttachmentsByChatMessage(ctx, db.ListAttachmentsByChatMessageParams{
					ChatMessageID: msg.ID,
					WorkspaceID:   session.WorkspaceID,
				})
				if err != nil {
					return fmt.Errorf("list bound chat attachments for title: %w", err)
				}
				if len(attachments) > 0 {
					title := chattitle.Derive(attachments[0].Filename)
					if title != "" {
						if _, err := qtx.InitializeChatSessionMediaTitle(ctx, db.InitializeChatSessionMediaTitleParams{
							ID: session.ID, MessageID: msg.ID, Title: title,
						}); err == nil {
							out.InitialTitle = title
						} else if !errors.Is(err, pgx.ErrNoRows) {
							return fmt.Errorf("initialize direct attachment chat title: %w", err)
						}
					}
				}
			}
		}

		if err := qtx.TouchChatSession(ctx, session.ID); err != nil {
			return fmt.Errorf("touch chat session: %w", err)
		}
		return nil
	}); err != nil {
		slog.Error("direct chat send failed",
			"chat_session_id", util.UUIDToString(session.ID),
			"agent_id", util.UUIDToString(session.AgentID),
			"error", err)
		return nil, err
	}

	slog.Info("direct chat task enqueued",
		"task_id", util.UUIDToString(out.Task.ID),
		"chat_session_id", util.UUIDToString(session.ID),
		"agent_id", util.UUIDToString(session.AgentID))
	// Notify only after commit. See EnqueueTaskForIssue for ordering rationale.
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, out.Task)
	s.NotifyTaskEnqueued(ctx, out.Task)
	return &out, nil
}

// CancelTasksForIssue cancels every active task on the issue, reconciles each
// affected agent's status, and broadcasts task:cancelled events so frontends
// clear their live cards.
//
// Callers are explicit issue-lifecycle cleanup paths only — DeleteIssue and
// BatchDeleteIssues, where the owning issue row is going away so its tasks
// must not be left orphaned. A plain status flip, `cancelled` included, no
// longer routes here (MUL-4465): cancelling an issue is not an implicit "stop
// all runs" switch. Do not re-add a status-driven caller.
//
// Before #1587 this path was "cancel rows and return", which left each affected
// agent stuck at status="working" indefinitely, requiring a manual
// `multica agent update <id> --status idle` to unwedge. It now reconciles agent
// status and broadcasts task:cancelled, matching CancelTask and RerunIssue.
func (s *TaskService) CancelTasksForIssue(ctx context.Context, issueID pgtype.UUID) error {
	var cancelled []db.AgentTaskQueue
	// The cancel and its settlement commit together: a settlement that failed
	// after the cancel committed could never be repaired, because the outbox
	// scan excludes a comment whose covering task is terminal and already holds
	// the receipt. It would neither replay nor settle — it would sit in the
	// partial index forever.
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		cancelled, err = qtx.CancelAgentTasksByIssue(ctx, issueID)
		if err != nil {
			return err
		}
		return SettleTerminalTaskState(ctx, qtx, cancelled...)
	}); err != nil {
		return err
	}
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
	}
	// Reconcile once per distinct agent instead of once per cancelled row:
	// cancelling an issue often stops several tasks owned by the same agent,
	// and each reconcile is a DB write plus a status broadcast. Matches
	// CancelTasksForAgent's single-reconcile shape (D#3319).
	for _, agentID := range distinctAgentIDs(cancelled) {
		s.ReconcileAgentStatus(ctx, agentID)
	}
	s.notifyTasksFinished(cancelled)
	return nil
}

// distinctAgentIDs returns each agent id appearing in the cancelled rows once,
// preserving first-seen order. Bulk cancellations frequently stop several tasks
// owned by the same agent; reconciling per distinct agent (rather than per row)
// collapses the redundant RefreshAgentStatusFromTasks writes and status
// broadcasts down to one per agent without changing the final agent status.
func distinctAgentIDs(cancelled []db.AgentTaskQueue) []pgtype.UUID {
	seen := make(map[pgtype.UUID]struct{}, len(cancelled))
	ids := make([]pgtype.UUID, 0, len(cancelled))
	for _, t := range cancelled {
		if _, dup := seen[t.AgentID]; dup {
			continue
		}
		seen[t.AgentID] = struct{}{}
		ids = append(ids, t.AgentID)
	}
	return ids
}

// CancelTasksForAgent cancels every active task belonging to an agent
// (queued + dispatched + running), reconciles the agent's status, and
// broadcasts task:cancelled events. Used by the agent-level "Cancel all
// tasks" action — same shape as CancelTasksForIssue but scoped on agent_id.
//
// Returns the cancelled rows so callers can report counts / log them.
func (s *TaskService) CancelTasksForAgent(ctx context.Context, agentID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	var cancelled []db.AgentTaskQueue
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		cancelled, err = qtx.CancelAgentTasksByAgent(ctx, agentID)
		if err != nil {
			return err
		}
		return SettleTerminalTaskState(ctx, qtx, cancelled...)
	}); err != nil {
		return nil, err
	}
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
	}
	// Reconcile once after the loop — agent transitions from
	// working→available based on remaining task counts, no need to call
	// per row (the rows we just cancelled all belong to the same agent).
	s.ReconcileAgentStatus(ctx, agentID)
	s.notifyTasksFinished(cancelled)
	return cancelled, nil
}

// CancelTasksByTriggerComment cancels active tasks whose planned comment batch
// contains the given edited/deleted comment. The historical method name is
// retained for call-site stability. It must run before deletion clears the
// trigger FK; the returned rows let the handler re-route every surviving input.
func (s *TaskService) CancelTasksByTriggerComment(ctx context.Context, commentID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	var cancelled []db.AgentTaskQueue
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		cancelled, err = qtx.CancelAgentTasksByTriggerComment(ctx, commentID)
		if err != nil {
			return err
		}
		return SettleTerminalTaskState(ctx, qtx, cancelled...)
	}); err != nil {
		return nil, err
	}
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
	}
	// Reconcile once per distinct agent instead of once per cancelled row: an
	// edited/deleted trigger comment can cancel several tasks owned by the same
	// agent, and each reconcile is a DB write plus a status broadcast (D#3319).
	for _, agentID := range distinctAgentIDs(cancelled) {
		s.ReconcileAgentStatus(ctx, agentID)
	}
	s.notifyTasksFinished(cancelled)
	return cancelled, nil
}

// BroadcastCancelledTasks reconciles each affected agent's status and emits
// task:cancelled for every row. Callers must invoke this AFTER committing the
// cancellation so subscribers don't observe a "cancelled" event for a row
// that the tx might still roll back.
//
// workspaceID comes from the caller instead of being resolved per task, because
// the transaction these callers have just committed can delete the row the
// resolution would read. A chat task's workspace is reached through its
// chat_session, and both DeleteChatSession and the runtime teardown remove that
// session — the teardown by deleting the system agent it hangs off. Afterwards
// ResolveTaskWorkspaceID finds nothing and returns "", and publishTaskEvent
// drops an event with no workspace before it reaches the bus: the rows are
// cancelled, nobody is told, and every queue view keeps
// showing a run that no longer exists. Each caller already knows the workspace
// — it is the one whose session, member or runtime is being torn down — so the
// lookup is not needed and cannot fail.
func (s *TaskService) BroadcastCancelledTasks(ctx context.Context, workspaceID string, cancelled []db.AgentTaskQueue) {
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		s.ReconcileAgentStatus(ctx, t.AgentID)
		if workspaceID != "" {
			s.Bus.Publish(taskEvent(protocol.EventTaskCancelled, workspaceID, t))
		}
	}
	s.notifyTasksFinished(cancelled)
}

// BroadcastTaskQueued emits a post-commit queue invalidation for clients.
func (s *TaskService) BroadcastTaskQueued(ctx context.Context, task db.AgentTaskQueue) {
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
}

// CaptureCancelledTasks records analytics for an already-committed bulk
// cancellation. It deliberately does NOT settle delegated-failure recoveries:
// by the time it runs the cancel has committed, so a failure here could not be
// rolled back and the stranded receipt could never be repaired. Callers settle
// inside the transaction that performs the cancel.
func (s *TaskService) CaptureCancelledTasks(ctx context.Context, cancelled []db.AgentTaskQueue) {
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
	}
}

type CancelledChatMessageResult struct {
	ChatSessionID  string
	MessageID      string
	Content        string
	RestoreToInput bool
	// Attachments are the rows detached from the deleted user message so they
	// survive the ON DELETE CASCADE and can re-bind when the restored draft is
	// re-sent.
	Attachments []db.Attachment
}

type CancelTaskResult struct {
	Task                 db.AgentTaskQueue
	CancelledChatMessage *CancelledChatMessageResult
}

var ErrTaskNoLongerQueued = errors.New("task is no longer queued")

// TaskCancellationActor is the point-in-time identity written onto a task when
// an authenticated member or agent explicitly stops it. Automatic paths leave
// this empty and the SQL transition records the system actor instead.
type TaskCancellationActor struct {
	Type string
	ID   pgtype.UUID
	Name string
}

// CancelTaskOptions carries what the caller knows about the client that asked
// for the cancellation.
type CancelTaskOptions struct {
	// ClientSupportsDraftRestore is true when the caller can recover a prompt
	// through the durable draft-restore path (#5219). Only such a client may be
	// handed a deferred outcome; for anyone else the empty-transcript judgment
	// stays synchronous, because the cancel response is their only chance to get
	// the prompt back. See protocol.AppCapabilityChatDraftRestoreV1.
	ClientSupportsDraftRestore bool
	// QueuedOnly turns queue edit/remove into a session-scoped compare-and-set.
	QueuedOnly          bool
	ExpectedChatSession pgtype.UUID
	QueueAction         string
	// ErrorMessage / FailureReason, when set, are persisted onto the cancelled
	// row. Only for cancellations the USER did not ask for: a user-initiated
	// cancel needs no explanation, but a server-initiated one without a
	// persisted reason surfaces as an unexplained "cancelled" whose only trace
	// is a 4xx in a daemon log the user never sees.
	ErrorMessage  string
	FailureReason string
	CancelledBy   TaskCancellationActor
	// UserInitiated distinguishes the issue UI/API cancel action from automatic
	// server repairs. An explicit user cancellation terminally acknowledges any
	// delegated-failure recovery signal planned into the task; automatic
	// cancellations must leave that signal replayable.
	UserInitiated bool
}

// CancelTask cancels a single task by ID for an automatic server path. It does
// not acknowledge delegated-failure recovery inputs; use CancelTaskByUser for
// an explicit issue-task cancellation. It broadcasts a task:cancelled event so
// frontends can update immediately.
func (s *TaskService) CancelTask(ctx context.Context, taskID pgtype.UUID) (*db.AgentTaskQueue, error) {
	// Callers of this wrapper are automatic non-chat repair/daemon paths, so
	// finalizeCancelledChatMessage returns before the gate is even read.
	// Should a chat task ever reach here, there is no client waiting on a
	// synchronous restore anyway, and the durable path is the only one that can
	// hand the prompt back at all.
	result, err := s.CancelTaskWithResult(ctx, taskID, CancelTaskOptions{ClientSupportsDraftRestore: true})
	if err != nil {
		return nil, err
	}
	return &result.Task, nil
}

// CancelTaskByUser is the explicit issue-task cancellation path. Unlike an
// automatic server cancellation, it terminally acknowledges any delegated-
// failure recovery signal carried by the task so the sweeper respects the
// user's decision instead of recreating the task.
func (s *TaskService) CancelTaskByUser(ctx context.Context, taskID pgtype.UUID, actor TaskCancellationActor) (*db.AgentTaskQueue, error) {
	result, err := s.CancelTaskWithResult(ctx, taskID, CancelTaskOptions{
		ClientSupportsDraftRestore: true,
		CancelledBy:                actor,
		UserInitiated:              true,
	})
	if err != nil {
		return nil, err
	}
	return &result.Task, nil
}

// CancelTaskWithReason cancels a task the SERVER decided to stop, persisting an
// actionable reason onto the row. It runs the same full cancellation flow as
// CancelTask — audit capture, chat settle, agent status reconcile,
// task:cancelled broadcast, NotifyTaskFinished — because a raw
// CancelAgentTaskWithReason query bypass leaves the agent pill running, the
// live card stale, and any capacity/serial waiter unwoken.
func (s *TaskService) CancelTaskWithReason(ctx context.Context, taskID pgtype.UUID, errorMessage, failureReason string) (*db.AgentTaskQueue, error) {
	result, err := s.CancelTaskWithResult(ctx, taskID, CancelTaskOptions{
		ClientSupportsDraftRestore: true,
		ErrorMessage:               errorMessage,
		FailureReason:              failureReason,
	})
	if err != nil {
		return nil, err
	}
	return &result.Task, nil
}

// CancelTaskWithResult cancels a single task and returns any chat-specific
// cleanup result needed by user-facing callers.
func (s *TaskService) CancelTaskWithResult(ctx context.Context, taskID pgtype.UUID, opts CancelTaskOptions) (*CancelTaskResult, error) {
	// Both fields are persisted onto the cancelled row's TEXT columns below, and
	// at least one caller interpolates an externally-supplied path into
	// ErrorMessage. A NUL in either rolls the cancellation back and leaves the
	// task running — the same wedge as GH #7098 on the fail/complete paths.
	opts.ErrorMessage = util.SanitizeTextForPostgres(opts.ErrorMessage)
	opts.FailureReason = util.SanitizeTextForPostgres(opts.FailureReason)
	opts.CancelledBy.Name = util.SanitizeTextForPostgres(opts.CancelledBy.Name)

	if opts.UserInitiated && (opts.ErrorMessage != "" || opts.FailureReason != "") {
		return nil, errors.New("user-initiated cancellation cannot carry a server failure reason")
	}
	if opts.UserInitiated &&
		(opts.CancelledBy.Type != "member" && opts.CancelledBy.Type != "agent") {
		return nil, errors.New("user-initiated cancellation requires a member or agent actor")
	}
	if opts.UserInitiated && !opts.CancelledBy.ID.Valid {
		return nil, errors.New("user-initiated cancellation requires an actor id")
	}
	var (
		task                 db.AgentTaskQueue
		cancelledChatMessage *CancelledChatMessageResult
		err                  error
	)
	if opts.QueuedOnly {
		if opts.QueueAction != "edit" && opts.QueueAction != "remove" {
			return nil, errors.New("queue action must be edit or remove")
		}
		err = s.runInTx(ctx, func(qtx *db.Queries) error {
			if _, err := qtx.LockChatSessionForTask(ctx, taskID); err != nil {
				return fmt.Errorf("lock queued chat session: %w", err)
			}
			task, err = qtx.CancelQueuedAgentTask(ctx, db.CancelQueuedAgentTaskParams{
				ID:              taskID,
				ChatSessionID:   opts.ExpectedChatSession,
				CancelledByType: pgtype.Text{String: opts.CancelledBy.Type, Valid: opts.CancelledBy.Type != ""},
				CancelledByID:   opts.CancelledBy.ID,
				CancelledByName: pgtype.Text{String: opts.CancelledBy.Name, Valid: opts.CancelledBy.Name != ""},
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTaskNoLongerQueued
			}
			if err != nil {
				return fmt.Errorf("cancel queued task: %w", err)
			}
			cancelledChatMessage, err = s.settleQueuedChatInput(ctx, qtx, task, opts.QueueAction)
			return err
		})
	} else {
		// The status flip and the chat resume-pointer advance commit together. Split
		// across two statements, the `cancelled` status becomes visible to every
		// other connection while the pointer still names the previous turn's
		// session, and a queued follow-up can resume that older session.
		err = s.runInTx(ctx, func(qtx *db.Queries) error {
			if err := lockChatSessionForTaskWrite(ctx, qtx, taskID); err != nil {
				return err
			}
			var (
				cancelled db.AgentTaskQueue
				err       error
			)
			if opts.UserInitiated {
				cancelled, err = qtx.CancelAgentTaskByUser(ctx, db.CancelAgentTaskByUserParams{
					ID:              taskID,
					CancelledByType: pgtype.Text{String: opts.CancelledBy.Type, Valid: true},
					CancelledByID:   opts.CancelledBy.ID,
					CancelledByName: pgtype.Text{String: opts.CancelledBy.Name, Valid: opts.CancelledBy.Name != ""},
				})
			} else if opts.ErrorMessage != "" || opts.FailureReason != "" {
				cancelled, err = qtx.CancelAgentTaskWithReason(ctx, db.CancelAgentTaskWithReasonParams{
					ID:            taskID,
					Error:         pgtype.Text{String: opts.ErrorMessage, Valid: opts.ErrorMessage != ""},
					FailureReason: pgtype.Text{String: opts.FailureReason, Valid: opts.FailureReason != ""},
				})
			} else {
				cancelled, err = qtx.CancelAgentTask(ctx, taskID)
			}
			if err != nil {
				return err
			}
			task = cancelled
			// CancelAgentTaskByUser appends the recovery receipt in the same
			// statement, so the returned row already carries it.
			if err := SettleTerminalTaskState(ctx, qtx, cancelled); err != nil {
				return err
			}
			if !cancelled.ChatSessionID.Valid {
				return nil
			}
			return qtx.AdvanceCancelledChatSessionPointer(ctx, cancelled.ID)
		})
	}
	if errors.Is(err, ErrTaskNoLongerQueued) {
		return nil, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err := s.Queries.GetAgentTask(ctx, taskID)
		if err != nil {
			return nil, fmt.Errorf("cancel task: %w", err)
		}
		return &CancelTaskResult{Task: existing}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cancel task: %w", err)
	}

	slog.Info("task cancelled", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
	s.captureTaskCancelled(ctx, task)
	if !opts.QueuedOnly {
		cancelledChatMessage = s.finalizeCancelledChatMessage(ctx, task, opts)
	}

	// Reconcile agent status
	s.ReconcileAgentStatus(ctx, task.AgentID)

	// Broadcast cancellation as a task:failed event so frontends clear the live card
	s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, task)
	s.NotifyTaskFinished(task)

	return &CancelTaskResult{
		Task:                 task,
		CancelledChatMessage: cancelledChatMessage,
	}, nil
}

// CancelQueuedChatTasks atomically cancels every queued follow-up in a chat
// session. The session lock preserves the delete path's session -> agent -> task
// order; the agent lock then prevents ClaimTask from promoting a row mid-update.
func (s *TaskService) CancelQueuedChatTasks(ctx context.Context, sessionID, agentID pgtype.UUID) error {
	var tasks []db.AgentTaskQueue
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		if _, err := qtx.LockChatSessionForDelete(ctx, sessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("lock chat session: %w", err)
		}
		if _, err := qtx.GetAgentForClaimUpdate(ctx, agentID); err != nil {
			return fmt.Errorf("lock chat agent: %w", err)
		}
		var err error
		tasks, err = qtx.CancelQueuedAgentTasksForSession(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("cancel queued chat tasks: %w", err)
		}
		if err := SettleTerminalTaskState(ctx, qtx, tasks...); err != nil {
			return err
		}
		for _, task := range tasks {
			if _, err := s.settleQueuedChatInput(ctx, qtx, task, "remove"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	for _, task := range tasks {
		slog.Info("task cancelled", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
		s.captureTaskCancelled(ctx, task)
	}
	if len(tasks) > 0 {
		s.ReconcileAgentStatus(ctx, agentID)
	}
	for _, task := range tasks {
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, task)
	}
	s.notifyTasksFinished(tasks)
	return nil
}

// lockChatSessionForTaskWrite takes the chat_session row a task belongs to. It
// must be the FIRST statement of any transaction that ends up holding both that
// session row and the task's own row, which is every terminal-state path a chat
// task has: complete, fail, cancel, the cancelled-turn finalize, and the
// daemon's mid-flight pin.
//
// chat_session -> agent_task_queue is the repo-wide order (see
// LockChatSessionForTask in chat.sql). DeleteChatSession and
// FinalizeDeferredCancelledChat already took it; the terminal reports did not,
// because they only ever wrote the task row first and the session second and
// nothing else contended for both. Once the cancel and pin paths started
// holding the session row too, "task first" and "session first" existed side by
// side and any crossing pair could deadlock — PostgreSQL aborts one with 40P01
// and runInTx has no retry. There is no per-path fix for that: either every
// writer agrees or none of them are safe.
//
// ErrNoRows means there is nothing to lock — a non-chat task, or one whose
// session was already deleted (the FK NULLs the column) — and the caller then
// has no session write to protect either.
func lockChatSessionForTaskWrite(ctx context.Context, qtx *db.Queries, taskID pgtype.UUID) error {
	if _, err := qtx.LockChatSessionForTask(ctx, taskID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock chat session for task write: %w", err)
	}
	return nil
}

// chatInputOwnerID resolves the id the task's user-message input batch is
// keyed on: chat_input_task_id when set (auto-retry clones inherit their
// parent's, so provenance checks reach the parent's sealed messages), falling
// back to the task's own id for legacy rows.
func chatInputOwnerID(task db.AgentTaskQueue) pgtype.UUID {
	if task.ChatInputTaskID.Valid {
		return task.ChatInputTaskID
	}
	return task.ID
}

// createAssistantChatMessage writes an assistant outcome and reanchors the
// newly-visible queued direct head in the caller's transaction. Keeping the two
// statements together is the transcript-order boundary: readers must not see
// the reply commit while the next head still carries its older enqueue time.
// The caller MUST observe the settling task outside the visible-head status set
// before invoking this helper; completion and failure settle it earlier in the
// same transaction, while cancellation commits that status in its prior
// transaction (#5219). Otherwise the head query still selects the settling task
// and reanchoring its successor is a no-op.
func createAssistantChatMessage(ctx context.Context, qtx *db.Queries, params db.CreateChatMessageParams) (db.ChatMessage, error) {
	if params.Role != "assistant" {
		return db.ChatMessage{}, fmt.Errorf("create assistant chat message: invalid role %q", params.Role)
	}
	row, err := qtx.CreateChatMessage(ctx, params)
	if err != nil {
		return db.ChatMessage{}, err
	}
	if err := qtx.ReanchorNextQueuedDirectChatInput(ctx, db.ReanchorNextQueuedDirectChatInputParams{
		AssistantCreatedAt: row.CreatedAt,
		ChatSessionID:      row.ChatSessionID,
	}); err != nil {
		return db.ChatMessage{}, fmt.Errorf("reanchor next queued direct chat input: %w", err)
	}
	return row, nil
}

func (s *TaskService) settleQueuedChatInput(
	ctx context.Context,
	qtx *db.Queries,
	task db.AgentTaskQueue,
	action string,
) (*CancelledChatMessageResult, error) {
	if !task.ChatSessionID.Valid {
		return nil, nil
	}
	inputOwnerID := chatInputOwnerID(task)
	var detached []db.Attachment
	var err error
	if action == "edit" {
		detached, err = qtx.DetachAttachmentsFromUserChatMessageByTask(ctx, inputOwnerID)
		if err != nil {
			return nil, fmt.Errorf("detach edited queued chat attachments: %w", err)
		}
	}
	deleted, err := deleteUserChatInput(ctx, qtx, inputOwnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("delete queued chat input: %w", err)
	}
	cancelled := &CancelledChatMessageResult{
		ChatSessionID: util.UUIDToString(deleted.ChatSessionID),
		MessageID:     util.UUIDToString(deleted.ID),
		Content:       deleted.Content,
	}
	if action == "remove" {
		return cancelled, nil
	}

	attachmentIDs := make([]pgtype.UUID, 0, len(detached))
	for _, attachment := range detached {
		attachmentIDs = append(attachmentIDs, attachment.ID)
	}
	if _, err := qtx.CreateChatDraftRestore(ctx, db.CreateChatDraftRestoreParams{
		ID:            deleted.ID,
		ChatSessionID: task.ChatSessionID,
		TaskID:        task.ID,
		Content:       deleted.Content,
		AttachmentIds: attachmentIDs,
	}); err != nil {
		return nil, fmt.Errorf("create queued chat draft restore: %w", err)
	}
	cancelled.RestoreToInput = true
	cancelled.Attachments = detached
	return cancelled, nil
}

// deleteUserChatInput removes a cancelled/edited turn's member-typed input.
func deleteUserChatInput(ctx context.Context, qtx *db.Queries, inputOwnerID pgtype.UUID) (db.ChatMessage, error) {
	return qtx.DeleteUserChatMessageByTask(ctx, inputOwnerID)
}

func (s *TaskService) finalizeCancelledChatMessage(ctx context.Context, task db.AgentTaskQueue, opts CancelTaskOptions) *CancelledChatMessageResult {
	if !task.ChatSessionID.Valid {
		return nil
	}
	var cancelled *CancelledChatMessageResult
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		// Same protocol as every other terminal path: this transaction marks the
		// task row and then writes chat_message rows, whose FK takes a KEY SHARE
		// lock on the session — two rows again, so it takes the session first.
		if err := lockChatSessionForTaskWrite(ctx, qtx, task.ID); err != nil {
			return err
		}
		hasMessages, err := qtx.HasTaskMessages(ctx, task.ID)
		if err != nil {
			return fmt.Errorf("list cancelled chat task messages: %w", err)
		}
		restorable := !hasMessages
		if restorable && task.StartedAt.Valid && opts.ClientSupportsDraftRestore {
			// A started task's daemon learns of the cancellation by polling
			// and may still be flushing its transcript tail, so "empty" is
			// not trustworthy yet. Defer the judgment until the daemon acks
			// its flush (cancel-ack) or the sweeper grace period expires
			// (#5219). "Non-empty" needs no deferral: late rows only append.
			//
			// Deferring is gated on the client: clients and server do not
			// upgrade together, and a client that cannot read the durable
			// restore would take an empty cancel response as "nothing to put
			// back" and lose the prompt. Such a client falls through to the
			// legacy synchronous branch below — it keeps the pre-#5219 race
			// (an in-flight transcript tail can still be misjudged as empty),
			// which is exactly the behaviour it has against an old server, and
			// strictly better than dropping the input.
			if _, err := qtx.MarkChatFinalizeDeferred(ctx, task.ID); err != nil {
				return fmt.Errorf("mark chat finalize deferred: %w", err)
			}
			return nil
		}
		if restorable {
			inputOwnerID := chatInputOwnerID(task)
			// Detach attachments BEFORE deleting the user message — the
			// attachment FK is ON DELETE CASCADE, so deleting first would
			// destroy rows the restored draft needs to re-bind.
			detached, err := qtx.DetachAttachmentsFromUserChatMessageByTask(ctx, inputOwnerID)
			if err != nil {
				return fmt.Errorf("detach cancelled chat message attachments: %w", err)
			}
			deleted, err := deleteUserChatInput(ctx, qtx, inputOwnerID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("delete empty cancelled chat user message: %w", err)
			}
			// Always restorable now: the delete cannot return a kickoff row, so
			// what comes back is always what the member typed (MUL-5827).
			cancelled = &CancelledChatMessageResult{
				ChatSessionID:  util.UUIDToString(deleted.ChatSessionID),
				MessageID:      util.UUIDToString(deleted.ID),
				Content:        deleted.Content,
				RestoreToInput: true,
				Attachments:    detached,
			}
			return nil
		}
		if _, err := createAssistantChatMessage(ctx, qtx, db.CreateChatMessageParams{
			ID:            dbid.NewV7(),
			ChatSessionID: task.ChatSessionID,
			Role:          "assistant",
			Content:       "Stopped.",
			TaskID:        task.ID,
			ElapsedMs:     computeChatElapsedMs(task),
		}); err != nil {
			return fmt.Errorf("create cancelled chat message: %w", err)
		}
		return nil
	}); err != nil {
		slog.Error("failed to finalize cancelled chat message",
			"task_id", util.UUIDToString(task.ID),
			"chat_session_id", util.UUIDToString(task.ChatSessionID),
			"error", err,
		)
		return nil
	}
	return cancelled
}

// FinalizeDeferredCancelledChat settles the empty/non-empty judgment that
// finalizeCancelledChatMessage deferred for a started-but-empty cancelled
// chat task (#5219). Called from the daemon's cancel-ack (transcript flush
// complete) and from the sweeper grace-period fallback; the marker claim is
// atomic, so concurrent callers cannot finalize the same task twice and a
// call with no pending marker is a no-op. The settled outcome is broadcast
// as chat:cancel_finalized since the cancel HTTP response has long returned.
// RebroadcastCancelledTask re-announces an already-cancelled task after a
// post-terminal delivery landed on its row (the cancel-ack's branch name or
// preserved-worktree error). The original task:cancelled broadcast fired at
// cancel time, BEFORE the daemon's ack — clients may have refetched a row
// without the delivery and will not refetch again on their own. Consumers
// treat task:cancelled as idempotent cache invalidation, so a replay is safe.
func (s *TaskService) RebroadcastCancelledTask(ctx context.Context, taskID pgtype.UUID) {
	task, err := s.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		slog.Warn("rebroadcast cancelled task: load failed",
			"task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	if task.Status != "cancelled" {
		// A complete/fail callback already announced its own terminal event
		// carrying the row's final fields; nothing stale to refresh.
		return
	}
	s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, task)
}

func (s *TaskService) FinalizeDeferredCancelledChat(ctx context.Context, taskID pgtype.UUID) bool {
	var (
		task    db.AgentTaskQueue
		payload protocol.ChatCancelFinalizedPayload
		changed bool
		settled bool
	)
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		// Lock the task's chat_session first. chat_draft_restore has no FK
		// (MUL-3515), so the insert below takes no lock of its own on the
		// session — without this, a workspace/agent/session delete that swept
		// the table just before we commit would leave our restore row (holding
		// the user's prompt) orphaned forever. The deleters take the same lock
		// before their sweep, so one of us blocks: either they wait and their
		// sweep sees our row, or we wait and find no session left to restore
		// into. Locking the session BEFORE the task claim also fixes the global
		// lock order (chat_session -> agent_task_queue) that keeps this from
		// deadlocking against the deleters' cascade.
		_, err := qtx.LockChatSessionForTask(ctx, taskID)
		sessionGone := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !sessionGone {
			return fmt.Errorf("lock chat session for deferred finalize: %w", err)
		}

		// Claim the marker inside the settlement tx: a failed settlement then
		// rolls the claim back so the sweeper can retry, instead of leaving the
		// task with a cleared marker and no finalized outcome. The row lock
		// still serializes the daemon ack and the sweeper — the loser's UPDATE
		// blocks until the winner commits, then matches no row (ErrNoRows) — so
		// the same task is never finalized twice.
		claimed, err := qtx.ClaimChatFinalizeDeferred(ctx, taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim deferred chat finalize: %w", err)
		}
		changed = true
		task = claimed
		if sessionGone {
			// The session cascaded away (its FK NULLs the column below anyway):
			// there is no transcript to settle and nowhere to put a restore. The
			// claim above still cleared the marker, so the sweeper stops retrying.
			return nil
		}
		if !claimed.ChatSessionID.Valid {
			return nil
		}
		settled = true
		payload.ChatSessionID = util.UUIDToString(claimed.ChatSessionID)
		payload.TaskID = util.UUIDToString(claimed.ID)
		payload.InitiatorUserID = util.UUIDToString(claimed.InitiatorUserID)

		hasMessages, err := qtx.HasTaskMessages(ctx, claimed.ID)
		if err != nil {
			return fmt.Errorf("list cancelled chat task messages: %w", err)
		}
		restorable := !hasMessages
		if restorable {
			inputOwnerID := chatInputOwnerID(claimed)
			// The transcript stayed empty through the daemon flush: same
			// outcome as the synchronous empty branch, but the cancel HTTP
			// response is long gone and the broadcast is best-effort. The
			// restore is persisted in this same tx and served by the
			// creator-authorized draft-restores endpoint, so a client that
			// misses the event recovers it on the next session open; the
			// event itself carries no content and is only an invalidation
			// hint.
			detached, err := qtx.DetachAttachmentsFromUserChatMessageByTask(ctx, inputOwnerID)
			if err != nil {
				return fmt.Errorf("detach cancelled chat message attachments: %w", err)
			}
			deleted, err := deleteUserChatInput(ctx, qtx, inputOwnerID)
			if errors.Is(err, pgx.ErrNoRows) {
				payload.Outcome = ""
				return nil
			}
			if err != nil {
				return fmt.Errorf("delete empty cancelled chat user message: %w", err)
			}
			attachmentIDs := make([]pgtype.UUID, 0, len(detached))
			for _, a := range detached {
				attachmentIDs = append(attachmentIDs, a.ID)
			}
			if _, err := qtx.CreateChatDraftRestore(ctx, db.CreateChatDraftRestoreParams{
				ID:            deleted.ID,
				ChatSessionID: claimed.ChatSessionID,
				TaskID:        claimed.ID,
				Content:       deleted.Content,
				AttachmentIds: attachmentIDs,
			}); err != nil {
				return fmt.Errorf("create chat draft restore: %w", err)
			}
			payload.Outcome = protocol.ChatCancelOutcomeRestored
			payload.MessageID = util.UUIDToString(deleted.ID)
			return nil
		}
		row, err := createAssistantChatMessage(ctx, qtx, db.CreateChatMessageParams{
			ID:            dbid.NewV7(),
			ChatSessionID: claimed.ChatSessionID,
			Role:          "assistant",
			Content:       "Stopped.",
			TaskID:        claimed.ID,
			ElapsedMs:     computeChatElapsedMs(claimed),
		})
		if err != nil {
			return fmt.Errorf("create cancelled chat message: %w", err)
		}
		payload.Outcome = protocol.ChatCancelOutcomeStopped
		payload.MessageID = util.UUIDToString(row.ID)
		payload.Content = row.Content
		payload.MessageKind = row.MessageKind
		if row.CreatedAt.Valid {
			payload.CreatedAt = row.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		if row.ElapsedMs.Valid {
			payload.ElapsedMs = row.ElapsedMs.Int64
		}
		return nil
	}); err != nil {
		slog.Error("failed to finalize deferred cancelled chat",
			"task_id", util.UUIDToString(taskID),
			"error", err,
		)
		return false
	}
	if !settled || payload.Outcome == "" {
		return changed
	}
	s.broadcastChatCancelFinalized(ctx, task, payload)
	return changed
}

func (s *TaskService) broadcastChatCancelFinalized(ctx context.Context, task db.AgentTaskQueue, payload protocol.ChatCancelFinalizedPayload) {
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	if workspaceID == "" {
		return
	}
	s.Bus.Publish(events.Event{
		Type:          protocol.EventChatCancelFinalized,
		WorkspaceID:   workspaceID,
		ActorType:     "system",
		ActorID:       "",
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		Payload:       payload,
	})
}

// errIssueBusyClaim reports a claim that lost the one-run-per-issue race.
var errIssueBusyClaim = errors.New("issue already has an active task")

const oneActiveTaskPerIssueIndex = "agent_task_queue_one_active_per_issue_idx"

func isIssueBusyClaimConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == oneActiveTaskPerIssueIndex
}

// ClaimTask atomically claims the next queued task for an agent on its current
// runtime, respecting max_concurrent_tasks.
func (s *TaskService) ClaimTask(ctx context.Context, agentID pgtype.UUID) (*db.AgentTaskQueue, error) {
	return s.claimTask(ctx, agentID, pgtype.UUID{})
}

// claimTask is the runtime-scoped claim primitive used by daemon poll paths.
// The exported ClaimTask wrapper omits runtimeID and therefore resolves the
// agent's currently bound runtime. Scoping the SQL claim itself prevents an
// offline candidate on runtime A from causing the same agent's task on runtime
// B to be dispatched and then dropped by the caller's runtime guard.
func (s *TaskService) claimTask(ctx context.Context, agentID, runtimeID pgtype.UUID) (*db.AgentTaskQueue, error) {
	start := time.Now()
	outcome := "unknown"
	var getAgentMs, countRunningMs, claimAgentMs, reanchorMs, updateStatusMs, dispatchMs int64
	var claimed *db.AgentTaskQueue
	var reclaimCheckAfter time.Time
	defer func() {
		s.maybeLogClaimSlow(agentID, outcome, start, getAgentMs, countRunningMs, claimAgentMs, reanchorMs, updateStatusMs, dispatchMs)
	}()

	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		t0 := time.Now()
		agent, err := qtx.GetAgentForClaimUpdate(ctx, agentID)
		getAgentMs = time.Since(t0).Milliseconds()
		if err != nil {
			outcome = "error_get_agent"
			return fmt.Errorf("agent not found: %w", err)
		}
		claimRuntimeID := runtimeID
		if !claimRuntimeID.Valid {
			claimRuntimeID = agent.RuntimeID
		}
		if !claimRuntimeID.Valid {
			outcome = "no_runtime"
			return nil
		}
		// A daemon may still hold a stale candidate after the agent is rebound.
		// Reject it before doing capacity work. ClaimAgentTask repeats the fence
		// before its state transition; the claim handler then rechecks the freshly
		// loaded Agent before returning any payload. Runtime mutation teardown is
		// responsible for serializing and settling the remaining queued rows.
		if runtimeID.Valid && agent.RuntimeID != runtimeID {
			outcome = "runtime_mismatch"
			return nil
		}

		t0 = time.Now()
		running, err := qtx.CountRunningTasks(ctx, agentID)
		countRunningMs = time.Since(t0).Milliseconds()
		if err != nil {
			outcome = "error_count_running"
			return fmt.Errorf("count running tasks: %w", err)
		}
		if running >= int64(agent.MaxConcurrentTasks) {
			slog.Debug("task claim: no capacity", "agent_id", util.UUIDToString(agentID), "running", running, "max", agent.MaxConcurrentTasks)
			outcome = "no_capacity"
			return nil
		}

		t0 = time.Now()
		reclaimCheckAfter = t0.Add(claimResponseRecoveryWindow + ReclaimCheckHintSafetyMargin)
		task, err := qtx.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
			AgentID:          agentID,
			RuntimeID:        claimRuntimeID,
			PrepareLeaseSecs: prepareLeaseDuration.Seconds(),
			RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
		})
		claimAgentMs = time.Since(t0).Milliseconds()
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				slog.Debug("task claim: no tasks available", "agent_id", util.UUIDToString(agentID))
				outcome = "no_tasks"
				return nil
			}
			if isIssueBusyClaimConflict(err) {
				// Another runtime claimed a task for the same issue in parallel.
				// The aborted transaction must roll back; the next poll sees the
				// issue busy and skips it.
				outcome = "issue_busy"
				return errIssueBusyClaim
			}
			outcome = "error_claim"
			return fmt.Errorf("claim task: %w", err)
		}

		// An idle task-owned direct-chat row may already be visible as the
		// positional queue head. Normal completion reanchors a successor beside
		// the assistant outcome before commit; this claim-time query is the
		// compatibility fallback for an older or otherwise out-of-order row.
		// It only moves that row inside the claim transaction, so full-list and
		// cursor readers see the corrected boundary once it is dispatched. Retry
		// children keep the root input owner and stale-dispatch reclaim uses a
		// separate query, so neither path moves an already-visible turn again.
		if task.ChatSessionID.Valid && task.ChatInputTaskID.Valid && task.ChatInputTaskID == task.ID {
			t0 = time.Now()
			if err := qtx.ReanchorClaimedDirectChatInput(ctx, db.ReanchorClaimedDirectChatInputParams{
				DispatchedAt: task.DispatchedAt,
				TaskID:       task.ID,
			}); err != nil {
				reanchorMs = time.Since(t0).Milliseconds()
				outcome = "error_reanchor_chat_input"
				return fmt.Errorf("reanchor claimed direct chat input: %w", err)
			}
			reanchorMs = time.Since(t0).Milliseconds()
		}

		claimedTask := task
		claimed = &claimedTask
		return nil
	})
	if errors.Is(err, errIssueBusyClaim) {
		return nil, nil
	}
	if err != nil {
		if outcome == "unknown" {
			outcome = "error_transaction"
		}
		return nil, err
	}
	if claimed == nil {
		return nil, nil
	}
	s.trackTaskForReclaim(*claimed, reclaimCheckAfter)

	slog.Info("task claimed", "task_id", util.UUIDToString(claimed.ID), "agent_id", util.UUIDToString(agentID))
	s.captureTaskDispatched(ctx, *claimed)

	// Refresh agent status from active tasks. This avoids a stale unconditional
	// working write racing after a just-cancelled claim.
	t0 := time.Now()
	s.ReconcileAgentStatus(ctx, agentID)
	updateStatusMs = time.Since(t0).Milliseconds()

	// Broadcast task:dispatch. ResolveTaskWorkspaceID inside this path can
	// re-query issue/chat_session, so it can also be a real
	// contributor to claim latency.
	t0 = time.Now()
	s.broadcastTaskDispatch(ctx, *claimed)
	dispatchMs = time.Since(t0).Milliseconds()

	outcome = "claimed"
	return claimed, nil
}

// ClaimTaskForRuntime claims the next runnable task for a runtime while
// still respecting each agent's max_concurrent_tasks limit.
//
// Empty-claim fast path: when EmptyClaim is configured and a recent
// check verified the runtime had no queued tasks, returns immediately
// without touching Postgres. The cache is invalidated synchronously on
// every enqueue (notifyTaskAvailable), so a queued task becomes
// claimable on the next call rather than waiting for the TTL.
func (s *TaskService) ClaimTaskForRuntime(ctx context.Context, runtimeID pgtype.UUID) (*db.AgentTaskQueue, error) {
	start := time.Now()
	var (
		outcome          = "no_task"
		listMs, loopMs   int64
		listCount, tried int
		claimedFlag      bool
	)
	defer func() {
		totalMs := time.Since(start).Milliseconds()
		if totalMs < 300 {
			return
		}
		slog.Info("claim_for_runtime slow",
			"runtime_id", util.UUIDToString(runtimeID),
			"outcome", outcome,
			"total_ms", totalMs,
			"list_pending_ms", listMs,
			"list_pending_count", listCount,
			"agents_tried", tried,
			"claim_loop_ms", loopMs,
			"claimed", claimedFlag,
		)
	}()

	runtimeKey := util.UUIDToString(runtimeID)
	if err := s.PromoteDueDeferredTasksForRuntime(ctx, runtimeID); err != nil {
		outcome = "error_promote_deferred"
		return nil, err
	}

	// Keep stale-response recovery before EmptyClaim because the queued-only
	// cache cannot represent dispatched work. ReclaimCheck independently skips
	// the UPDATE until a task hint or bounded DB backstop is due.
	checkStarted := time.Now()
	if due := s.ReclaimCheck.DueRuntimeIDs(ctx, []string{runtimeKey}, checkStarted); len(due) > 0 {
		reclaimCheckAfter := time.Now().Add(claimResponseRecoveryWindow + ReclaimCheckHintSafetyMargin)
		stale, err := s.Queries.ReclaimStaleDispatchedTaskForRuntime(ctx, db.ReclaimStaleDispatchedTaskForRuntimeParams{
			RuntimeID:         runtimeID,
			ClaimRecoverySecs: claimResponseRecoveryWindow.Seconds(),
			PrepareLeaseSecs:  prepareLeaseDuration.Seconds(),
			RuntimeStaleSecs:  RuntimeClaimFreshnessSeconds,
		})
		if err == nil {
			s.ReclaimCheck.MarkChecked(
				ctx,
				[]string{runtimeKey},
				checkStarted,
				time.Now().Add(ReclaimCheckRetryInterval),
			)
			s.trackTaskForReclaim(stale, reclaimCheckAfter)
			outcome = "reclaimed_dispatched"
			claimedFlag = true
			slog.Info("stale dispatched task reclaimed",
				"task_id", util.UUIDToString(stale.ID),
				"runtime_id", runtimeKey,
				"agent_id", util.UUIDToString(stale.AgentID),
			)
			return &stale, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			outcome = "error_reclaim_dispatched"
			return nil, fmt.Errorf("reclaim stale dispatched task: %w", err)
		}
		s.ReclaimCheck.MarkChecked(
			ctx,
			[]string{runtimeKey},
			checkStarted,
			time.Now().Add(ReclaimCheckRetryInterval),
		)
	}

	if s.EmptyClaim.IsEmpty(ctx, runtimeKey) {
		outcome = "empty_cache_hit"
		return nil, nil
	}

	// Sample the invalidation version BEFORE the SELECT. If a
	// concurrent enqueue Bumps between this read and the post-SELECT
	// MarkEmpty, the next IsEmpty will see the empty key tagged with
	// a stale version and reject it — closing the race that would
	// otherwise stall the just-queued task until the empty key's TTL
	// expired.
	preSelectVersion := s.EmptyClaim.CurrentVersion(ctx, runtimeKey)

	t0 := time.Now()
	tasks, err := s.Queries.ListQueuedClaimCandidatesByRuntime(ctx, runtimeID)
	listMs = time.Since(t0).Milliseconds()
	listCount = len(tasks)
	if err != nil {
		outcome = "error_list"
		return nil, fmt.Errorf("list queued claim candidates: %w", err)
	}

	if len(tasks) == 0 {
		s.EmptyClaim.MarkEmpty(ctx, runtimeKey, preSelectVersion)
		outcome = "empty_db"
		return nil, nil
	}

	loopStart := time.Now()
	triedAgents := map[string]struct{}{}
	var claimed *db.AgentTaskQueue
	for _, candidate := range tasks {
		agentKey := util.UUIDToString(candidate.AgentID)
		if _, seen := triedAgents[agentKey]; seen {
			continue
		}
		triedAgents[agentKey] = struct{}{}
		tried++

		task, err := s.claimTask(ctx, candidate.AgentID, runtimeID)
		if err != nil {
			loopMs = time.Since(loopStart).Milliseconds()
			outcome = "error_claim"
			return nil, err
		}
		if task != nil && task.RuntimeID == runtimeID {
			claimed = task
			break
		}
	}
	loopMs = time.Since(loopStart).Milliseconds()
	if claimed != nil {
		claimedFlag = true
		outcome = "claimed"
	}

	return claimed, nil
}

// ErrClaimDeliveryAuthz signals that the final delivery gate rejected the
// claimed task: the current agent/runtime authorization no longer holds at the
// delivery boundary. The task is settled by the caller through the existing
// FailTask path; no claim payload is dispatched.
type ClaimDeliveryAuthzError struct {
	Reason string
	Detail string
}

func (e *ClaimDeliveryAuthzError) Error() string {
	return "claim delivery authorization failed: " + e.Reason + ": " + e.Detail
}

// FinalizeTaskClaim atomically persists the task-scoped agent token, the
// comparable issue state this payload was built from, and, for a comment-backed
// task, the exact comment ids embedded in the response. The handler must call
// this only after the full payload has been built and before writing any
// response bytes. A failure rolls every write back so the claim can be safely
// returned to the queue.
//
// issueSnapshot is empty for tasks with no issue (chat) and for an issue
// claim whose snapshot could not be encoded; the
// write is then skipped and the NEXT run on that issue reports "not compared"
// rather than a wrong "unchanged" (MUL-7344). Unlike the comment receipt it is
// NOT gated on the task being comment-backed: an assignment run that recorded
// no snapshot leaves the following comment-triggered run with no baseline.
//
// The optional authorize closure runs INSIDE the same transaction, after the
// gate has re-read the current runtime row under a FOR UPDATE row lock. That
// makes the authorization decision and the task-token write one
// atomic unit: a concurrent runtime re-registration that would change owner_id
// blocks until the gate commits, so the owner the gate authorized against is
// the owner the token was minted for — no stale-snapshot delivery window.
// The closure receives the in-transaction token params so it can normalize
// identity fields from the locked rows before the token is inserted. It
// returns a *ClaimDeliveryAuthzError to reject delivery (every other error
// rolls the claim back like any other finalize failure).
func (s *TaskService) FinalizeTaskClaim(
	ctx context.Context,
	task db.AgentTaskQueue,
	token db.CreateTaskTokenParams,
	deliveredCommentIDs []pgtype.UUID,
	recordCommentReceipt bool,
	authorize func(qtx *db.Queries, token *db.CreateTaskTokenParams) error,
	issueSnapshot []byte,
) ([]pgtype.UUID, error) {
	receipt := task.DeliveredCommentIds
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		if authorize != nil {
			if err := authorize(qtx, &token); err != nil {
				return fmt.Errorf("authorize claim delivery: %w", err)
			}
		}
		if _, err := qtx.CreateTaskToken(ctx, token); err != nil {
			return fmt.Errorf("create task token: %w", err)
		}
		if len(issueSnapshot) > 0 {
			// Same CAS columns as the receipt below, so a stale handler cannot
			// write a snapshot over a newer reclaim's, or after the run has
			// started. A no-op update (0 rows) is not an error: it means this
			// claim generation is no longer current, and the newer one records
			// its own snapshot.
			if err := qtx.SetTaskIssueSnapshot(ctx, db.SetTaskIssueSnapshotParams{
				IssueSnapshot: issueSnapshot,
				TaskID:        task.ID,
				RuntimeID:     task.RuntimeID,
				DispatchedAt:  task.DispatchedAt,
			}); err != nil {
				return fmt.Errorf("set task issue snapshot: %w", err)
			}
		}
		if !recordCommentReceipt {
			return nil
		}
		persisted, err := qtx.SetTaskDeliveredCommentIDs(ctx, db.SetTaskDeliveredCommentIDsParams{
			DeliveredCommentIds:      deliveredCommentIDs,
			TaskID:                   task.ID,
			RuntimeID:                task.RuntimeID,
			DispatchedAt:             task.DispatchedAt,
			ExpectedTriggerCommentID: task.TriggerCommentID,
		})
		if err != nil {
			return fmt.Errorf("set delivered comment ids: %w", err)
		}
		receipt = persisted
		return nil
	})
	if err != nil {
		return nil, err
	}
	return receipt, nil
}

// RequeueTaskAfterClaimFailure immediately releases an exact dispatched claim
// whose payload finalization failed before the HTTP response was written. The
// SQL CAS includes dispatched_at so a late handler cannot roll back a newer
// reclaim. This is not a fresh enqueue: do not duplicate queued analytics.
func (s *TaskService) RequeueTaskAfterClaimFailure(ctx context.Context, task db.AgentTaskQueue) (*db.AgentTaskQueue, error) {
	requeued, err := s.Queries.RequeueAgentTaskAfterClaimFailure(ctx, db.RequeueAgentTaskAfterClaimFailureParams{
		TaskID:       task.ID,
		RuntimeID:    task.RuntimeID,
		DispatchedAt: task.DispatchedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("requeue task after claim failure: %w", err)
	}
	s.forgetTaskReclaim(requeued)
	s.ReconcileAgentStatus(ctx, requeued.AgentID)
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, requeued)
	s.notifyTaskAvailable(requeued)
	slog.Info("task requeued after claim finalization failure",
		"task_id", util.UUIDToString(requeued.ID),
		"runtime_id", util.UUIDToString(requeued.RuntimeID),
	)
	return &requeued, nil
}

// ClaimTasksForRuntimes is the machine-level (MUL-4257) batch counterpart of
// ClaimTaskForRuntime: it claims up to maxTasks tasks across every runtime in
// runtimeIDs in a single call, so a daemon can poll for all of its runtimes
// with one HTTP request and a constant number of DB queries instead of one
// request (and one promote/reclaim/list cycle) per runtime.
//
// It preserves the exact per-runtime semantics, just set-ified:
//  1. promote due deferred tasks across the set (one UPDATE);
//  2. reclaim up to maxTasks stale-dispatched tasks across the set (one UPDATE)
//     — done before the empty-cache check because a lost claim response moves
//     the task out of `queued`, which the empty-queued cache cannot represent;
//  3. short-circuit runtimes whose empty-claim verdict is cached, sampling the
//     invalidation version for the rest BEFORE the candidate SELECT;
//  4. list queued candidates across the non-empty set (one SELECT);
//  5. mark still-empty runtimes so their next idle poll skips Postgres;
//  6. claim per distinct agent via the runtime-scoped helper, preserving
//     per-(issue, agent) serialization, the agent concurrency cap, and every
//     dispatch side effect until maxTasks is reached.
//
// The returned slice contains both reclaimed and freshly-claimed tasks, each
// already carrying its runtime_id so the daemon routes it to the matching
// runtime locally.
func (s *TaskService) ClaimTasksForRuntimes(ctx context.Context, runtimeIDs []pgtype.UUID, maxTasks int) ([]db.AgentTaskQueue, error) {
	if len(runtimeIDs) == 0 || maxTasks <= 0 {
		return nil, nil
	}

	// De-dup runtime IDs defensively so MarkEmpty/version bookkeeping stays
	// unambiguous even if a daemon ever sends a duplicate.
	seen := make(map[string]struct{}, len(runtimeIDs))
	uniqueIDs := make([]pgtype.UUID, 0, len(runtimeIDs))
	runtimeInSet := make(map[string]struct{}, len(runtimeIDs))
	for _, rid := range runtimeIDs {
		key := util.UUIDToString(rid)
		runtimeInSet[key] = struct{}{}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		uniqueIDs = append(uniqueIDs, rid)
	}

	claimed := make([]db.AgentTaskQueue, 0, maxTasks)

	// 1. Promote due deferred tasks across the whole set (promote-first, like
	// the singular path). Replay the per-row side effects the singular service
	// method PromoteDueDeferredTasksForRuntime performs — crucially
	// EmptyClaim.Bump (via NotifyTaskEnqueued → notifyTaskAvailable) so a
	// just-promoted deferred task invalidates its runtime's cached empty
	// verdict BEFORE the empty-cache filter in step 3; otherwise a stale
	// MarkEmpty from a prior idle poll would short-circuit the runtime and the
	// promoted task would sit unclaimed until the empty key's TTL. Also emits
	// the deferred→queued UI event and the enqueue analytics sample.
	s.cancelSupersededDeferredRetries(ctx, uniqueIDs)
	promoted, err := s.Queries.PromoteDueDeferredTasksForRuntimes(ctx, db.PromoteDueDeferredTasksForRuntimesParams{
		RuntimeIds:       uniqueIDs,
		RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
	})
	if isDuplicatePendingTaskErr(err) {
		// Same tolerance as the single-runtime path, and it matters more here:
		// one contended row would otherwise fail the claim for EVERY runtime in
		// the batch. Promote nothing this tick and let the claim continue.
		slog.Info("promote deferred tasks (batch): slot taken by a concurrent enqueue, skipping this tick")
		promoted = nil
	} else if err != nil {
		return nil, fmt.Errorf("promote deferred tasks: %w", err)
	}
	for _, task := range promoted {
		slog.Info("deferred task promoted (batch)",
			"task_id", util.UUIDToString(task.ID),
			"runtime_id", util.UUIDToString(task.RuntimeID),
			"agent_id", util.UUIDToString(task.AgentID),
		)
		s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
		s.NotifyTaskEnqueued(ctx, task)
	}

	// 2. Reclaim lost-response dispatched tasks when any runtime's task schedule
	// or bounded DB backstop is due. The query always receives the complete
	// machine-level set so per-runtime backstops stay aligned and fixed UPDATE
	// setup/locking cost is paid at a predictable cadence. A nil/missing/failed
	// cache preserves the historical query path.
	runtimeKeys := make([]string, 0, len(uniqueIDs))
	for _, rid := range uniqueIDs {
		runtimeKeys = append(runtimeKeys, util.UUIDToString(rid))
	}
	checkStarted := time.Now()
	dueKeys := s.ReclaimCheck.DueRuntimeIDs(ctx, runtimeKeys, checkStarted)
	var reclaimed []db.AgentTaskQueue
	var reclaimCheckAfter time.Time
	if len(dueKeys) > 0 {
		reclaimCheckAfter = time.Now().Add(claimResponseRecoveryWindow + ReclaimCheckHintSafetyMargin)
		reclaimed, err = s.Queries.ReclaimStaleDispatchedTasksForRuntimes(ctx, db.ReclaimStaleDispatchedTasksForRuntimesParams{
			RuntimeIds:        uniqueIDs,
			ClaimRecoverySecs: claimResponseRecoveryWindow.Seconds(),
			PrepareLeaseSecs:  prepareLeaseDuration.Seconds(),
			RuntimeStaleSecs:  RuntimeClaimFreshnessSeconds,
			MaxTasks:          int32(maxTasks),
		})
		if err != nil {
			return nil, fmt.Errorf("reclaim stale dispatched tasks: %w", err)
		}
		// The UPDATE's fixed setup/locking cost dominates array width. Query and
		// advance the complete machine-level set together so per-runtime backstops
		// cannot drift into one UPDATE on almost every daemon poll.
		s.ReclaimCheck.MarkChecked(
			ctx,
			runtimeKeys,
			checkStarted,
			time.Now().Add(ReclaimCheckRetryInterval),
		)
	}
	for i := range reclaimed {
		s.trackTaskForReclaim(reclaimed[i], reclaimCheckAfter)
		claimed = append(claimed, reclaimed[i])
		slog.Info("stale dispatched task reclaimed (batch)",
			"task_id", util.UUIDToString(reclaimed[i].ID),
			"runtime_id", util.UUIDToString(reclaimed[i].RuntimeID),
			"agent_id", util.UUIDToString(reclaimed[i].AgentID),
		)
	}
	if len(claimed) >= maxTasks {
		return claimed[:maxTasks], nil
	}

	// 3. Empty-cache short-circuit + version sampling for the remaining runtimes.
	nonEmpty := make([]pgtype.UUID, 0, len(uniqueIDs))
	versions := make(map[string]int64, len(uniqueIDs))
	for _, rid := range uniqueIDs {
		key := util.UUIDToString(rid)
		if s.EmptyClaim.IsEmpty(ctx, key) {
			continue
		}
		versions[key] = s.EmptyClaim.CurrentVersion(ctx, key)
		nonEmpty = append(nonEmpty, rid)
	}
	if len(nonEmpty) == 0 {
		return claimed, nil
	}

	// 4. One candidate SELECT across the non-empty set.
	candidates, err := s.Queries.ListQueuedClaimCandidatesByRuntimes(ctx, nonEmpty)
	if err != nil {
		// Steps 2/6 commit reclaimed/claimed tasks in their own transactions,
		// so `claimed` may already hold tasks dispatched server-side. Dropping
		// them with a 500 makes the daemon HTTP-fall-back and claim a SECOND
		// batch into the same free slots (the first batch then waits for stale
		// reclaim) — the same double-claim this PR set out to remove
		// (MUL-4257). Prefer partial success: hand back what committed so the
		// handler finalizes and returns it; the errored candidates stay queued
		// for the next poll.
		if len(claimed) > 0 {
			slog.Error("batch claim: candidate query failed after partial success; returning claimed tasks to avoid loss",
				"error", err, "claimed", len(claimed))
			return claimed, nil
		}
		return nil, fmt.Errorf("list queued claim candidates: %w", err)
	}

	// 5. Mark runtimes with zero candidates empty so their next idle poll skips
	// Postgres. Runtimes that had at least one candidate are intentionally not
	// marked (positive results always re-check the DB, matching the singular
	// path).
	withCandidates := make(map[string]struct{}, len(candidates))
	for i := range candidates {
		withCandidates[util.UUIDToString(candidates[i].RuntimeID)] = struct{}{}
	}
	for _, rid := range nonEmpty {
		key := util.UUIDToString(rid)
		if _, ok := withCandidates[key]; !ok {
			s.EmptyClaim.MarkEmpty(ctx, key, versions[key])
		}
	}

	// 6. Claim per distinct agent through the runtime-scoped helper, preserving
	// per-(issue, agent) serialization, capacity caps, and dispatch side effects.
	triedAgents := make(map[string]struct{}, len(candidates))
	for i := range candidates {
		if len(claimed) >= maxTasks {
			break
		}
		agentKey := util.UUIDToString(candidates[i].AgentID)
		if _, tried := triedAgents[agentKey]; tried {
			continue
		}
		triedAgents[agentKey] = struct{}{}

		task, err := s.claimTask(ctx, candidates[i].AgentID, candidates[i].RuntimeID)
		if err != nil {
			// Each scoped claim commits in its own transaction, so earlier
			// iterations (and step-2 reclaims) are already dispatched
			// server-side. Returning nil here would drop them and force the
			// daemon to double-claim via HTTP fallback (MUL-4257). Return the
			// partial batch instead; the failed agent's task stays queued.
			if len(claimed) > 0 {
				slog.Error("batch claim: claim task failed after partial success; returning claimed tasks to avoid loss",
					"error", err, "claimed", len(claimed))
				return claimed, nil
			}
			return nil, fmt.Errorf("claim task: %w", err)
		}
		if task == nil {
			continue
		}
		// The SQL claim is scoped to the candidate runtime. Retain this guard as
		// a defensive contract check so a future query change cannot route work
		// to a runtime this daemon does not host.
		if _, ok := runtimeInSet[util.UUIDToString(task.RuntimeID)]; !ok {
			continue
		}
		claimed = append(claimed, *task)
	}

	return claimed, nil
}

// cancelSupersededDeferredRetries drops deferred auto-retry rows that an active
// task already supersedes, so a single rerun click still produces exactly one
// more run. Runs immediately before promotion — promotion is the moment a stale
// retry would otherwise become claimable. Best-effort: a failure here must not
// stop the claim loop, it just leaves the row for the next tick.
func (s *TaskService) cancelSupersededDeferredRetries(ctx context.Context, runtimeIDs []pgtype.UUID) {
	if len(runtimeIDs) == 0 {
		return
	}
	cancelled, err := s.Queries.CancelSupersededDeferredRetriesForRuntimes(ctx, runtimeIDs)
	if err != nil {
		slog.Warn("cancel superseded deferred retries failed", "error", err)
		return
	}
	for _, task := range cancelled {
		slog.Info("deferred auto-retry cancelled: superseded by an active task",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"agent_id", util.UUIDToString(task.AgentID),
		)
		s.captureTaskCancelled(ctx, task)
		s.ReconcileAgentStatus(ctx, task.AgentID)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, task)
	}
}

func (s *TaskService) PromoteDueDeferredTasksForRuntime(ctx context.Context, runtimeID pgtype.UUID) error {
	s.cancelSupersededDeferredRetries(ctx, []pgtype.UUID{runtimeID})
	tasks, err := s.Queries.PromoteDueDeferredTasksForRuntime(ctx, db.PromoteDueDeferredTasksForRuntimeParams{
		RuntimeID:        runtimeID,
		RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
	})
	if isDuplicatePendingTaskErr(err) {
		// The NOT EXISTS fence inside the query cannot see an enqueue that has
		// not committed yet, so a rerun landing in the same instant still
		// collides here. One row losing its slot must not fail the whole claim:
		// the row stays deferred and the next tick — by which time the rerun is
		// visible — skips it cleanly. Costs one poll interval, never a stall.
		slog.Info("promote due deferred tasks: slot taken by a concurrent enqueue, skipping this tick",
			"runtime_id", util.UUIDToString(runtimeID))
		return nil
	}
	if err != nil {
		return fmt.Errorf("promote due deferred tasks: %w", err)
	}
	for _, task := range tasks {
		slog.Info("deferred task promoted",
			"task_id", util.UUIDToString(task.ID),
			"runtime_id", util.UUIDToString(runtimeID),
			"agent_id", util.UUIDToString(task.AgentID),
		)
		s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
		s.NotifyTaskEnqueued(ctx, task)
	}
	return nil
}

// maybeLogClaimSlow emits one structured log per ClaimTask call when its total
// latency exceeds 300ms, so the prod tail can be diagnosed without flooding
// logs at normal poll rates. Called via defer so it captures the full path
// including post-claim updateAgentStatus / broadcastTaskDispatch (both of
// which can hit the DB) and any error exit.
func (s *TaskService) maybeLogClaimSlow(agentID pgtype.UUID, outcome string, start time.Time, getAgentMs, countRunningMs, claimAgentMs, reanchorMs, updateStatusMs, dispatchMs int64) {
	totalMs := time.Since(start).Milliseconds()
	if totalMs < 300 {
		return
	}
	slog.Info("claim_task slow",
		"agent_id", util.UUIDToString(agentID),
		"outcome", outcome,
		"total_ms", totalMs,
		"get_agent_ms", getAgentMs,
		"count_running_ms", countRunningMs,
		"claim_agent_ms", claimAgentMs,
		"reanchor_chat_input_ms", reanchorMs,
		"update_status_ms", updateStatusMs,
		"dispatch_ms", dispatchMs,
	)
}

// StartTask transitions a dispatched task to running.
// Issue status is NOT changed here — the agent manages it via the CLI.
func (s *TaskService) StartTask(ctx context.Context, taskID pgtype.UUID, supplementSupport ...bool) (*db.AgentTaskQueue, error) {
	enableTaskSupplement := len(supplementSupport) > 0 && supplementSupport[0]
	task, err := s.Queries.StartAgentTaskWithSupplement(ctx, db.StartAgentTaskWithSupplementParams{
		TaskID:               taskID,
		EnableTaskSupplement: enableTaskSupplement,
	})
	if err != nil {
		return nil, fmt.Errorf("start task: %w", err)
	}
	s.taskStarted(ctx, task)
	return &task, nil
}

// StartTaskForClaim serializes the ownership check and transition with reclaim,
// cancellation and other start requests. A replay linearizes at the locked read;
// a cancellation that commits later can still cancel the acknowledged task.
func (s *TaskService) StartTaskForClaim(ctx context.Context, claim db.LockAgentTaskStartClaimParams, supplementSupport ...bool) (*db.AgentTaskQueue, error) {
	if !claim.ID.Valid || !claim.RuntimeID.Valid || !claim.DispatchedAt.Valid {
		return nil, fmt.Errorf("start task: incomplete claim")
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin task start: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	task, err := qtx.LockAgentTaskStartClaim(ctx, claim)
	if err != nil {
		return nil, fmt.Errorf("lock task start claim: %w", err)
	}
	replay := task.Status == "running"
	if !replay {
		task, err = qtx.StartAgentTaskWithSupplement(ctx, db.StartAgentTaskWithSupplementParams{
			TaskID:               task.ID,
			EnableTaskSupplement: len(supplementSupport) > 0 && supplementSupport[0],
		})
		if err != nil {
			return nil, fmt.Errorf("start claimed task: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit task start: %w", err)
	}
	if !replay {
		s.taskStarted(ctx, task)
	}
	return &task, nil
}

func (s *TaskService) taskStarted(ctx context.Context, task db.AgentTaskQueue) {
	s.forgetTaskReclaim(task)

	slog.Info("task started", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
	s.captureTaskStarted(ctx, task)
	// A local-directory waiter was reconciled out of the persisted working
	// status while parked. Restore working as soon as it enters running; the
	// normal dispatched -> running path is already working, so this is
	// intentionally idempotent there.
	s.ReconcileAgentStatus(ctx, task.AgentID)
	// Tell every connected workspace WS client that this task transitioned
	// (dispatched | waiting_local_directory) → running. Without this, the
	// workspace-wide `agentTaskSnapshot` query only refreshes on the 30s
	// staleTime, so any UI that distinguishes "queued" from "running" (e.g.
	// the issue-card agent activity indicator) lags by up to half a minute
	// on the transition users care about most.
	s.broadcastTaskEvent(ctx, protocol.EventTaskRunning, task)
}

// ExtendTaskPrepareLease keeps a claimed-but-not-started task protected while
// the daemon resolves cached inputs and prepares the execution environment.
func (s *TaskService) ExtendTaskPrepareLease(ctx context.Context, taskID, runtimeID pgtype.UUID) (*db.AgentTaskQueue, error) {
	task, err := s.Queries.ExtendAgentTaskPrepareLease(ctx, db.ExtendAgentTaskPrepareLeaseParams{
		ID:        taskID,
		RuntimeID: runtimeID,
		LeaseSecs: prepareLeaseDuration.Seconds(),
	})
	if err != nil {
		return nil, fmt.Errorf("extend task prepare lease: %w", err)
	}
	if task.Status == "dispatched" {
		// Use the successful response time as a conservative application-clock
		// approximation of the DB lease start. Scheduling slightly late by query
		// latency is safer than an early failed check suppressing the hint until
		// the next backstop.
		s.extendTaskReclaimHint(task, time.Now().Add(prepareLeaseDuration))
	} else {
		s.forgetTaskReclaim(task)
	}
	return &task, nil
}

// MarkTaskWaitingLocalDirectory parks a dispatched task in the
// waiting_local_directory state while the daemon waits for another in-flight
// task to release the project_resource path lock. reason carries a short
// human-readable hint (typically the contested path) that the UI surfaces
// next to the status. Returns the updated row so the daemon can confirm the
// transition and so the broadcast carries the up-to-date snapshot.
func (s *TaskService) MarkTaskWaitingLocalDirectory(ctx context.Context, taskID pgtype.UUID, reason string) (*db.AgentTaskQueue, error) {
	reason = sanitizeWaitReason(reason)
	task, err := s.Queries.MarkAgentTaskWaitingLocalDirectory(ctx, db.MarkAgentTaskWaitingLocalDirectoryParams{
		ID:               taskID,
		WaitReason:       pgtype.Text{String: reason, Valid: reason != ""},
		PrepareLeaseSecs: prepareLeaseDuration.Seconds(),
	})
	if err != nil {
		return nil, fmt.Errorf("mark task waiting_local_directory: %w", err)
	}
	s.forgetTaskReclaim(task)

	slog.Info("task waiting_local_directory",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(task.IssueID),
		"reason", reason,
	)
	// waiting_local_directory is owned/queued work, not executing work. The
	// claim path marked the agent working while the row was dispatched, so
	// reconcile immediately when it parks instead of leaving that persisted
	// status stale until a terminal transition.
	s.ReconcileAgentStatus(ctx, task.AgentID)
	// Carry the reason on the event, not just in the row. Without it the client
	// learns WHY a task parked only on the follow-up refetch, so the pill spends
	// that round-trip saying nothing useful — which is the gap this hint exists
	// to close. Already sanitized above, so the socket cannot carry a path the
	// REST payload would have withheld.
	extra := map[string]any{}
	if reason != "" {
		extra["wait_reason"] = reason
	}
	s.broadcastTaskEvent(ctx, protocol.EventTaskWaitingLocalDirectory, task, extra)
	return &task, nil
}

// legacyLocalDirectoryWaitPrefix is how daemons before the display-name change
// opened a wait reason: the literal word, a space, then the absolute path.
const legacyLocalDirectoryWaitPrefix = "local_directory "

// sanitizeWaitReason drops a wait reason that carries an absolute filesystem
// path, then trims what remains.
//
// Version skew makes this load-bearing rather than defensive. Current daemons
// send a display name (localDirectoryAssignment.DisplayName) precisely because
// this text reaches every client on the session and lands in screenshots. An
// OLDER daemon paired with this server still sends `local_directory /Users/
// <name>/repo (held by task abc12345)`, and since clients now render the field
// instead of ignoring it, passing that through would put the user's home path
// — and account name — on screen. The leak would be introduced BY surfacing
// the field, so the guard belongs with it.
//
// Dropping the whole string rather than editing it: clients already handle an
// absent reason (older servers never sent one) by showing the plain waiting
// label, which is exactly the pre-change behaviour and is never wrong.
//
// Remove once no supported daemon emits the legacy format.
func sanitizeWaitReason(reason string) string {
	reason = strings.TrimSpace(reason)
	rest, ok := strings.CutPrefix(reason, legacyLocalDirectoryWaitPrefix)
	if ok && startsWithAbsolutePath(rest) {
		return ""
	}
	return reason
}

// startsWithAbsolutePath reports whether s opens with an absolute path on any
// platform the daemon runs on: POSIX (/…), Windows drive (C:\… or C:/…), or a
// UNC share (\\host\share). Checked against the daemon's own output, so the
// question is only "did an old daemon put a path here", not general parsing.
//
// It deliberately does NOT match the holder clause a current daemon appends —
// `local_directory (held by task abc12345)` is what this server produces when
// a directory is genuinely NAMED "local_directory", and that name is the user's
// own label, not a path.
func startsWithAbsolutePath(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '/' || strings.HasPrefix(s, `\\`) {
		return true
	}
	// Drive-letter form: a single ASCII letter, a colon, then a separator.
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		c := s[0]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return false
}

// CompleteTask marks a task as completed.
// Issue status is NOT changed here — the agent manages it via the CLI.
//
// For chat tasks, CompleteAgentTask and the chat_session resume-pointer
// update run in a single transaction. This closes a race where the next
// queued chat message could be claimed in the window between the task
// flipping to 'completed' and chat_session.session_id being refreshed,
// causing the new task to resume against a stale (or NULL) session.
// durableWorkDir is terminal delivery metadata, not a resume pointer: it is
// populated only after the daemon confirms a disposable worktree is gone.
func (s *TaskService) CompleteTask(ctx context.Context, taskID pgtype.UUID, result []byte, sessionID, workDir, branchName string, sessionRolloutMissing bool, retiredSessionID, durableWorkDir string) (*db.AgentTaskQueue, error) {
	task, _, err := s.CompleteTaskWithTransition(ctx, taskID, result, sessionID, workDir, branchName, sessionRolloutMissing, retiredSessionID, durableWorkDir)
	return task, err
}

// CompleteTaskWithTransition reports whether this call won the running ->
// completed compare-and-swap. Callers with transaction-external side effects
// must only run them when transitioned is true; a replay against an already
// terminal task is still an idempotent success but must not emit them again.
func (s *TaskService) CompleteTaskWithTransition(ctx context.Context, taskID pgtype.UUID, result []byte, sessionID, workDir, branchName string, sessionRolloutMissing bool, retiredSessionID, durableWorkDir string) (*db.AgentTaskQueue, bool, error) {
	var task db.AgentTaskQueue
	// chatAssistantMsg is the single assistant outcome row written for a chat
	// task inside the completion transaction below. It is broadcast (chat:done)
	// only after the transaction commits.
	var chatAssistantMsg *db.ChatMessage
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		if err := lockChatSessionForTaskWrite(ctx, qtx, taskID); err != nil {
			return err
		}
		t, err := qtx.CompleteAgentTask(ctx, db.CompleteAgentTaskParams{
			ID:                    taskID,
			Result:                result,
			SessionID:             pgtype.Text{String: sessionID, Valid: sessionID != ""},
			WorkDir:               pgtype.Text{String: workDir, Valid: workDir != ""},
			DurableWorkDir:        pgtype.Text{String: durableWorkDir, Valid: durableWorkDir != ""},
			BranchName:            pgtype.Text{String: branchName, Valid: branchName != ""},
			SessionRolloutMissing: sessionRolloutMissing,
			RetiredSessionID:      pgtype.Text{String: retiredSessionID, Valid: retiredSessionID != ""},
		})
		if err != nil {
			return err
		}
		task = t

		// Atomic with the status flip: a crash between the two would leave a
		// finished obligation looking pending forever.
		if err := SettleTerminalTaskState(ctx, qtx, t); err != nil {
			return err
		}

		if t.ChatSessionID.Valid {
			// Pin the chat_session's runtime_id alongside the session_id so the
			// next claim can apply the runtime-guard. Both fields move together:
			// when there's no session_id to record, leave runtime_id untouched
			// (NULL → COALESCE keeps the existing value).
			var sessionRuntimeID pgtype.UUID
			if sessionID != "" {
				sessionRuntimeID = t.RuntimeID
			}
			// COALESCE in SQL guarantees empty inputs don't wipe the
			// existing resume pointer; we still surface DB errors.
			if err := qtx.UpdateChatSessionSession(ctx, db.UpdateChatSessionSessionParams{
				ID:        t.ChatSessionID,
				SessionID: pgtype.Text{String: sessionID, Valid: sessionID != ""},
				WorkDir:   pgtype.Text{String: workDir, Valid: workDir != ""},
				RuntimeID: sessionRuntimeID,
			}); err != nil {
				return fmt.Errorf("update chat session resume pointer: %w", err)
			}
			// A turn that recovered by abandoning its session still has to
			// retire it here. The COALESCE above cannot: a fresh retry that
			// succeeds without emitting a new id passes sessionID == "", which
			// preserves the pointer — and the pointer is the poisoned session
			// (GH #6066). Runs after the update so a real new id wins first and
			// the match guard then finds nothing to clear.
			if retiredSessionID != "" {
				if err := qtx.ClearChatSessionSessionIfMatches(ctx, db.ClearChatSessionSessionIfMatchesParams{
					ID:        t.ChatSessionID,
					SessionID: pgtype.Text{String: retiredSessionID, Valid: true},
					RuntimeID: t.RuntimeID,
				}); err != nil {
					return fmt.Errorf("clear retired chat session resume pointer: %w", err)
				}
			}

			// Write the assistant outcome in the SAME transaction as the status
			// flip and resume-pointer update (MUL-4351). For a task-owned direct
			// task this is exactly one row (message or no_response); for a
			// legacy task an empty output writes no row (see
			// writeChatCompletionOutcome). Failing here rolls the whole completion
			// back so the daemon retries the terminal callback, and the status CAS
			// above guarantees a replay can't write a second row.
			msg, err := s.writeChatCompletionOutcome(ctx, qtx, t, result)
			if err != nil {
				return fmt.Errorf("write chat assistant outcome: %w", err)
			}
			chatAssistantMsg = msg
		}
		return nil
	}); err != nil {
		// When parallel agents race, a task may already be completed,
		// cancelled, or failed by the time this call runs. The UPDATE
		// … WHERE status = 'running' returns no rows in that case.
		// Treat it as an idempotent success — same pattern as CancelTask.
		if existing, lookupErr := s.Queries.GetAgentTask(ctx, taskID); lookupErr == nil {
			if errors.Is(err, pgx.ErrNoRows) {
				slog.Info("complete task: already finalized",
					"task_id", util.UUIDToString(taskID),
					"current_status", existing.Status,
					"agent_id", util.UUIDToString(existing.AgentID),
				)
				return &existing, false, nil
			}
			slog.Warn("complete task failed",
				"task_id", util.UUIDToString(taskID),
				"current_status", existing.Status,
				"issue_id", util.UUIDToString(existing.IssueID),
				"chat_session_id", util.UUIDToString(existing.ChatSessionID),
				"agent_id", util.UUIDToString(existing.AgentID),
				"error", err,
			)
		} else {
			slog.Warn("complete task failed: task not found",
				"task_id", util.UUIDToString(taskID),
				"lookup_error", lookupErr,
			)
		}
		return nil, false, fmt.Errorf("complete task: %w", err)
	}

	slog.Info("task completed", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
	s.captureTaskCompleted(ctx, task)

	// Invariant: every completed issue task must have at least one agent
	// comment on the issue, so the user always sees something when a run
	// ends. If the agent posted a comment during execution (result, progress
	// ping, or CLI reply), HasAgentCommentedSince returns true and we skip.
	// Otherwise, synthesize one from the final output. For comment-triggered
	// tasks, TriggerCommentID threads the fallback under the original comment;
	// for assignment-triggered tasks it is NULL and the fallback is top-level.
	// Chat tasks have no IssueID and are handled separately below.
	if task.IssueID.Valid {
		agentCommented, _ := s.Queries.HasAgentCommentedSince(ctx, db.HasAgentCommentedSinceParams{
			IssueID:  task.IssueID,
			AuthorID: task.AgentID,
			Since:    task.StartedAt,
		})
		if !agentCommented {
			var payload protocol.TaskCompletedPayload
			if err := json.Unmarshal(result, &payload); err == nil {
				if payload.Output != "" {
					// Match the CLI's --content / --description behavior: agents that
					// emit literal `\n` 4-char sequences (Python/JSON-style) get them
					// decoded into real newlines before the comment hits the DB. See
					// util.UnescapeBackslashEscapes for the exact contract.
					body := util.UnescapeBackslashEscapes(payload.Output)
					if task.TriggerCommentID.Valid && isTrivialDoneOutput(body) {
						slog.Warn("suppressing trivial comment-trigger fallback output",
							"task_id", util.UUIDToString(task.ID),
							"issue_id", util.UUIDToString(task.IssueID),
							"agent_id", util.UUIDToString(task.AgentID),
						)
					} else {
						// Redact first, then bound: a runaway raw-stream Output (GH #5455)
						// must never reach the issue thread, even as a clipped excerpt.
						content := truncateFallbackCommentBody(redact.Text(body), maxSynthesizedFallbackCommentRunes)
						s.createAgentComment(ctx, task.IssueID, task.AgentID, content, "comment", task.TriggerCommentID, task.ID)
					}
				}
			}
		}
	}

	// For chat tasks, broadcast chat:done AFTER commit. The single assistant
	// outcome row (message or no_response) and the resume pointer were already
	// persisted inside the transaction above. Unread is derived from the read
	// cursor (chat_session.last_read_at) vs the assistant messages after it — a
	// no_response row has role='assistant' and a fresh created_at, so it counts
	// as unread just like a text reply, no per-reply stamping needed.
	if task.ChatSessionID.Valid {
		// The assistant outcome row (message / no_response) and any attachment
		// binding were written inside the completion transaction above by
		// writeChatCompletionOutcome. Broadcast chat:done AFTER commit.
		s.broadcastChatDone(ctx, task, chatAssistantMsg)
	}

	// Reconcile agent status
	s.ReconcileAgentStatus(ctx, task.AgentID)

	// Broadcast
	s.broadcastTaskEvent(ctx, protocol.EventTaskCompleted, task)

	return &task, true, nil
}

// chatNoResponseFallback is the non-empty English body stored on a no_response
// assistant row. New clients render a localized "no text reply this turn"
// message keyed on message_kind='no_response'; older clients that ignore
// message_kind still show this text instead of an empty bubble (MUL-4351).
const chatNoResponseFallback = "The agent finished this turn without a text reply."

// writeChatCompletionOutcome writes the assistant chat_message outcome for a
// completed chat task inside the caller's completion transaction, returning the
// row (nil when none is written).
//
// Direct (web/mobile) tasks get the explicit single-outcome contract: a
// non-empty final output becomes an ordinary assistant message, and an
// empty/whitespace output becomes a visible no_response outcome carrying a
// non-empty English fallback body. It never auto-retries: an empty output is a
// legitimate terminal result (a tool-only turn) and re-running it would repeat
// side effects already performed.
//
// Legacy tasks (chat_input_task_id NULL) keep the prior behavior: a non-empty
// output writes an ordinary assistant message, but an EMPTY output writes NO
// row, so chat:done carries empty content.
func (s *TaskService) writeChatCompletionOutcome(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, result []byte) (*db.ChatMessage, error) {
	// result is the daemon request re-marshalled by the handler, so it is always
	// valid JSON; an empty Output is the only case this branch cares about.
	var payload protocol.TaskCompletedPayload
	_ = json.Unmarshal(result, &payload)
	// Same unescape as the issue-comment path: literal `\n` from agent stdout
	// becomes a real newline so the chat panel renders paragraph breaks.
	body := util.UnescapeBackslashEscapes(payload.Output)
	isEmpty := strings.TrimSpace(body) == ""

	// MUL-4899 completion-boundary observation. Measures whether the delivery
	// contract in the runtime brief is actually landing on the chat surface.
	// Strictly non-blocking: the reply is written either way.
	s.observeChatOutputLocalPath(task, body)

	// Attachments the agent uploaded during this task (tagged with task_id, not
	// yet bound to any owner) are part of this reply. They make an empty-text
	// turn a real image/file response — NOT a no_response — and need a row to
	// hang on. Count + bind run on qtx so message creation and binding are one
	// atomic outcome.
	wsUUID, _ := util.ParseUUID(s.ResolveTaskWorkspaceID(ctx, task))
	var pendingAttachments int64
	if wsUUID.Valid {
		n, err := qtx.CountUnboundChatAttachmentsForTask(ctx, db.CountUnboundChatAttachmentsForTaskParams{
			WorkspaceID: wsUUID,
			TaskID:      task.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("count chat attachments: %w", err)
		}
		pendingAttachments = n
	}

	// Legacy empty completion with nothing to show: emit no assistant row, only
	// an empty chat:done for typing/lifecycle. Attachments still force a row —
	// the agent produced a deliverable the user must see.
	if isEmpty && pendingAttachments == 0 && !task.ChatInputTaskID.Valid {
		return nil, nil // legacy task
	}

	params := db.CreateChatMessageParams{
		ID:            dbid.NewV7(),
		ChatSessionID: task.ChatSessionID,
		Role:          "assistant",
		TaskID:        task.ID,
		ElapsedMs:     computeChatElapsedMs(task),
	}
	switch {
	case !isEmpty:
		params.Content = redact.Text(body)
		// message_kind left NULL → COALESCE defaults to 'message'.
	case pendingAttachments > 0:
		// Image/file-only reply: a real 'message' outcome with empty text — the
		// attachment cards ARE the response, so it must not read as no_response.
		params.Content = ""
	default:
		// Task-owned direct task, empty output, no attachments: explicit,
		// visible no_response outcome.
		params.Content = chatNoResponseFallback
		params.MessageKind = pgtype.Text{String: protocol.ChatMessageKindNoResponse, Valid: true}
	}
	row, err := createAssistantChatMessage(ctx, qtx, params)
	if err != nil {
		return nil, err
	}

	// Bind the task's still-unclaimed attachments to the reply we just wrote.
	if pendingAttachments > 0 && wsUUID.Valid {
		bound, err := qtx.BindChatAttachmentsToMessage(ctx, db.BindChatAttachmentsToMessageParams{
			ChatMessageID: row.ID,
			WorkspaceID:   wsUUID,
			TaskID:        task.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("bind chat attachments: %w", err)
		}
		if len(bound) > 0 {
			slog.Info("bound chat attachments to assistant reply",
				"task_id", util.UUIDToString(task.ID),
				"message_id", util.UUIDToString(row.ID),
				"count", len(bound),
			)
		}
	}
	return &row, nil
}

// observeChatOutputLocalPath records a metric when a chat reply references a
// runtime-local path (MUL-4899). Observation only — it never mutates the reply,
// never fails the completion, and makes no claim to have fixed anything.
//
// Two hard constraints shape it:
//
//  1. Lexical only. The path lives on the daemon's machine, so the server cannot
//     os.Stat it the way the CLI lint can. That leaves two signals it can be
//     confident about without guessing: a `file://` URL, and the task's own
//     recorded work_dir as a prefix. Anything subtler would be a guess, and a
//     guess is not worth a false signal on a dashboard.
//  2. No path, no body text, and no fragment of either may reach the metric or
//     the log — only the classification and the task id.
func (s *TaskService) observeChatOutputLocalPath(task db.AgentTaskQueue, body string) {
	if s.Metrics == nil || strings.TrimSpace(body) == "" {
		return
	}
	kind := ""
	switch {
	case strings.Contains(strings.ToLower(body), "file://"):
		kind = "file_url"
	case task.WorkDir.Valid && task.WorkDir.String != "" && strings.Contains(body, task.WorkDir.String):
		kind = "workdir_path"
	default:
		return
	}
	s.Metrics.RecordChatOutputLocalPath(kind)
	slog.Warn("chat reply references a runtime-local path",
		"task_id", util.UUIDToString(task.ID),
		"kind", kind,
	)
}

// FailTask marks a task as failed.
// Issue status is NOT changed here — the agent manages it via the CLI.
//
// sessionID/workDir are optional: when the agent established a real session
// before failing (e.g. crashed mid-conversation, was cancelled, or hit a
// tool error), the daemon should pass them so we can preserve the resume
// pointer on both the task row and the chat_session — otherwise the next
// chat turn would silently start a brand-new session and lose memory.
// durableWorkDir is persisted on the task only; it never replaces the actual
// workDir used for session resumption.
//
// failureReason is a coarse classifier consumed by the auto-retry path.
// Pass "" when unknown — the server runs the raw error text through
// taskfailure.Classify so the persisted failure_reason still lands in
// the canonical refined taxonomy rather than the legacy "agent_error"
// coarse bucket. Daemon callers that already produced a refined reason
// (via classifyPoisonedError, the timeout / runtime classifier, etc.)
// will have their value preserved untouched.
func (s *TaskService) FailTask(ctx context.Context, taskID pgtype.UUID, errMsg, sessionID, workDir, branchName, failureReason string, sessionRolloutMissing bool, retiredSessionID, durableWorkDir string) (*db.AgentTaskQueue, error) {
	task, _, err := s.FailTaskWithTransition(ctx, taskID, errMsg, sessionID, workDir, branchName, failureReason, sessionRolloutMissing, retiredSessionID, durableWorkDir)
	return task, err
}

// FailTaskWithTransition is the failure counterpart to
// CompleteTaskWithTransition. The bool is false for an idempotent replay that
// observed an already-terminal row.
func (s *TaskService) FailTaskWithTransition(ctx context.Context, taskID pgtype.UUID, errMsg, sessionID, workDir, branchName, failureReason string, sessionRolloutMissing bool, retiredSessionID, durableWorkDir string) (*db.AgentTaskQueue, bool, error) {
	// Strip bytes PostgreSQL cannot store before anything else reads errMsg, so
	// the classifier, the transaction and every downstream consumer see the one
	// text we will actually persist (GH #7098). Kept at the service boundary
	// rather than only in the /fail handler because failClaimedTaskBeforeLaunch
	// reaches FailTask without going through request decoding.
	errMsg = util.SanitizeTextForPostgres(errMsg)

	// MUL-2946: synthesise a refined reason from the error text whenever the
	// caller didn't supply one. This is the last write-path guard against
	// "agent_error" coarse rows ending up in agent_task_queue.failure_reason
	// — every other path either provides a classified reason directly
	// (sweepers writing 'queued_expired' / 'runtime_offline' / 'timeout'
	// / 'runtime_recovery' via SQL) or runs the daemon's classifyPoisonedError
	// + taskfailure.Classify chain.
	if failureReason == "" {
		failureReason = taskfailure.Classify(errMsg).String()
	}
	// MUL-5370: daemons upgrade on their own cadence, so a fix that depends on
	// a new daemon-side label only reaches hosts that happened to update. An
	// older daemon reports a *non-empty* catchall for a failed skill-bundle
	// download, which the branch above deliberately leaves alone — without this
	// the retry and the actionable copy would both skip every un-upgraded host.
	// Runs after the empty-reason branch so a legacy reason synthesised there
	// is normalised too, and before the retry pre-compute below so the upgraded
	// reason is what decides retry eligibility.
	failureReason = taskfailure.NormalizeDaemonReason(failureReason, errMsg).String()

	// Pre-compute the auto-retry so the retry child can be created inside the
	// SAME transaction as the fail (MUL-4351). Doing it atomically closes the
	// window between the fail committing and the retry appearing during which a
	// newer chat task could claim the now-idle session and jump ahead of the
	// retry. Eligibility is resolved here, before the transaction, and only for
	// retryable failures, so the common agent_error path skips this work.
	var (
		wantRetry        bool
		retryFireAt      pgtype.Timestamptz
		retryMaxAttempts pgtype.Int4
	)
	if retryableReasons[failureReason] {
		if parent, perr := s.Queries.GetAgentTask(ctx, taskID); perr != nil {
			slog.Warn("fail task auto-retry: load parent failed",
				"task_id", util.UUIDToString(taskID), "error", perr)
		} else if retryEligible(failureReason, parent) {
			wantRetry = true
			// Persist the reason-aware effective budget into the child so the
			// retry chain self-describes (e.g. provider_network → max_attempts=3),
			// rather than leaking a contradictory attempt=N/max_attempts=2 row.
			retryMaxAttempts = pgtype.Int4{Int32: retryAttemptCeiling(failureReason, parent.MaxAttempts), Valid: true}
			// Defer this attempt when the reason's schedule calls for a backoff
			// (provider_network's final attempt waits ~5s); a zero delay leaves
			// fire_at NULL so the child is created immediately-claimable.
			if delay := retryDelayForAttempt(failureReason, parent.Attempt); delay > 0 {
				retryFireAt = pgtype.Timestamptz{Time: time.Now().Add(delay), Valid: true}
			}
		}
	}

	var task db.AgentTaskQueue
	var retried *db.AgentTaskQueue
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		if err := lockChatSessionForTaskWrite(ctx, qtx, taskID); err != nil {
			return err
		}
		t, err := qtx.FailAgentTask(ctx, db.FailAgentTaskParams{
			ID:             taskID,
			Error:          pgtype.Text{String: errMsg, Valid: true},
			FailureReason:  pgtype.Text{String: failureReason, Valid: failureReason != ""},
			SessionID:      pgtype.Text{String: sessionID, Valid: sessionID != ""},
			WorkDir:        pgtype.Text{String: workDir, Valid: workDir != ""},
			DurableWorkDir: pgtype.Text{String: durableWorkDir, Valid: durableWorkDir != ""},
			// Older daemons' worktree mode can report a branch for a failed
			// run; keep the pointer to that partial work.
			BranchName:            pgtype.Text{String: branchName, Valid: branchName != ""},
			SessionRolloutMissing: sessionRolloutMissing,
			RetiredSessionID:      pgtype.Text{String: retiredSessionID, Valid: retiredSessionID != ""},
		})
		if err != nil {
			return err
		}
		task = t

		// Atomic with the status flip, same as the completion path. A failed
		// coordinator that already received the recovery comment has consumed
		// the obligation: the pre-existing delivered_comment_ids coverage check
		// never looked at the covering task's status either.
		if err := SettleTerminalTaskState(ctx, qtx, t); err != nil {
			return err
		}

		// Keep resume-unsafe sessions on the task row for observability, but
		// do not promote them to the chat-level resume pointer.
		//
		// Declining to overwrite is not enough on its own: the claim handler
		// reads chat_session.session_id BEFORE consulting
		// GetLastChatTaskSession, so a pointer already holding the dead
		// session bypasses every filter that query applies and the next turn
		// resumes it (GH #6066). Clear it in this same transaction, matched on
		// the exact session and runtime so a concurrent turn's newer pointer
		// is left alone. ResumeUnsafeFailure (not the reason alone) so an
		// un-upgraded daemon's agent_error.unknown row is caught by the error
		// text too.
		if t.ChatSessionID.Valid && ResumeUnsafeFailure(failureReason, errMsg) {
			deadSession := sessionID
			if deadSession == "" {
				deadSession = t.SessionID.String
			}
			if deadSession != "" {
				if err := qtx.ClearChatSessionSessionIfMatches(ctx, db.ClearChatSessionSessionIfMatchesParams{
					ID:        t.ChatSessionID,
					SessionID: pgtype.Text{String: deadSession, Valid: true},
					RuntimeID: t.RuntimeID,
				}); err != nil {
					return fmt.Errorf("clear poisoned chat session resume pointer: %w", err)
				}
			}
		}
		// A session the run explicitly retired must go too, whatever the
		// terminal status: the fresh-session retry that replaced it may well
		// have succeeded, and the pointer would otherwise still name the
		// transcript it retried away from.
		if t.ChatSessionID.Valid && retiredSessionID != "" {
			if err := qtx.ClearChatSessionSessionIfMatches(ctx, db.ClearChatSessionSessionIfMatchesParams{
				ID:        t.ChatSessionID,
				SessionID: pgtype.Text{String: retiredSessionID, Valid: true},
				RuntimeID: t.RuntimeID,
			}); err != nil {
				return fmt.Errorf("clear retired chat session resume pointer: %w", err)
			}
		}

		// ResumeUnsafeFailure, not resumeUnsafeFailureReason: the reason-only
		// check passes an un-upgraded daemon's agent_error.unknown row, which
		// would re-pin the very session the clear above just removed.
		if t.ChatSessionID.Valid && !ResumeUnsafeFailure(failureReason, errMsg) {
			// Pin the chat_session's runtime_id alongside the session_id so the
			// next claim can apply the runtime-guard. Both fields move together:
			// when there's no session_id to record, leave runtime_id untouched
			// (NULL → COALESCE keeps the existing value).
			var sessionRuntimeID pgtype.UUID
			if sessionID != "" {
				sessionRuntimeID = t.RuntimeID
			}
			if err := qtx.UpdateChatSessionSession(ctx, db.UpdateChatSessionSessionParams{
				ID:        t.ChatSessionID,
				SessionID: pgtype.Text{String: sessionID, Valid: sessionID != ""},
				WorkDir:   pgtype.Text{String: workDir, Valid: workDir != ""},
				RuntimeID: sessionRuntimeID,
			}); err != nil {
				return fmt.Errorf("update chat session resume pointer: %w", err)
			}
		}

		// Create the retry child atomically with the fail. CreateRetryTask reads
		// the just-failed parent row (same tx), so it inherits chat_input_task_id
		// and the bumped chat-retry priority; broadcast/notify happen after commit.
		// This check is an optimisation, NOT the safety mechanism. It is a plain
		// count at READ COMMITTED and takes no lock, so a rerun can always commit
		// between it and the insert below; it only saves the work when a
		// successor is already visible and gives the skip a readable log line.
		// Correctness under that race belongs to CreateRetryTask's
		// ON CONFLICT DO NOTHING, which yields the slot instead of raising and so
		// can never abort this transaction — which also carries the parent's
		// failed status.
		createRetry := wantRetry
		if createRetry {
			successor, herr := hasRunnableSuccessor(ctx, qtx, t)
			if herr != nil {
				return fmt.Errorf("check runnable successor: %w", herr)
			}
			if successor {
				slog.Info("fail task auto-retry skipped: a successor is already pending",
					"task_id", util.UUIDToString(taskID),
					"issue_id", util.UUIDToString(t.IssueID),
					"agent_id", util.UUIDToString(t.AgentID),
				)
				createRetry = false
			}
		}
		if createRetry {
			child, cerr := qtx.CreateRetryTask(ctx, db.CreateRetryTaskParams{
				NewTaskID:   dbid.NewV7(),
				ID:          taskID,
				FireAt:      retryFireAt,
				MaxAttempts: retryMaxAttempts,
			})
			switch {
			case cerr == nil:
				retried = &child
			case errors.Is(cerr, pgx.ErrNoRows):
				// The statement wrote nothing: either the owning workspace was
				// torn down mid-flight, or a rerun took the pending slot after
				// the unlocked check above (ON CONFLICT DO NOTHING). Neither is
				// a reason to abort — this transaction still owns the parent's
				// failed status, and losing that would leave the task stuck in
				// 'running'. Record the failure and move on without a retry.
				slog.Info("fail task auto-retry not created: no row written",
					"task_id", util.UUIDToString(taskID),
					"issue_id", util.UUIDToString(t.IssueID),
					"agent_id", util.UUIDToString(t.AgentID),
				)
			default:
				return fmt.Errorf("create retry task: %w", cerr)
			}
		}

		// A terminal non-retried chat failure is a visible assistant outcome.
		// Persist it while the session lock and failure transaction are still
		// held, then reanchor the next direct head. Otherwise the successor could
		// be claimed between the status flip and this row, placing its user input
		// before the failure it follows.
		if t.ChatSessionID.Valid && retried == nil {
			if _, err := createAssistantChatMessage(ctx, qtx, db.CreateChatMessageParams{
				ID:            dbid.NewV7(),
				ChatSessionID: t.ChatSessionID,
				Role:          "assistant",
				Content:       redact.Text(errMsg),
				TaskID:        t.ID,
				FailureReason: pgtype.Text{String: failureReason, Valid: failureReason != ""},
				ElapsedMs:     computeChatElapsedMs(t),
			}); err != nil {
				return fmt.Errorf("write chat failure outcome: %w", err)
			}
		}
		return nil
	}); err != nil {
		if existing, lookupErr := s.Queries.GetAgentTask(ctx, taskID); lookupErr == nil {
			if errors.Is(err, pgx.ErrNoRows) {
				slog.Info("fail task: already finalized",
					"task_id", util.UUIDToString(taskID),
					"current_status", existing.Status,
					"agent_id", util.UUIDToString(existing.AgentID),
				)
				return &existing, false, nil
			}
			slog.Warn("fail task failed",
				"task_id", util.UUIDToString(taskID),
				"current_status", existing.Status,
				"issue_id", util.UUIDToString(existing.IssueID),
				"chat_session_id", util.UUIDToString(existing.ChatSessionID),
				"agent_id", util.UUIDToString(existing.AgentID),
				"error", err,
			)
		} else {
			slog.Warn("fail task failed: task not found",
				"task_id", util.UUIDToString(taskID),
				"lookup_error", lookupErr,
			)
		}
		return nil, false, fmt.Errorf("fail task: %w", err)
	}

	slog.Warn("task failed", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID), "error", errMsg, "failure_reason", failureReason)
	s.captureTaskFailed(ctx, task)

	// The auto-retry child (if any) was created inside the transaction above so
	// no newer chat task could jump ahead of it. Surface it now: broadcast
	// queued first, then notify the daemon — see EnqueueTaskForIssue for the
	// ordering rationale. A deferred child (backoff armed via fire_at) is NOT
	// queued yet: PromoteDueDeferredTasksForRuntime emits its queued event and
	// daemon wakeup when fire_at arrives, so announcing it here would be wrong.
	if retried != nil {
		slog.Info("task auto-retry enqueued",
			"parent_task_id", util.UUIDToString(task.ID),
			"child_task_id", util.UUIDToString(retried.ID),
			"reason", failureReason,
			"attempt", retried.Attempt,
			"max_attempts", retried.MaxAttempts,
			"status", retried.Status,
		)
		if retried.Status == "queued" {
			s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, *retried)
			s.NotifyTaskEnqueued(ctx, *retried)
		}
	}

	// A delegated task that has reached a terminal failure must hand control
	// back to the task that delegated it. This runs only after the existing
	// retry decision: retryable failures keep their current policy and remain
	// silent while a child attempt is pending. The recovery signal is a
	// platform-authored comment on the source task's issue, routed explicitly to
	// that task's agent; recoverDelegatedTaskFailure coalesces it with an
	// existing coordinator run and deduplicates by the failed task id.
	if retried == nil {
		_, recoveryErr := s.recoverDelegatedTaskFailure(ctx, task)
		if recoveryErr != nil {
			slog.Warn("delegated task failure recovery failed",
				"task_id", util.UUIDToString(task.ID),
				"delegated_from_task_id", util.UUIDToString(task.DelegatedFromTaskID),
				"error", recoveryErr,
			)
		}
	}

	// Skip the per-failure system comment when we'll immediately retry —
	// the new task will surface its own status to the user, and we don't
	// want to spam the issue with "task timed out" messages on every
	// daemon hiccup. Delegated failures keep this existing failed-issue comment
	// in addition to the coordinator recovery signal, preserving visibility on
	// both sides of a cross-issue handoff.
	if errMsg != "" && task.IssueID.Valid && retried == nil {
		s.createAgentComment(ctx, task.IssueID, task.AgentID, redact.Text(errMsg), "system", task.TriggerCommentID, task.ID)
	}

	// Reconcile agent status
	s.ReconcileAgentStatus(ctx, task.AgentID)

	// Broadcast with the same redacted failure text that was persisted in the
	// chat transcript. A retry-pending attempt carries no error text because its
	// child reports the eventual terminal outcome.
	s.broadcastTaskFailedEvent(ctx, task, errMsg, failureReason, retried != nil)

	return &task, true, nil
}

// retryableReasons enumerates failure reasons that the auto-retry path is
// allowed to act on. Agent-side errors (compile failures, model rejections,
// etc.) are intentionally excluded — those are real problems that the user
// should see, not infrastructure flakiness.
//
// The one agent_error.* exception is provider_network: a mid-stream provider
// disconnect (e.g. Claude Code's "API Error: Connection closed mid-response")
// is transient infrastructure flakiness, not an agent decision. Unattended
// issue runs otherwise terminate on it, while interactive chat only survives
// because the CLI's own in-process retry happens to recover first — so we make
// the platform retry it directly (MUL-4910). It is resume-safe (not in
// resumeUnsafeFailureReason), so the retry child inherits the session and
// continues the truncated conversation rather than restarting from scratch.
// skill_bundle_unavailable is retryable for the same reason: the agent process
// never started, so there is nothing to be idempotent about, and every bundle
// that did download is already cached on disk — a retry resumes from there
// instead of re-fetching the whole set (MUL-5370).
var retryableReasons = map[string]bool{
	string(taskfailure.ReasonRuntimeOffline):         true,
	string(taskfailure.ReasonRuntimeRecovery):        true,
	string(taskfailure.ReasonTimeout):                true,
	"codex_semantic_inactivity":                      true,
	string(taskfailure.ReasonAgentProviderNetwork):   true,
	string(taskfailure.ReasonSkillBundleUnavailable): true,
}

// runtime_offline retries start deferred, not queued: their positive fire_at
// routes them through health-gated promotion after a fresh runtime heartbeat
// returns. The queue sweeper also exempts this retry lineage, covering the race
// where a runtime disconnects again after promotion but before claim. This
// delay is only a state marker, not the reconnect policy; the heartbeat gate is
// authoritative.
//
// Transient provider stream cuts (provider_network) get a bespoke three-tier
// schedule (MUL-4910): first run + immediate retry + one retry deferred ~5s.
// A blip that survives the immediate retry gets a short cooldown before the
// final attempt instead of firing back-to-back. Every other retryable reason
// keeps the task's generic max_attempts ceiling and retries immediately.
const (
	runtimeOfflineRetryDeferral   = time.Second
	providerNetworkMaxAttempts    = 3
	providerNetworkFinalRetryWait = 5 * time.Second
)

// retryAttemptCeiling reports how many attempts the auto-retry path allows for
// a failure reason. It only ever WIDENS the task's generic max_attempts, and
// only for reasons with a bespoke schedule; everything else keeps the column's
// value (default 2 = first run + one retry).
//
// max_attempts <= 1 explicitly disables auto-retry (055_task_lease_and_retry.up
// .sql: "1 disables retry"), so it is never overridden — a disabled task must
// not be revived by a raised ceiling. Callers persist this value into the retry
// child (CreateRetryTask's max_attempts) so the row stays self-consistent:
// provider_network's chain records attempt=3, max_attempts=3, not a
// contradictory attempt=3, max_attempts=2 (MUL-4910).
func retryAttemptCeiling(reason string, taskMaxAttempts int32) int32 {
	if taskMaxAttempts <= 1 {
		return taskMaxAttempts
	}
	if reason == string(taskfailure.ReasonAgentProviderNetwork) && taskMaxAttempts < providerNetworkMaxAttempts {
		return providerNetworkMaxAttempts
	}
	return taskMaxAttempts
}

// retryDelayForAttempt reports how long to defer the NEXT attempt after a
// failure at failedAttempt. runtime_offline always gets a positive fire_at so
// it waits for the health-gated promotion path. provider_network's final
// attempt is deferred ~5s; every other retry remains immediate (zero delay →
// the child is created 'queued', claimable at once). Callers pass the returned
// delay to CreateRetryTask via fire_at.
func retryDelayForAttempt(reason string, failedAttempt int32) time.Duration {
	if reason == string(taskfailure.ReasonRuntimeOffline) {
		return runtimeOfflineRetryDeferral
	}
	if reason == string(taskfailure.ReasonAgentProviderNetwork) &&
		failedAttempt >= providerNetworkMaxAttempts-1 {
		return providerNetworkFinalRetryWait
	}
	return 0
}

func resumeUnsafeFailureReason(reason string) bool {
	switch reason {
	// Failures that poison the agent CONVERSATION (not the workdir): resuming
	// the same session would immediately replay the stuck/oversized state.
	// Keep in sync with the GetLastTaskSession / GetLastChatTaskSession resume
	// blacklists. (CreateRetryTask's fresh-session CASE WHEN only needs the
	// subset of these that is also auto-retryable, currently
	// codex_semantic_inactivity.)
	// codex_resume_oversized is the strongest member of this set: a codex
	// rollout only ever grows, so a thread whose resume response already
	// overflowed the reader will overflow on every future attempt too.
	case "iteration_limit", "agent_fallback_message", "api_invalid_request", "codex_semantic_inactivity", "agent_error.context_overflow", "codex_resume_oversized":
		return true
	default:
		return false
	}
}

// ResumeUnsafeFailure reports whether a failed task's agent session must NOT be
// resumed on a retry. It combines the failure_reason poison set
// (resumeUnsafeFailureReason) with the SAME defense-in-depth on raw error text
// that the GetLastTaskSession / GetLastChatTaskSession resume queries apply: an
// Anthropic 400 invalid_request_error means the conversation history itself is
// unprocessable even when failure_reason was mis- or un-classified (legacy
// 'agent_error' rows written before MUL-1921, or deploy-window rows). Callers
// that only have a failure_reason (e.g. at fail time) may pass an empty
// errorText.
//
// This is the shared source of truth for the manual-retry claim path, which
// reads the exact source task instead of GetLastTaskSession and would otherwise
// bypass the error-text guard.
func ResumeUnsafeFailure(failureReason, errorText string) bool {
	if resumeUnsafeFailureReason(failureReason) {
		return true
	}
	lower := strings.ToLower(errorText)
	if strings.Contains(lower, "400") && strings.Contains(lower, "invalid_request_error") {
		return true
	}
	// Provider credential-resolution failures are deterministic on resume: the
	// missing api_key / auth_token / auth header is baked into the session's
	// provider state, so a rerun must start fresh instead of replaying the same
	// auth error on the recorded (agent, issue) session. taskfailure.Classify
	// deliberately leaves this error as agent_error.unknown, so this
	// reason-independent text guard is the load-bearing protection for both new
	// and already persisted rows. Keep it in sync with the GetLastTaskSession /
	// GetLastChatTaskSession resume queries.
	//
	// The phrase itself lives in taskfailure.AuthMethodUnresolved, shared with
	// the daemon's in-turn fresh-session retry gate so the two layers cannot
	// disagree about which errors mean "this session can never be resumed".
	if taskfailure.AuthMethodUnresolved(errorText) {
		return true
	}
	// Same defense-in-depth for the provider-agnostic empty-message shape:
	// a daemon too old to carry classifyPoisonedError's new branch reports
	// agent_error.unknown, and without this the manual-retry path would
	// happily resume the transcript the provider just refused (GH #6066).
	return taskfailure.UnresumableHistory(errorText)
}

// retryEligible reports whether a failed task qualifies for an automatic retry
// attempt: an infrastructure-shaped failure_reason, remaining attempt budget,
// and linked to an issue or chat session. Shared by FailTask's in-transaction
// retry and the orphan sweeper's MaybeRetryFailedTask so both agree on which
// failures re-run.
func retryEligible(failureReason string, t db.AgentTaskQueue) bool {
	return retryableReasons[failureReason] &&
		t.Attempt < retryAttemptCeiling(failureReason, t.MaxAttempts) &&
		(t.IssueID.Valid || t.ChatSessionID.Valid)
}

// hasRunnableSuccessor reports whether another not-yet-started task already
// occupies the single queued/dispatched slot that
// idx_one_pending_task_per_issue_agent_v2 allows per (issue, agent).
//
// Both auto-retry paths consult this before inserting a retry child. A manual
// rerun can now be enqueued BEHIND a still-running task instead of cancelling
// it, so by the time that task fails its slot may already be taken, and the
// retry exists to give the issue a runnable successor that already exists.
//
// This is advisory only: the query takes no lock, so a rerun committing after it
// returns is always possible. CreateRetryTask's ON CONFLICT DO NOTHING is what
// makes losing that race harmless. Skipping early just avoids pointless work and
// records why no retry was made.
//
// Chat tasks carry no issue_id, so the index cannot apply to them
// and their retries can never collide.
func hasRunnableSuccessor(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (bool, error) {
	if !task.IssueID.Valid {
		return false, nil
	}
	return q.HasPendingTaskForIssueAndAgentInThread(ctx, db.HasPendingTaskForIssueAndAgentInThreadParams{
		ThreadCommentID: task.TriggerCommentID,
		IssueID:         task.IssueID,
		AgentID:         task.AgentID,
	})
}

// MaybeRetryFailedTask spawns a fresh queued attempt for a recently-failed
// task when the failure was infrastructure-shaped (daemon crash, runtime
// went offline, dispatch/run timeout) and the task hasn't exhausted its
// max_attempts budget. The child task inherits agent/runtime/issue/chat
// links and, for resume-safe failures, the parent's session_id/work_dir so
// the agent can resume the conversation when the backend supports it. Returns
// the new task, or nil when no retry was created.
func (s *TaskService) MaybeRetryFailedTask(ctx context.Context, parent db.AgentTaskQueue) (*db.AgentTaskQueue, error) {
	if parent.Status != "failed" {
		return nil, nil
	}
	reason := ""
	if parent.FailureReason.Valid {
		reason = parent.FailureReason.String
	}
	if !retryableReasons[reason] {
		return nil, nil
	}
	// Use the reason-aware ceiling, not the raw max_attempts column, so an
	// orphaned provider_network task recovered on its 2nd attempt is still
	// allowed its deferred 3rd attempt (retryAttemptCeiling raises the ceiling
	// to 3). Kept in sync with retryEligible below, which applies the same
	// ceiling to the primary FailTask path.
	if parent.Attempt >= retryAttemptCeiling(reason, parent.MaxAttempts) {
		slog.Info("task auto-retry skipped: budget exhausted",
			"task_id", util.UUIDToString(parent.ID),
			"attempt", parent.Attempt,
			"max_attempts", parent.MaxAttempts,
			"ceiling", retryAttemptCeiling(reason, parent.MaxAttempts),
		)
		return nil, nil
	}
	// A task with no issue/chat link has nowhere to report its retry —
	// retryEligible keeps this sweeper path in sync with FailTask's in-tx retry.
	if !retryEligible(reason, parent) {
		return nil, nil
	}

	// Mirror FailTask's in-tx backoff + effective-budget persistence: defer the
	// final provider_network attempt ~5s via fire_at (zero delay leaves fire_at
	// NULL for an immediate child), and write the reason-aware ceiling into the
	// child's max_attempts so the retry chain stays self-consistent.
	var retryFireAt pgtype.Timestamptz
	if delay := retryDelayForAttempt(reason, parent.Attempt); delay > 0 {
		retryFireAt = pgtype.Timestamptz{Time: time.Now().Add(delay), Valid: true}
	}
	// Same advisory slot check as FailTask's path, for the same reason: skip the
	// work when a successor is already visible. Losing the race is handled by
	// CreateRetryTask yielding the slot, which this caller reads as "no retry".
	if successor, herr := hasRunnableSuccessor(ctx, s.Queries, parent); herr != nil {
		slog.Warn("task auto-retry: successor check failed; attempting retry anyway",
			"parent_task_id", util.UUIDToString(parent.ID), "error", herr)
	} else if successor {
		slog.Info("task auto-retry skipped: a successor is already pending",
			"parent_task_id", util.UUIDToString(parent.ID),
			"issue_id", util.UUIDToString(parent.IssueID),
			"agent_id", util.UUIDToString(parent.AgentID),
		)
		return nil, nil
	}
	if s.TxStarter == nil {
		return nil, errors.New("task auto-retry: transaction starter is required")
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("task auto-retry: begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	child, err := qtx.CreateRetryTask(ctx, db.CreateRetryTaskParams{
		NewTaskID:   dbid.NewV7(),
		ID:          parent.ID,
		FireAt:      retryFireAt,
		MaxAttempts: pgtype.Int4{Int32: retryAttemptCeiling(reason, parent.MaxAttempts), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Workspace torn down, or the pending slot was taken between the check
		// above and this insert. Same contract as FailTask's path: no retry, no
		// error.
		slog.Info("task auto-retry not created: no row written",
			"parent_task_id", util.UUIDToString(parent.ID),
			"reason", reason,
		)
		return nil, nil
	}
	if err != nil {
		slog.Warn("task auto-retry failed",
			"parent_task_id", util.UUIDToString(parent.ID),
			"reason", reason,
			"error", err,
		)
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("task auto-retry: commit: %w", err)
	}
	slog.Info("task auto-retry enqueued",
		"parent_task_id", util.UUIDToString(parent.ID),
		"child_task_id", util.UUIDToString(child.ID),
		"reason", reason,
		"attempt", child.Attempt,
		"max_attempts", child.MaxAttempts,
		"status", child.Status,
	)
	// A queued child transitions ∅ → queued (same as EnqueueTaskFor*): broadcast
	// queued first, then notify the daemon — see EnqueueTaskForIssue for ordering
	// rationale. A deferred child (backoff armed) stays inert until
	// PromoteDueDeferredTasksForRuntime fires its queued event + wakeup.
	if child.Status == "queued" {
		s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, child)
		s.NotifyTaskEnqueued(ctx, child)
	}
	return &child, nil
}

// RerunIssue creates a fresh queued task for an agent on the issue. Used by
// the manual rerun endpoint.
//
// Target agent resolution:
//   - sourceTaskID Valid: rerun the agent that ran that task. This is what
//     the execution log retry button uses so a per-row retry survives a
//     subsequent assignee change and correctly re-fires the mention agent
//     whose row was clicked. The
//     source task's trigger_comment_id is also inherited (when the caller
//     didn't pass one) so a per-row rerun of a comment- or mention-triggered
//     task stays comment-triggered — the daemon's buildCommentPrompt path
//     keys on TriggerCommentID, and losing it would degrade the rerun into
//     a generic issue run that no longer carries the original comment.
//   - sourceTaskID empty: fall back to the issue's current agent assignee.
//     This preserves the CLI / API contract for callers
//     that have an issue ID but no specific task to target.
//
// A retry ALWAYS reuses the source task's workdir when it still exists on
// disk (MUL-4869): a transient failure — network, provider 5xx/rate-limit,
// runtime_offline, timeout, or an auth/quota/config error the user has since
// fixed — should not throw away the work already done. Only the agent SESSION
// is conditionally resumed, and that decision is made later by the daemon claim
// handler from the SOURCE task (via rerun_of_task_id), NOT baked into this row.
// enqueueRerunTask pins force_fresh_session=true so an old claim handler during
// a rolling deploy degrades to a clean start rather than resuming a different
// execution; the new claim handler ignores the flag for reruns and resumes the
// session only when the source failure did not poison the conversation (see
// service.ResumeUnsafeFailure) and the source ran on the same runtime. When the
// dir is objectively unreusable (GC'd, absent on the claiming runtime, or never
// recorded) the daemon falls back to a fresh workdir. Auto-retry of an orphaned
// mid-flight failure (HandleFailedTasks → MaybeRetryFailedTask →
// CreateRetryTask) takes its own path, so MUL-1128's mid-flight resume contract
// is preserved.
//
// ErrRerunInvokeNotAllowed signals that RerunIssue refused to rerun because the
// current operator may not invoke the resolved target agent. The handler maps it
// to a structured 403 (no task was cancelled or created).
var ErrRerunInvokeNotAllowed = errors.New("rerun: operator not allowed to invoke target agent")

// Only tasks belonging to the target agent on this issue are cancelled.
// Tasks owned by other agents on the same issue (e.g. a parallel
// @-mention agent) are left alone — rerun must not collateral-cancel
// them.
//
// canInvoke re-validates that the current operator may invoke the RESOLVED
// target agent, keyed on the historical agent for a task_id rerun and on the
// current assignee otherwise (MUL-4525). It runs AFTER the target is
// resolved but BEFORE any prior task is cancelled or a new one is created, so a
// caller who can see the issue but cannot invoke its private agent cannot use
// rerun as a back door — and a blocked rerun mutates nothing. Pass nil only
// from trusted internal callers (tests, backfill) that have already gated.
func (s *TaskService) RerunIssue(ctx context.Context, issueID pgtype.UUID, sourceTaskID pgtype.UUID, triggerCommentID pgtype.UUID, actorUserID pgtype.UUID, canInvoke func(agent db.Agent) bool) (*db.AgentTaskQueue, error) {
	issue, err := s.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("load issue: %w", err)
	}

	// Determine the target agent for the rerun.
	var (
		agentID             pgtype.UUID
		coalescedCommentIDs []pgtype.UUID
	)
	if sourceTaskID.Valid {
		sourceTask, err := s.Queries.GetAgentTask(ctx, sourceTaskID)
		if err != nil {
			return nil, fmt.Errorf("load source task: %w", err)
		}
		if !sourceTask.IssueID.Valid || util.UUIDToString(sourceTask.IssueID) != util.UUIDToString(issueID) {
			return nil, fmt.Errorf("source task does not belong to this issue")
		}
		agentID = sourceTask.AgentID
		// Inherit trigger provenance so a per-row rerun of a comment- or
		// mention-triggered task stays a comment-triggered task. Without
		// this the daemon's buildCommentPrompt path is skipped (it keys on
		// TriggerCommentID) and the rerun degrades into a generic issue
		// run that has lost the original comment context. Only override
		// when the caller didn't pass one explicitly.
		if !triggerCommentID.Valid {
			coalescedCommentIDs = append([]pgtype.UUID{}, sourceTask.CoalescedCommentIds...)
			sourceTriggerLive := sourceTask.TriggerCommentID.Valid
			if sourceTriggerLive {
				// A trigger deleted while it had replies keeps its row as a
				// tombstone instead of clearing trigger_comment_id; repair the
				// plan exactly as for a removed trigger.
				trigger, err := s.Queries.GetComment(ctx, sourceTask.TriggerCommentID)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return nil, fmt.Errorf("load source trigger comment: %w", err)
				}
				sourceTriggerLive = err == nil && !trigger.DeletedAt.Valid
			}
			if sourceTriggerLive {
				triggerCommentID = sourceTask.TriggerCommentID
			} else if len(coalescedCommentIDs) > 0 {
				triggerCommentID, coalescedCommentIDs, err = s.promoteNewestSurvivingComment(ctx, coalescedCommentIDs)
				if err != nil {
					return nil, fmt.Errorf("repair source comment plan: %w", err)
				}
			}
		}
	} else {
		switch {
		case issue.AssigneeType.String == "agent" && issue.AssigneeID.Valid:
			agentID = issue.AssigneeID
		default:
			return nil, fmt.Errorf("issue is not assigned to an agent")
		}
	}

	// Re-validate invoke permission on the RESOLVED target before mutating
	// anything (MUL-4525). For a task_id rerun this gates the historical agent,
	// so a since-reassigned issue can't be used to re-fire a private agent the
	// operator may only view. A block fails closed: no prior task is cancelled,
	// no new task is created.
	if canInvoke != nil {
		targetAgent, err := s.Queries.GetAgent(ctx, agentID)
		if err != nil {
			return nil, fmt.Errorf("load target agent: %w", err)
		}
		if !canInvoke(targetAgent) {
			return nil, ErrRerunInvokeNotAllowed
		}
	}

	// Clear only the tasks that have not begun executing. Those are the rows the
	// fresh enqueue would collide with under
	// idx_one_pending_task_per_issue_agent_v2, and replacing them costs no work
	// while keeping the new run attributed to the rerunning member (MUL-4302 §5)
	// rather than inheriting whoever created the pending row.
	//
	// A running / waiting_local_directory task is deliberately left alone: an
	// agent is executing in it. Neither status appears in that unique index, so
	// the enqueue below inserts a queued row BEHIND the active one, and
	// ClaimAgentTask's per-(issue, agent) serialization holds it there until the
	// active run reaches a terminal state. Rerun used to cancel these too, so
	// asking an agent for another pass silently killed the pass it was still
	// working on; interrupting an in-flight run is what CancelTask /
	// `multica issue cancel-task` is for.
	clearPendingSlot := func() int {
		var cancelled []db.AgentTaskQueue
		// Atomic with the cancel: a pending coordinator task can already hold a
		// recovery receipt, and nothing downstream could repair a settlement
		// that failed after this committed.
		cerr := s.runInTx(ctx, func(qtx *db.Queries) error {
			var err error
			cancelled, err = qtx.CancelPendingTasksByIssueAndAgentInThread(ctx, db.CancelPendingTasksByIssueAndAgentInThreadParams{
				ThreadCommentID: triggerCommentID,
				IssueID:         issueID,
				AgentID:         agentID,
			})
			if err != nil {
				return err
			}
			return SettleTerminalTaskState(ctx, qtx, cancelled...)
		})
		if cerr != nil {
			slog.Warn("rerun: cancel pending tasks failed",
				"issue_id", util.UUIDToString(issueID),
				"agent_id", util.UUIDToString(agentID),
				"error", cerr,
			)
		}
		for _, t := range cancelled {
			s.captureTaskCancelled(ctx, t)
			s.ReconcileAgentStatus(ctx, t.AgentID)
			s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
		}
		return len(cancelled)
	}
	cancelledCount := clearPendingSlot()

	// A manual rerun is a NEW direct_human trigger attributed to the rerunning
	// member, not the original run's human (MUL-4302 §5); actorUserID carries them.
	// sourceTaskID is the rerun lineage: it rides the CreateAgentTask insert
	// (rerun_of_task_id) so the queued event / daemon claim never sees a NULL
	// lineage, and it stays distinct from system-retry's retry_of_task_id (§5).
	task, err := s.enqueueRerunTask(ctx, issue, agentID, triggerCommentID, coalescedCommentIDs, actorUserID, sourceTaskID)
	if pendingSlotTakenErr(err) {
		// The clear above and this enqueue are separate commits, so a system
		// retry created by a concurrent FailTask can take the pending slot in
		// between. CreateRetryTask yields the slot when it is already occupied,
		// but it cannot yield to a row that does not exist yet, so a retry
		// committing inside this window gets there first. Clear once more and
		// retry: the deliberate human action is the one that should hold the
		// slot. Bounded to a single extra attempt — a second collision would mean
		// something is enqueueing in a loop, which is worth surfacing rather than
		// spinning on.
		slog.Info("issue rerun: pending slot taken concurrently, reclaiming",
			"issue_id", util.UUIDToString(issueID),
			"agent_id", util.UUIDToString(agentID),
		)
		cancelledCount += clearPendingSlot()
		task, err = s.enqueueRerunTask(ctx, issue, agentID, triggerCommentID, coalescedCommentIDs, actorUserID, sourceTaskID)
	}
	if err != nil {
		return nil, err
	}
	slog.Info("issue rerun enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issueID),
		"agent_id", util.UUIDToString(agentID),
		"source_task_id", util.UUIDToString(sourceTaskID),
		"cancelled_pending", cancelledCount,
	)
	return &task, nil
}

// promoteNewestSurvivingComment repairs a manual rerun whose original trigger
// was deleted (the FK clears trigger_comment_id, or the trigger is a tombstone,
// while the UUID-array plan survives). Tombstones never count as survivors.
// Promoting before enqueue lets the normal enqueue path recompute originator
// attribution from the real comment, rather
// than carrying the deleted trigger's stale security context.
func (s *TaskService) promoteNewestSurvivingComment(ctx context.Context, ids []pgtype.UUID) (pgtype.UUID, []pgtype.UUID, error) {
	type survivingComment struct {
		id        pgtype.UUID
		createdAt time.Time
	}
	survivors := make([]survivingComment, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !id.Valid {
			continue
		}
		key := util.UUIDToString(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		comment, err := s.Queries.GetComment(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return pgtype.UUID{}, nil, err
		}
		if comment.DeletedAt.Valid {
			continue
		}
		survivors = append(survivors, survivingComment{id: comment.ID, createdAt: comment.CreatedAt.Time})
	}
	if len(survivors) == 0 {
		return pgtype.UUID{}, nil, nil
	}
	newest := 0
	for i := 1; i < len(survivors); i++ {
		if survivors[i].createdAt.After(survivors[newest].createdAt) ||
			(survivors[i].createdAt.Equal(survivors[newest].createdAt) &&
				util.UUIDToString(survivors[i].id) > util.UUIDToString(survivors[newest].id)) {
			newest = i
		}
	}
	remaining := make([]pgtype.UUID, 0, len(survivors)-1)
	for i, comment := range survivors {
		if i != newest {
			remaining = append(remaining, comment.id)
		}
	}
	return survivors[newest].id, remaining, nil
}

// enqueueRerunTask enqueues a fresh task for the given agent on the issue.
// When the target agent is the issue's single-agent assignee we use the
// assignee-driven path (enqueueIssueTask) so the issue-assignee bookkeeping
// stays in sync; otherwise (prior assignee that has since been reassigned,
// mention agent) we use the mention path.
//
// force_fresh_session is pinned to true on every rerun row on purpose. It is
// the rollback-safe legacy signal: an OLD claim handler (mid rolling deploy)
// gates the whole resume lookup on !force_fresh_session, so it starts clean
// instead of resuming via the (agent, issue) most-recent query — which could
// pick a different execution than the one the user clicked. The NEW claim
// handler ignores this flag for reruns and instead reads the exact source task
// (rerun_of_task_id) to reuse its workdir and, when the failure did not poison
// the conversation, resume its session (MUL-4869).
func (s *TaskService) enqueueRerunTask(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID, coalescedCommentIDs []pgtype.UUID, actorUserID pgtype.UUID, rerunOfTaskID pgtype.UUID) (db.AgentTaskQueue, error) {
	if issue.AssigneeType.String == "agent" && issue.AssigneeID.Valid &&
		util.UUIDToString(issue.AssigneeID) == util.UUIDToString(agentID) {
		return s.enqueueIssueTaskWithCommentPlan(ctx, issue, triggerCommentID, coalescedCommentIDs, true, "", actorUserID, rerunOfTaskID)
	}
	return s.enqueueMentionTaskWithCommentPlan(ctx, issue, agentID, triggerCommentID, coalescedCommentIDs, true, "", actorUserID, rerunOfTaskID)
}

// The bulk terminal writes below are the sweeper, archive and daemon-recovery
// paths that finalize many tasks in one statement. They exist on TaskService rather than
// being called as bare queries so the statement and all terminal-state
// settlement share a transaction.
//
// That is not a stylistic preference. HandleFailedTasks and
// CaptureCancelledTasks run AFTER their caller committed, so a settlement
// issued there could not be rolled back — and could not be repaired either,
// because ListPendingDelegatedFailureRecoveries excludes a comment whose
// covering task is terminal and already holds the receipt. Such a row would
// neither replay nor settle; it would sit in the partial index forever,
// restoring the unbounded history scan this change set removes.

// FailTasksForOfflineRuntimes fails in-flight tasks whose runtime stayed
// offline past the reconnect grace.
func (s *TaskService) FailTasksForOfflineRuntimes(ctx context.Context, arg db.FailTasksForOfflineRuntimesParams) ([]db.AgentTaskQueue, error) {
	return s.terminateTasksInTx(ctx, func(qtx *db.Queries) ([]db.AgentTaskQueue, error) {
		return qtx.FailTasksForOfflineRuntimes(ctx, arg)
	})
}

// FailExpiredRuntimeReconnectRetries fails deferred retries that reached their
// terminal reconnect deadline.
func (s *TaskService) FailExpiredRuntimeReconnectRetries(ctx context.Context, arg db.FailExpiredRuntimeReconnectRetriesParams) ([]db.AgentTaskQueue, error) {
	return s.terminateTasksInTx(ctx, func(qtx *db.Queries) ([]db.AgentTaskQueue, error) {
		return qtx.FailExpiredRuntimeReconnectRetries(ctx, arg)
	})
}

// FailStaleTasks fails claimed work whose runtime stopped reporting.
func (s *TaskService) FailStaleTasks(ctx context.Context, arg db.FailStaleTasksParams) ([]db.AgentTaskQueue, error) {
	return s.terminateTasksInTx(ctx, func(qtx *db.Queries) ([]db.AgentTaskQueue, error) {
		return qtx.FailStaleTasks(ctx, arg)
	})
}

// ExpireStaleQueuedTasks fails queued work whose runtime never came back.
func (s *TaskService) ExpireStaleQueuedTasks(ctx context.Context, arg db.ExpireStaleQueuedTasksParams) ([]db.AgentTaskQueue, error) {
	return s.terminateTasksInTx(ctx, func(qtx *db.Queries) ([]db.AgentTaskQueue, error) {
		return qtx.ExpireStaleQueuedTasks(ctx, arg)
	})
}

// RecoverOrphanedTasksForRuntime fails work a restarted daemon reports it lost.
func (s *TaskService) RecoverOrphanedTasksForRuntime(ctx context.Context, runtimeID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	return s.terminateTasksInTx(ctx, func(qtx *db.Queries) ([]db.AgentTaskQueue, error) {
		return qtx.RecoverOrphanedTasksForRuntime(ctx, runtimeID)
	})
}

// CancelTasksForArchivedAgent cancels every active task belonging to an agent
// being archived and settles their recovery receipts in the same transaction.
//
// After commit, cancellation side effects are captured before chat tasks emit
// task:cancelled so existing consumers can clear processing indicators, release
// streams, and refresh chat state. The caller still publishes agent:archived;
// non-chat tasks keep their existing behavior.
func (s *TaskService) CancelTasksForArchivedAgent(ctx context.Context, agentID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	cancelled, err := s.terminateTasksInTx(ctx, func(qtx *db.Queries) ([]db.AgentTaskQueue, error) {
		return qtx.CancelAgentTasksByAgent(ctx, agentID)
	})
	if err != nil {
		return nil, err
	}
	s.CaptureCancelledTasks(ctx, cancelled)
	for _, task := range cancelled {
		if task.ChatSessionID.Valid {
			s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, task)
		}
	}
	return cancelled, nil
}

func (s *TaskService) terminateTasksInTx(ctx context.Context, fail func(*db.Queries) ([]db.AgentTaskQueue, error)) ([]db.AgentTaskQueue, error) {
	var failed []db.AgentTaskQueue
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		failed, err = fail(qtx)
		if err != nil {
			return err
		}
		return SettleTerminalTaskState(ctx, qtx, failed...)
	}); err != nil {
		return nil, err
	}
	return failed, nil
}

// HandleFailedTasks runs the post-failure side effects for a batch of
// freshly-failed tasks: optional auto-retry, task:failed event broadcast,
// agent status reconciliation, and (when an issue has no remaining active
// task and isn't being retried) resetting the issue back to todo so the
// daemon can pick it up again.
//
// All callers that surface a task as failed — sweepers, FailTask,
// recover-orphans — funnel through here so the same UI-consistency
// guarantees apply on every code path.
func (s *TaskService) HandleFailedTasks(ctx context.Context, tasks []db.AgentTaskQueue) int {
	if len(tasks) == 0 {
		return 0
	}

	affectedAgents := make(map[string]pgtype.UUID)
	processedIssues := make(map[string]bool)
	retriedIssues := make(map[string]bool)
	retried := 0

	for _, t := range tasks {
		// Auto-retry first so the issue stays in_progress rather than
		// flapping todo → in_progress within a tick.
		retryPending := false
		if child, _ := s.MaybeRetryFailedTask(ctx, t); child != nil {
			retryPending = true
			retried++
			if t.IssueID.Valid {
				retriedIssues[util.UUIDToString(t.IssueID)] = true
			}
		}
		if !retryPending {
			if _, err := s.recoverDelegatedTaskFailure(ctx, t); err != nil {
				slog.Warn("handle failed tasks: delegated failure recovery failed",
					"task_id", util.UUIDToString(t.ID),
					"delegated_from_task_id", util.UUIDToString(t.DelegatedFromTaskID),
					"error", err,
				)
			}
		}

		failureReason := "agent_error"
		if t.FailureReason.Valid && t.FailureReason.String != "" {
			failureReason = t.FailureReason.String
		}
		s.captureTaskFailed(ctx, t)

		workspaceID := ""
		if t.IssueID.Valid {
			if issue, err := s.Queries.GetIssue(ctx, t.IssueID); err == nil {
				workspaceID = util.UUIDToString(issue.WorkspaceID)
				// Reset stuck in_progress issues only when no other active
				// task exists for the issue and no retry was just enqueued.
				issueKey := util.UUIDToString(t.IssueID)
				// Only "an agent is actively working" resets, and since
				// MUL-7240 that is the fixed in_progress key alone. in_review
				// and blocked are excluded because a human or an external
				// dependency owns the issue then; a CUSTOM started status is
				// excluded because custom statuses inherit lifecycle only, not
				// the active-status recovery rule. Effective() no longer
				// projects a nonterminal custom key onto a built-in, so this is
				// a key comparison on purpose. (MUL-6243, MUL-7240)
				effectiveStatus := issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, issue.Status)
				if effectiveStatus == "in_progress" && !processedIssues[issueKey] && !retriedIssues[issueKey] {
					processedIssues[issueKey] = true
					hasActive, checkErr := s.Queries.HasActiveTaskForIssue(ctx, t.IssueID)
					if checkErr != nil {
						slog.Warn("handle failed tasks: active check failed",
							"issue_id", issueKey,
							"error", checkErr,
						)
					} else if !hasActive {
						updatedIssue, updateErr := s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
							ID:          t.IssueID,
							Status:      "todo",
							WorkspaceID: issue.WorkspaceID,
						})
						if updateErr != nil {
							slog.Warn("handle failed tasks: reset stuck issue failed",
								"issue_id", issueKey,
								"error", updateErr,
							)
						} else {
							// This direct reset bypasses the HTTP UpdateIssue
							// handler that normally emits issue:updated, so emit
							// it here too. Without it the board / status-filter
							// caches keep showing the issue as in_progress until
							// the next write touches it (#4648 / MUL-3782).
							s.broadcastIssueUpdated(ctx, updatedIssue, issue.Status)
						}
					}
				}
			}
		}
		if workspaceID == "" {
			workspaceID = s.ResolveTaskWorkspaceID(ctx, t)
		}

		s.publishTaskFailedEvent(workspaceID, t, t.Error.String, failureReason, retryPending)

		affectedAgents[util.UUIDToString(t.AgentID)] = t.AgentID
	}

	for _, agentID := range affectedAgents {
		s.ReconcileAgentStatus(ctx, agentID)
	}
	s.notifyTasksFinished(tasks)
	return retried
}

const (
	delegatedFailureErrorSummaryRunes       = 800
	delegatedFailureRecoveryMaxTaskAttempts = 3
	delegatedFailureRecoveryCommentType     = "progress_update"
)

// SettleTerminalTaskState applies every application-owned side effect of a
// task entering a terminal state. It must run with the same qtx as the status
// update so task completion and its dependent receipts commit atomically.
//
// Keeping this as the single terminal-settlement entry point is important:
// task supplements used to rely on an agent_task_queue trigger that performed
// a cross-table update invisibly. Explicit settlement preserves the existing
// task-row -> dependent-row lock order without making task status writes depend
// on database trigger behavior.
func SettleTerminalTaskState(ctx context.Context, q *db.Queries, tasks ...db.AgentTaskQueue) error {
	if len(tasks) == 0 {
		return nil
	}

	taskIDs := make([]pgtype.UUID, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.ID)
	}
	if _, err := q.SettleTerminalTaskSupplements(ctx, taskIDs); err != nil {
		return fmt.Errorf("settle terminal task supplements: %w", err)
	}
	return SettleDeliveredDelegatedFailureRecoveries(ctx, q, tasks...)
}

// SettleDeliveredDelegatedFailureRecoveries retires every delegated-failure
// recovery comment the given now-terminal tasks actually received, so those
// comments drop out of idx_comment_delegated_failure_unsettled instead of
// being re-proven settled by ListPendingDelegatedFailureRecoveries on every
// sweeper tick. Without it the outbox scan grows with total history even when
// it returns nothing.
//
// INVARIANT: every path that moves tasks to a terminal status must reach
// SettleTerminalTaskState with the same qtx as the terminal write — per-task
// writes and bulk cancellations alike, so every marker commits atomically with
// the status change or not at all. A row stranded by a
// committed-but-unsettled terminal write
// cannot be repaired later: ListPendingDelegatedFailureRecoveries excludes a
// comment whose covering task is already terminal and holds its receipt, so
// nothing replays the settlement and nothing else marks it, and the index
// reacquires the unbounded growth it exists to remove.
//
// For the same reason the post-commit helpers — BroadcastCancelledTasks,
// CaptureCancelledTasks, HandleFailedTasks — must never call this: they run
// after their caller has committed, where a failure here can neither roll back
// nor be compensated. Do not reintroduce a best-effort settlement outside the
// transaction; the old one was deleted, not kept.
//
// Call this only once the task is terminal. A dispatched task's receipt is
// still replaceable (SetTaskDeliveredCommentIDs), so settling earlier would
// freeze a legitimate reclaim window into a permanently lost recovery.
// SettleDelegatedFailureRecoveriesForTask re-checks the terminal status in SQL,
// so a mistaken early call updates nothing rather than losing a recovery.
//
// A task with no delivery receipt — nearly every task — costs a slice length
// check and no query.
func SettleDeliveredDelegatedFailureRecoveries(ctx context.Context, q *db.Queries, tasks ...db.AgentTaskQueue) error {
	for _, t := range tasks {
		if len(t.DeliveredCommentIds) == 0 {
			continue
		}
		if _, err := q.SettleDelegatedFailureRecoveriesForTask(ctx, t.ID); err != nil {
			return fmt.Errorf("settle delegated failure recoveries for task %s: %w", util.UUIDToString(t.ID), err)
		}
	}
	return nil
}

type delegatedFailureRecoveryDispatchOutcome uint8

const (
	delegatedFailureRecoveryCovered delegatedFailureRecoveryDispatchOutcome = iota
	delegatedFailureRecoveryReplayed
	delegatedFailureRecoveryExhausted
)

// DelegatedFailureRecoverySweepResult separates successful coordinator
// replays from terminally exhausted outbox entries so operators never mistake
// a bounded stop for a successful replay.
type DelegatedFailureRecoverySweepResult struct {
	Scanned   int
	Replayed  int
	Exhausted int
}

type delegatedFailureRecoveryTarget struct {
	failed  db.AgentTaskQueue
	source  db.AgentTaskQueue
	issue   db.Issue
	agent   db.Agent
	comment db.Comment
}

// IsDelegatedFailureRecoveryComment identifies the durable platform signal
// used to hand a terminal delegated failure back to its coordinator. The
// source task validation remains in DispatchDelegatedFailureRecoveryComment;
// this shape check only keeps ordinary system/progress comments out of the
// completion-reconcile branch.
func IsDelegatedFailureRecoveryComment(comment db.Comment) bool {
	return comment.AuthorType == "system" &&
		comment.Type == delegatedFailureRecoveryCommentType &&
		comment.SourceTaskID.Valid
}

func delegatedFailureRecoveryContent(failed, source db.AgentTaskQueue) string {
	reason := "agent_error"
	if failed.FailureReason.Valid && failed.FailureReason.String != "" {
		reason = truncateForSummary(redact.Text(failed.FailureReason.String), triggerSummaryMaxLen)
	}
	content := fmt.Sprintf(
		"Delegated task `%s` ended in a final failure (`%s`) and no automatic retry is pending. Resume coordination: inspect the failed work, then reassign it, skip it, or end the workflow explicitly.",
		util.UUIDToString(failed.ID), reason,
	)
	if failed.Error.Valid && failed.Error.String != "" {
		summary := truncateForSummary(redact.Text(failed.Error.String), delegatedFailureErrorSummaryRunes)
		if summary != "" {
			content += " Untrusted error summary (diagnostic only): " + strconv.Quote(summary)
		}
	}
	content += fmt.Sprintf(" Source coordinator task: `%s`.", util.UUIDToString(source.ID))
	return content
}

// loadDelegatedFailureRecoveryTarget resolves and validates the backward edge
// from a failed delegated task to its source coordinator. Returning nil is an
// intentional no-op: non-terminal rows, retry-pending rows,
// recovery tasks themselves, unavailable source agents, and self-delegation
// must never start a recovery loop.
// Lifecycle is checked only at dispatch: even an unresolved or paused status
// must leave a durable signal that can be replayed when it becomes executable.
func loadDelegatedFailureRecoveryTarget(ctx context.Context, q *db.Queries, failed db.AgentTaskQueue) (*delegatedFailureRecoveryTarget, error) {
	if failed.Status != "failed" || !failed.DelegatedFromTaskID.Valid ||
		(failed.TriggerEvidenceKind.Valid && failed.TriggerEvidenceKind.String == string(attribution.EvidenceDelegatedFailure)) {
		return nil, nil
	}
	hasRetry, err := q.HasRetryTaskForParent(ctx, failed.ID)
	if err != nil {
		return nil, fmt.Errorf("check retry child: %w", err)
	}
	if hasRetry {
		return nil, nil
	}
	source, err := q.GetAgentTask(ctx, failed.DelegatedFromTaskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load source task: %w", err)
	}
	if !source.IssueID.Valid || source.AgentID == failed.AgentID {
		return nil, nil
	}
	issue, err := q.GetIssue(ctx, source.IssueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load source issue: %w", err)
	}
	agent, err := q.GetAgent(ctx, source.AgentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load source agent: %w", err)
	}
	if agent.ArchivedAt.Valid || !agent.RuntimeID.Valid || agent.WorkspaceID != issue.WorkspaceID {
		return nil, nil
	}
	return &delegatedFailureRecoveryTarget{failed: failed, source: source, issue: issue, agent: agent}, nil
}

// canDispatchDelegatedFailureRecovery matches the lifecycle predicate in
// ListPendingDelegatedFailureRecoveries. It must not gate signal creation:
// catalog failures are retryable only after the outbox comment is committed.
func canDispatchDelegatedFailureRecovery(ctx context.Context, q *db.Queries, issue db.Issue) (bool, error) {
	category, err := issuestatus.CategoryWithError(ctx, q, issue.WorkspaceID, issue.Status)
	if err != nil {
		return false, err
	}
	return issue.Status != issuestatus.Backlog &&
		(category == issuestatus.CategoryUnstarted || category == issuestatus.CategoryStarted), nil
}

// ensureDelegatedFailureRecoveryComment creates one durable recovery signal
// per failed task. The failed row lock serializes FailTask and sweeper callers;
// the comment itself is platform-authored so it does not create subscriber or
// mention-notification side effects.
func (s *TaskService) ensureDelegatedFailureRecoveryComment(ctx context.Context, failedID pgtype.UUID) (*delegatedFailureRecoveryTarget, bool, error) {
	var target *delegatedFailureRecoveryTarget
	created := false
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		failed, err := qtx.GetAgentTaskForDelegatedFailureUpdate(ctx, failedID)
		if err != nil {
			return fmt.Errorf("lock failed task: %w", err)
		}
		target, err = loadDelegatedFailureRecoveryTarget(ctx, qtx, failed)
		if err != nil || target == nil {
			return err
		}
		comment, err := qtx.GetDelegatedFailureRecoveryComment(ctx, db.GetDelegatedFailureRecoveryCommentParams{
			IssueID:      target.issue.ID,
			WorkspaceID:  target.issue.WorkspaceID,
			SourceTaskID: failed.ID,
		})
		if err == nil {
			target.comment = comment
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("find recovery comment: %w", err)
		}
		createdComment, err := qtx.CreateComment(ctx, db.CreateCommentParams{
			ID:           dbid.NewV7(),
			IssueID:      target.issue.ID,
			WorkspaceID:  target.issue.WorkspaceID,
			AuthorType:   "system",
			AuthorID:     pgtype.UUID{Valid: true},
			Content:      delegatedFailureRecoveryContent(target.failed, target.source),
			Type:         delegatedFailureRecoveryCommentType,
			ParentID:     target.source.TriggerCommentID,
			SourceTaskID: failed.ID,
		})
		if err != nil {
			return fmt.Errorf("create recovery comment: %w", err)
		}
		target.comment = createdComment.Comment()
		created = true
		return nil
	}); err != nil {
		return nil, false, err
	}
	if target == nil {
		return nil, false, nil
	}
	if created && s.Bus != nil {
		s.Bus.Publish(events.Event{
			Type:        protocol.EventCommentCreated,
			WorkspaceID: util.UUIDToString(target.issue.WorkspaceID),
			ActorType:   "system",
			ActorID:     "",
			Payload: map[string]any{
				"comment":      commentEventFields(target.comment),
				"issue_title":  target.issue.Title,
				"issue_status": target.issue.Status,
			},
		})
	}
	return target, created, nil
}

func delegatedFailureRecoveryExhaustionContent(target *delegatedFailureRecoveryTarget) string {
	return fmt.Sprintf(
		"Automatic recovery for delegated task `%s` stopped after %d coordinator tasks ended before receiving the recovery signal. No more recovery tasks will be created automatically; resume or dismiss the work manually. Source coordinator task: `%s`.",
		util.UUIDToString(target.failed.ID),
		delegatedFailureRecoveryMaxTaskAttempts,
		util.UUIDToString(target.source.ID),
	)
}

func delegatedFailureRecoveryAttribution(target *delegatedFailureRecoveryTarget) (pgtype.UUID, pgtype.UUID) {
	originator := target.failed.OriginatorUserID
	accountable := target.failed.AccountableUserID
	if originator.Valid {
		accountable = originator
	}
	if !originator.Valid && !accountable.Valid {
		originator = target.source.OriginatorUserID
		accountable = target.source.AccountableUserID
		if originator.Valid {
			accountable = originator
		}
	}
	return originator, accountable
}

// exhaustDelegatedFailureRecovery atomically settles the recovery outbox after
// its bounded automatic attempts, creates one visible system explanation, and
// notifies the responsible human. The bool reports whether this caller created
// that terminal outcome. Updating the newest attempt first serializes
// concurrent sweepers; the second caller then observes the explanation written
// by the first and does not report another exhaustion.
func (s *TaskService) exhaustDelegatedFailureRecovery(ctx context.Context, target *delegatedFailureRecoveryTarget) (bool, error) {
	var exhaustedComment db.Comment
	var exhaustedInbox db.InboxItem
	created := false
	inboxCreated := false
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		if _, err := qtx.AcknowledgeExhaustedDelegatedFailureRecovery(ctx, db.AcknowledgeExhaustedDelegatedFailureRecoveryParams{
			CommentID:    target.comment.ID,
			FailedTaskID: target.failed.ID,
			MaxAttempts:  delegatedFailureRecoveryMaxTaskAttempts,
		}); err != nil {
			return fmt.Errorf("acknowledge exhausted delegated failure recovery: %w", err)
		}

		// The receipt above lands on the newest attempt row, which may still be
		// running, so the task-scoped settle cannot see it. Exhaustion is
		// terminal on its own terms — the attempt budget is spent and the
		// visible explanation below tells the user why nothing else will run —
		// so retire the comment directly, in this same transaction.
		if _, err := qtx.SettleDelegatedFailureRecoveryComment(ctx, target.comment.ID); err != nil {
			return fmt.Errorf("settle exhausted delegated failure recovery: %w", err)
		}

		comment, err := qtx.GetDelegatedFailureRecoveryExhaustionComment(ctx, db.GetDelegatedFailureRecoveryExhaustionCommentParams{
			IssueID:      target.issue.ID,
			WorkspaceID:  target.issue.WorkspaceID,
			SourceTaskID: target.failed.ID,
		})
		if err == nil {
			exhaustedComment = comment
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("find delegated failure exhaustion comment: %w", err)
		}

		createdComment, err := qtx.CreateComment(ctx, db.CreateCommentParams{
			ID:           dbid.NewV7(),
			IssueID:      target.issue.ID,
			WorkspaceID:  target.issue.WorkspaceID,
			AuthorType:   "system",
			AuthorID:     pgtype.UUID{Valid: true},
			Content:      delegatedFailureRecoveryExhaustionContent(target),
			Type:         "system",
			ParentID:     target.source.TriggerCommentID,
			SourceTaskID: target.failed.ID,
		})
		if err != nil {
			return fmt.Errorf("create delegated failure exhaustion comment: %w", err)
		}
		exhaustedComment = createdComment.Comment()
		created = true

		// Exhaustion deliberately does not @mention the coordinator agent: doing
		// so would enqueue a fourth recovery run and defeat the attempt bound.
		// Instead, create one durable action-required inbox item for the human
		// who originated (or is accountable for) the delegated work.
		_, recipient := delegatedFailureRecoveryAttribution(target)
		if recipient.Valid {
			_, err = qtx.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
				UserID:      recipient,
				WorkspaceID: target.issue.WorkspaceID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("validate delegated failure exhaustion recipient: %w", err)
			}

			details, err := json.Marshal(map[string]any{
				"failed_task_id":       util.UUIDToString(target.failed.ID),
				"source_task_id":       util.UUIDToString(target.source.ID),
				"coordinator_agent_id": util.UUIDToString(target.agent.ID),
				"max_attempts":         delegatedFailureRecoveryMaxTaskAttempts,
			})
			if err != nil {
				return fmt.Errorf("encode delegated failure exhaustion details: %w", err)
			}
			exhaustedInbox, err = qtx.CreateInboxItem(ctx, db.CreateInboxItemParams{
				ID:            dbid.NewV7(),
				WorkspaceID:   target.issue.WorkspaceID,
				RecipientType: "member",
				RecipientID:   recipient,
				Type:          "task_failed",
				Severity:      "action_required",
				IssueID:       target.issue.ID,
				Title:         target.issue.Title,
				Body:          pgtype.Text{String: exhaustedComment.Content, Valid: true},
				ActorType:     pgtype.Text{String: "system", Valid: true},
				ActorID:       pgtype.UUID{},
				Details:       details,
			})
			if err != nil {
				return fmt.Errorf("create delegated failure exhaustion inbox item: %w", err)
			}
			inboxCreated = true
		}
		return nil
	}); err != nil {
		return false, err
	}

	if created && s.Bus != nil {
		s.Bus.Publish(events.Event{
			Type:        protocol.EventCommentCreated,
			WorkspaceID: util.UUIDToString(target.issue.WorkspaceID),
			ActorType:   "system",
			ActorID:     "",
			Payload: map[string]any{
				"comment":      commentEventFields(exhaustedComment),
				"issue_title":  target.issue.Title,
				"issue_status": target.issue.Status,
			},
		})
	}
	if inboxCreated && s.Bus != nil {
		s.Bus.Publish(events.Event{
			Type:        protocol.EventInboxNew,
			WorkspaceID: util.UUIDToString(target.issue.WorkspaceID),
			ActorType:   "system",
			ActorID:     "",
			Payload: map[string]any{"item": map[string]any{
				"id":             util.UUIDToString(exhaustedInbox.ID),
				"workspace_id":   util.UUIDToString(exhaustedInbox.WorkspaceID),
				"recipient_type": exhaustedInbox.RecipientType,
				"recipient_id":   util.UUIDToString(exhaustedInbox.RecipientID),
				"type":           exhaustedInbox.Type,
				"severity":       exhaustedInbox.Severity,
				"issue_id":       util.UUIDToPtr(exhaustedInbox.IssueID),
				"title":          exhaustedInbox.Title,
				"body":           util.TextToPtr(exhaustedInbox.Body),
				"read":           exhaustedInbox.Read,
				"archived":       exhaustedInbox.Archived,
				"created_at":     util.TimestampToString(exhaustedInbox.CreatedAt),
				"actor_type":     util.TextToPtr(exhaustedInbox.ActorType),
				"actor_id":       util.UUIDToPtr(exhaustedInbox.ActorID),
				"details":        json.RawMessage(exhaustedInbox.Details),
				"issue_status":   target.issue.Status,
			}},
		})
	}
	return created, nil
}

// dispatchDelegatedFailureRecovery routes a recovery comment to the source
// coordinator without relying on generic mention parsing. A pre-claim task
// absorbs it; otherwise a dedicated queued successor is created. The only
// state that blocks both writes is a dispatched task (its claim is already
// built and the pending-task uniqueness slot is still held), so that narrow
// race records the comment as planned-but-undelivered and lets completion
// reconciliation schedule the follow-up. The three-pass loop closes state
// changes around those writes.
func (s *TaskService) dispatchDelegatedFailureRecovery(ctx context.Context, target *delegatedFailureRecoveryTarget, excludeTaskID pgtype.UUID) (delegatedFailureRecoveryDispatchOutcome, error) {
	// Signal creation has committed before reaching this shared dispatch path.
	// Refresh the issue because its status may have changed since creation or
	// sweep selection; a paused/unreadable lifecycle never settles the signal.
	issue, err := s.Queries.GetIssue(ctx, target.issue.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return delegatedFailureRecoveryCovered, nil
	}
	if err != nil {
		return delegatedFailureRecoveryCovered, fmt.Errorf("reload recovery source issue: %w", err)
	}
	allowed, err := canDispatchDelegatedFailureRecovery(ctx, s.Queries, issue)
	if err != nil || !allowed {
		return delegatedFailureRecoveryCovered, err
	}
	target.issue = issue
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		covered, err := s.Queries.HasTaskCoveringDelegatedFailureComment(ctx, db.HasTaskCoveringDelegatedFailureCommentParams{
			IssueID:       target.issue.ID,
			AgentID:       target.agent.ID,
			CommentID:     target.comment.ID,
			ExcludeTaskID: excludeTaskID,
		})
		if err != nil {
			return delegatedFailureRecoveryCovered, fmt.Errorf("check recovery coverage: %w", err)
		}
		if covered {
			return delegatedFailureRecoveryCovered, nil
		}

		recoveryTasks, err := s.Queries.CountDelegatedFailureRecoveryTasks(ctx, target.failed.ID)
		if err != nil {
			return delegatedFailureRecoveryCovered, fmt.Errorf("count delegated failure recovery tasks: %w", err)
		}
		if recoveryTasks >= delegatedFailureRecoveryMaxTaskAttempts {
			exhausted, err := s.exhaustDelegatedFailureRecovery(ctx, target)
			if err != nil {
				return delegatedFailureRecoveryCovered, err
			}
			if exhausted {
				return delegatedFailureRecoveryExhausted, nil
			}
			return delegatedFailureRecoveryCovered, nil
		}

		if merged, err := s.Queries.MergeDelegatedFailureCommentIntoPendingTask(ctx, db.MergeDelegatedFailureCommentIntoPendingTaskParams{
			CommentID:      target.comment.ID,
			TriggerSummary: s.buildCommentTriggerSummary(ctx, target.issue.WorkspaceID, target.comment.ID),
			IssueID:        target.issue.ID,
			AgentID:        target.agent.ID,
		}); err == nil {
			slog.Info("delegated failure recovery merged into pending coordinator task",
				"failed_task_id", util.UUIDToString(target.failed.ID),
				"coordinator_task_id", util.UUIDToString(merged.ID),
			)
			return delegatedFailureRecoveryReplayed, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return delegatedFailureRecoveryCovered, fmt.Errorf("merge recovery into pending task: %w", err)
		}

		originator, accountable := delegatedFailureRecoveryAttribution(target)
		source := attribution.SourceDelegation
		if !originator.Valid && !accountable.Valid {
			source = attribution.SourceUnattributed
		}
		ruleVersionID := target.failed.RuleVersionID
		if !ruleVersionID.Valid {
			ruleVersionID = target.source.RuleVersionID
		}
		task, err := s.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
			ID:                   dbid.NewV7(),
			AgentID:              target.agent.ID,
			RuntimeID:            target.agent.RuntimeID,
			IssueID:              target.issue.ID,
			Priority:             priorityToInt(target.issue.Priority),
			TriggerCommentID:     target.comment.ID,
			TriggerSummary:       s.buildCommentTriggerSummary(ctx, target.issue.WorkspaceID, target.comment.ID),
			OriginatorUserID:     originator,
			AccountableUserID:    accountable,
			OriginatorSource:     pgtype.Text{String: string(source), Valid: true},
			DelegatedFromTaskID:  target.failed.ID,
			RuleVersionID:        ruleVersionID,
			TriggerEvidenceKind:  pgtype.Text{String: string(attribution.EvidenceDelegatedFailure), Valid: true},
			TriggerEvidenceRefID: target.failed.ID,
			HeadSha:              headShaText(s.ResolveIssueReviewSHA(ctx, target.issue.ID)),
		})
		if err == nil {
			slog.Info("delegated failure recovery task enqueued",
				"failed_task_id", util.UUIDToString(target.failed.ID),
				"source_task_id", util.UUIDToString(target.source.ID),
				"recovery_task_id", util.UUIDToString(task.ID),
				"coordinator_agent_id", util.UUIDToString(target.agent.ID),
			)
			s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
			s.NotifyTaskEnqueued(ctx, task)
			return delegatedFailureRecoveryReplayed, nil
		}
		if !isDuplicatePendingTaskErr(err) {
			return delegatedFailureRecoveryCovered, fmt.Errorf("create recovery task: %w", err)
		}

		// A dispatched task still owns the unique queued/dispatched slot, but
		// its claim payload is immutable. Register the comment as undelivered so
		// its completion reconciliation creates the successor. Running tasks do
		// not own that slot, so they took the durable queued-successor path above.
		if active, err := s.Queries.RegisterPlannedCommentForActiveTask(ctx, db.RegisterPlannedCommentForActiveTaskParams{
			CommentID: target.comment.ID,
			IssueID:   target.issue.ID,
			AgentID:   target.agent.ID,
		}); err == nil {
			slog.Info("delegated failure recovery registered behind dispatched coordinator task",
				"failed_task_id", util.UUIDToString(target.failed.ID),
				"coordinator_task_id", util.UUIDToString(active.ID),
			)
			return delegatedFailureRecoveryReplayed, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return delegatedFailureRecoveryCovered, fmt.Errorf("register recovery on dispatched task: %w", err)
		}
	}
	return delegatedFailureRecoveryCovered, fmt.Errorf("delegate failure recovery could not acquire coordinator task slot")
}

// recoverDelegatedTaskFailure is the shared post-terminal hook for FailTask and
// HandleFailedTasks. handled reports whether the failure was an eligible
// delegated terminal; the production terminal paths deliberately retain their
// legacy raw-error notice alongside the richer coordinator recovery signal.
func (s *TaskService) recoverDelegatedTaskFailure(ctx context.Context, failed db.AgentTaskQueue) (handled bool, err error) {
	target, _, err := s.ensureDelegatedFailureRecoveryComment(ctx, failed.ID)
	if err != nil || target == nil {
		return false, err
	}
	_, err = s.dispatchDelegatedFailureRecovery(ctx, target, pgtype.UUID{})
	return true, err
}

// RecoverPendingDelegatedFailures replays the durable recovery outbox. The
// platform recovery comment is the obligation; it is complete only while a task
// that carries it can still execute, or after a task records it in
// delivered_comment_ids. This lets a later sweeper repair a process crash or
// transient database error between comment creation and coordinator dispatch
// without producing duplicate runnable tasks.
func (s *TaskService) RecoverPendingDelegatedFailures(ctx context.Context, maxPerTick int32) (DelegatedFailureRecoverySweepResult, error) {
	result := DelegatedFailureRecoverySweepResult{}
	if maxPerTick <= 0 {
		return result, nil
	}
	pending, err := s.Queries.ListPendingDelegatedFailureRecoveries(ctx, maxPerTick)
	if err != nil {
		return result, fmt.Errorf("list pending delegated failure recoveries: %w", err)
	}
	result.Scanned = len(pending)

	errs := make([]error, 0)
	for _, comment := range pending {
		outcome, recoveryErr := s.dispatchDelegatedFailureRecoveryComment(ctx, comment, pgtype.UUID{})
		if recoveryErr != nil {
			errs = append(errs, fmt.Errorf("dispatch recovery comment %s: %w", util.UUIDToString(comment.ID), recoveryErr))
			continue
		}
		switch outcome {
		case delegatedFailureRecoveryReplayed:
			result.Replayed++
		case delegatedFailureRecoveryExhausted:
			result.Exhausted++
		}
	}
	return result, errors.Join(errs...)
}

// DispatchDelegatedFailureRecoveryComment is used by completion reconciliation
// when a recovery signal arrived after a coordinator task was claimed. The
// completed task is excluded from the coverage check because the comment was
// planned but not delivered to it; routing then merges/enqueues exactly one
// follow-up.
func (s *TaskService) DispatchDelegatedFailureRecoveryComment(ctx context.Context, comment db.Comment, completedTaskID pgtype.UUID) error {
	_, err := s.dispatchDelegatedFailureRecoveryComment(ctx, comment, completedTaskID)
	return err
}

func (s *TaskService) dispatchDelegatedFailureRecoveryComment(ctx context.Context, comment db.Comment, completedTaskID pgtype.UUID) (delegatedFailureRecoveryDispatchOutcome, error) {
	if !IsDelegatedFailureRecoveryComment(comment) {
		return delegatedFailureRecoveryCovered, nil
	}
	failed, err := s.Queries.GetAgentTask(ctx, comment.SourceTaskID)
	if err != nil {
		return delegatedFailureRecoveryCovered, fmt.Errorf("load failed recovery source: %w", err)
	}
	target, err := loadDelegatedFailureRecoveryTarget(ctx, s.Queries, failed)
	if err != nil || target == nil {
		return delegatedFailureRecoveryCovered, err
	}
	if target.issue.ID != comment.IssueID || target.issue.WorkspaceID != comment.WorkspaceID {
		return delegatedFailureRecoveryCovered, fmt.Errorf("delegated failure recovery comment scope mismatch")
	}
	target.comment = comment
	return s.dispatchDelegatedFailureRecovery(ctx, target, completedTaskID)
}

// runInTx executes fn inside a single DB transaction. If TxStarter is nil
// (e.g. some tests construct TaskService directly), fn runs against the
// regular Queries handle without transactional guarantees.
func (s *TaskService) runInTx(ctx context.Context, fn func(*db.Queries) error) error {
	if s.TxStarter == nil {
		return fn(s.Queries)
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReportProgress broadcasts a progress update via the event bus.
func (s *TaskService) ReportProgress(ctx context.Context, taskID string, workspaceID string, summary string, step, total int) {
	s.Bus.Publish(events.Event{
		Type:        protocol.EventTaskProgress,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		ActorID:     "",
		TaskID:      taskID,
		Payload: protocol.TaskProgressPayload{
			TaskID:  taskID,
			Summary: summary,
			Step:    step,
			Total:   total,
		},
	})
}

// ReconcileAgentStatus refreshes agent status from the current working task
// set. The query returns no row when the status is already correct, which
// avoids rewriting updated_at and broadcasting a zero-information event.
func (s *TaskService) ReconcileAgentStatus(ctx context.Context, agentID pgtype.UUID) {
	agent, err := s.Queries.RefreshAgentStatusFromTasks(ctx, agentID)
	if err != nil {
		return
	}
	slog.Debug("agent status reconciled", "agent_id", util.UUIDToString(agentID), "status", agent.Status)
	s.publishAgentStatus(agent)
}

func (s *TaskService) updateAgentStatus(ctx context.Context, agentID pgtype.UUID, status string) {
	agent, err := s.Queries.UpdateAgentStatus(ctx, db.UpdateAgentStatusParams{
		ID:     agentID,
		Status: status,
	})
	if err != nil {
		return
	}
	s.publishAgentStatus(agent)
}

func (s *TaskService) publishAgentStatus(agent db.Agent) {
	s.Bus.Publish(events.Event{
		Type:        protocol.EventAgentStatus,
		WorkspaceID: util.UUIDToString(agent.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload:     map[string]any{"agent": agentToMap(agent)},
	})
}

// computeChatElapsedMs returns the wall-clock duration from task creation
// (user hit send) to terminal state (completed/failed). Stored on the
// assistant chat_message so the UI can render "Replied in 38s" /
// "Failed after 12s". Uses created_at — not started_at — because users
// experience total wait time, including queue + dispatch, not just the
// daemon's actual run time.
func computeChatElapsedMs(task db.AgentTaskQueue) pgtype.Int8 {
	if !task.CompletedAt.Valid || !task.CreatedAt.Valid {
		return pgtype.Int8{}
	}
	ms := task.CompletedAt.Time.Sub(task.CreatedAt.Time).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return pgtype.Int8{Int64: ms, Valid: true}
}

func priorityToInt(p string) int32 {
	switch p {
	case "urgent":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// NotifyTaskEnqueued is called after a row is inserted into
// agent_task_queue. Invalidates the empty-claim
// cache and kicks the daemon WS so the new task is claimed without
// waiting for the next poll.
func (s *TaskService) NotifyTaskEnqueued(ctx context.Context, task db.AgentTaskQueue) {
	s.captureTaskQueued(ctx, task)
	s.notifyTaskAvailable(task)
}

// NotifyTaskFinished invalidates a runtime's empty-claim verdict and emits a
// best-effort daemon wakeup after a task reaches a terminal state. The task ID
// is deliberately omitted from the wakeup payload: the completed task itself
// is not available; the hint only means that a queued successor may have
// become claimable because an agent-capacity or serialization barrier cleared.
func (s *TaskService) NotifyTaskFinished(task db.AgentTaskQueue) {
	s.forgetTaskReclaim(task)
	s.notifyRuntimeMayHaveWork(task.RuntimeID, "")
}

// notifyTasksFinished is the batch form used by bulk terminal transitions.
// Coalesce by runtime so cancelling many tasks on one machine produces one
// cache bump and one websocket hint rather than a burst of identical work.
func (s *TaskService) notifyTasksFinished(tasks []db.AgentTaskQueue) {
	seen := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		if !task.RuntimeID.Valid {
			continue
		}
		s.forgetTaskReclaim(task)
		runtimeKey := util.UUIDToString(task.RuntimeID)
		if _, ok := seen[runtimeKey]; ok {
			continue
		}
		seen[runtimeKey] = struct{}{}
		s.notifyRuntimeMayHaveWork(task.RuntimeID, "")
	}
}

// notifyTaskAvailable runs after a task has been inserted: bumps the
// runtime's invalidation version so any in-flight claim that is about
// to write an "empty" verdict will have it rejected on read, then
// kicks the daemon WS so the daemon claims without waiting for its
// next poll. Order matters — Bump must happen before the wakeup,
// otherwise the wakeup-driven claim could read the still-current
// empty verdict and return null.
func (s *TaskService) notifyTaskAvailable(task db.AgentTaskQueue) {
	s.notifyRuntimeMayHaveWork(task.RuntimeID, util.UUIDToString(task.ID))
}

// notifyRuntimeMayHaveWork is the shared bump-before-wakeup primitive for both
// fresh enqueues and terminal transitions that can unblock queued work.
func (s *TaskService) notifyRuntimeMayHaveWork(runtimeID pgtype.UUID, taskID string) {
	if !runtimeID.Valid {
		return
	}
	runtimeKey := util.UUIDToString(runtimeID)
	// Use a background context: the cache bump / wakeup must outlive
	// the request that created the task, otherwise an early client
	// disconnect could leave the empty verdict in place and stall the
	// just-queued task until the TTL expires. The cache itself bounds
	// every Redis call with a short timeout so a wedged Redis cannot
	// block enqueue.
	s.EmptyClaim.Bump(context.Background(), runtimeKey)
	if s.Wakeup == nil {
		return
	}
	s.Wakeup.NotifyTaskAvailable(runtimeKey, taskID)
}

func (s *TaskService) broadcastTaskDispatch(ctx context.Context, task db.AgentTaskQueue) {
	var payload map[string]any
	if task.Context != nil {
		json.Unmarshal(task.Context, &payload)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["task_id"] = util.UUIDToString(task.ID)
	payload["runtime_id"] = util.UUIDToString(task.RuntimeID)
	payload["issue_id"] = util.UUIDToString(task.IssueID)
	payload["agent_id"] = util.UUIDToString(task.AgentID)
	// chat_session_id is the routing key the chat window uses to writethrough
	// `chatKeys.pendingTask` to status="running" the moment the daemon claims
	// the task. Without it the pill stays stuck at "Queued" until completion.
	if task.ChatSessionID.Valid {
		payload["chat_session_id"] = util.UUIDToString(task.ChatSessionID)
	}

	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	if workspaceID == "" {
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventTaskDispatch,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		ActorID:     "",
		Payload:     payload,
	})
}

// taskEvent builds the shared task-lifecycle event contract. Scope hints are
// duplicated on the envelope intentionally: current listeners remain
// compatible with the payload map, while the realtime layer can route without
// decoding it once per-resource fanout is enabled.
func taskEvent(eventType, workspaceID string, task db.AgentTaskQueue, extra ...map[string]any) events.Event {
	payload := map[string]any{
		"task_id":  util.UUIDToString(task.ID),
		"agent_id": util.UUIDToString(task.AgentID),
		"issue_id": util.UUIDToString(task.IssueID),
		"status":   task.Status,
	}
	e := events.Event{
		Type:        eventType,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		ActorID:     "",
		TaskID:      util.UUIDToString(task.ID),
		Payload:     payload,
	}
	if task.ChatSessionID.Valid {
		chatSessionID := util.UUIDToString(task.ChatSessionID)
		payload["chat_session_id"] = chatSessionID
		e.ChatSessionID = chatSessionID
	}
	for _, fields := range extra {
		for key, value := range fields {
			payload[key] = value
		}
	}
	return e
}

func (s *TaskService) publishTaskEvent(eventType, workspaceID string, task db.AgentTaskQueue, extra ...map[string]any) {
	if workspaceID == "" {
		return
	}
	s.Bus.Publish(taskEvent(eventType, workspaceID, task, extra...))
}

func (s *TaskService) broadcastTaskEvent(ctx context.Context, eventType string, task db.AgentTaskQueue, extra ...map[string]any) {
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	s.publishTaskEvent(eventType, workspaceID, task, extra...)
}

// taskFailedFields adds the terminal failure context without changing the
// long-standing map payload used by existing task event consumers. Error text is redacted and omitted while an automatic
// retry is pending, so consumers can distinguish an intermediate failed
// attempt from a user-visible terminal failure.
func taskFailedFields(errMsg, failureReason string, retryPending bool) map[string]any {
	fields := map[string]any{
		"failure_reason": failureReason,
		"retry_pending":  retryPending,
	}
	if errMsg != "" && !retryPending {
		fields["error"] = redact.Text(errMsg)
	}
	return fields
}

func (s *TaskService) publishTaskFailedEvent(workspaceID string, task db.AgentTaskQueue, errMsg, failureReason string, retryPending bool) {
	s.publishTaskEvent(protocol.EventTaskFailed, workspaceID, task, taskFailedFields(errMsg, failureReason, retryPending))
}

func (s *TaskService) broadcastTaskFailedEvent(ctx context.Context, task db.AgentTaskQueue, errMsg, failureReason string, retryPending bool) {
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	s.publishTaskFailedEvent(workspaceID, task, errMsg, failureReason, retryPending)
}

// ResolveTaskWorkspaceID determines the workspace ID for a task, best-effort.
// Returns "" when the workspace could not be determined, whether because the
// link target is genuinely gone or because a lookup failed.
//
// Use this only where "" is an acceptable answer — event broadcasts skip
// themselves rather than fabricating a workspace. Anything that turns the
// result into an HTTP status MUST use ResolveTaskWorkspaceIDChecked instead:
// collapsing both cases to "" is what let a transient DB error be reported to
// the daemon as `404 task not found`, which it acts on by killing a healthy
// run (MUL-7259 / GH #8272).
func (s *TaskService) ResolveTaskWorkspaceID(ctx context.Context, task db.AgentTaskQueue) string {
	workspaceID, _ := s.ResolveTaskWorkspaceIDChecked(ctx, task)
	return workspaceID
}

// ResolveTaskWorkspaceIDChecked resolves a task's workspace and keeps the two
// failure modes apart:
//
//   - ("", nil)  — every link this task carries was looked up successfully and
//     the target is genuinely absent. The task is unreachable; a 404 is honest.
//   - ("", err)  — a lookup could not be completed (DB timeout, pool exhaustion,
//     …). We do not know whether the task is reachable, and the caller must NOT
//     report absence. This is a 5xx.
//
// For issue tasks the workspace comes from the issue and for chat tasks from
// the chat session.
//
// A failed lookup does not stop the walk. If a later link resolves, its
// workspace is returned and the earlier error is dropped, because that answer
// is still trustworthy; the error is surfaced only when nothing resolved. That
// keeps a task carrying several links working during a partial outage while
// still refusing to call an unknown state "not found".
func (s *TaskService) ResolveTaskWorkspaceIDChecked(ctx context.Context, task db.AgentTaskQueue) (string, error) {
	// isNotFound is the "genuinely absent" signal; every other error means the
	// lookup itself did not complete. pgx.ErrNoRows is the only error the
	// queries below use to say "this row does not exist".
	var lookupErr error
	note := func(err error) {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) && lookupErr == nil {
			lookupErr = err
		}
	}

	if task.IssueID.Valid {
		issue, err := s.Queries.GetIssue(ctx, task.IssueID)
		if err == nil {
			return util.UUIDToString(issue.WorkspaceID), nil
		}
		note(err)
	}
	if task.ChatSessionID.Valid {
		cs, err := s.Queries.GetChatSession(ctx, task.ChatSessionID)
		if err == nil {
			return util.UUIDToString(cs.WorkspaceID), nil
		}
		note(err)
	}
	if lookupErr != nil {
		return "", fmt.Errorf("resolve task workspace: %w", lookupErr)
	}
	return "", nil
}

func (s *TaskService) broadcastChatDone(ctx context.Context, task db.AgentTaskQueue, msg *db.ChatMessage) {
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	if workspaceID == "" {
		return
	}
	payload := protocol.ChatDonePayload{
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		TaskID:        util.UUIDToString(task.ID),
	}
	if msg != nil {
		payload.MessageID = util.UUIDToString(msg.ID)
		payload.Content = msg.Content
		payload.MessageKind = msg.MessageKind
		if msg.CreatedAt.Valid {
			payload.CreatedAt = msg.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		if msg.ElapsedMs.Valid {
			payload.ElapsedMs = msg.ElapsedMs.Int64
		}
	}
	s.Bus.Publish(events.Event{
		Type:          protocol.EventChatDone,
		WorkspaceID:   workspaceID,
		ActorType:     "system",
		ActorID:       "",
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		Payload:       payload,
	})
}

// broadcastIssueUpdated publishes the issue:updated event the frontend's
// realtime reconcile (onIssueUpdated) relies on to move an issue between status
// columns / status filters and reconcile their bucket counts. prevStatus is the
// issue's status before the write so the client can gate that reconcile on
// status_changed.
//
// The `issue` payload is a map (IssueToMap), which the workspace WS fanout
// (listeners.go SubscribeAll) marshals and broadcasts as-is — that is what
// drives the UI reconcile. Note this does NOT cover the full HTTP UpdateIssue
// side effects: the activity-log and inbox listeners type-assert `issue` to a
// handler.IssueResponse and skip a map, so a background status reset does not
// emit status-change activity / notifications. That is intentional for the
// realtime-staleness fix (#4648 / MUL-3782); folding those side effects in
// would mean unifying the payload type and is left as a follow-up.
func (s *TaskService) broadcastIssueUpdated(ctx context.Context, issue db.Issue, prevStatus string) {
	prefix := s.getIssuePrefix(issue.WorkspaceID)
	s.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"issue":          IssueToMapResolved(ctx, s.Queries, issue, prefix),
			"status_changed": prevStatus != issue.Status,
			"prev_status":    prevStatus,
		},
	})
}

func (s *TaskService) getIssuePrefix(workspaceID pgtype.UUID) string {
	ws, err := s.Queries.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return ""
	}
	return ws.IssuePrefix
}

// commentEventFields renders the `comment` object carried by comment:created
// broadcasts published outside the HTTP handler (agent replies, delegated
// failure recovery, recovery exhaustion).
//
// created_at goes through util.TimestampToString — the SAME helper the REST
// CommentResponse uses — so the timestamp a client receives over WS is
// byte-identical to the one it gets when it refetches that comment. These
// payloads used to format the time with a literal "Z" suffix instead, which
// is not Go's Z07:00 offset token: pgx decodes timestamptz into the process
// location (pgtype/timestamptz.go, no ScanLocation configured), so on a
// deployment whose process TZ is not UTC the broadcast stamped local
// wall-clock digits with a UTC label. Clients then placed the entry a whole
// offset away from its real instant and moved it again once a refetch
// replaced the value — visible as timeline entries jumping position.
func commentEventFields(c db.Comment) map[string]any {
	return map[string]any{
		"id":             util.UUIDToString(c.ID),
		"issue_id":       util.UUIDToString(c.IssueID),
		"author_type":    c.AuthorType,
		"author_id":      util.UUIDToString(c.AuthorID),
		"content":        c.Content,
		"type":           c.Type,
		"parent_id":      util.UUIDToPtr(c.ParentID),
		"source_task_id": util.UUIDToPtr(c.SourceTaskID),
		"created_at":     util.TimestampToString(c.CreatedAt),
	}
}

func (s *TaskService) createAgentComment(ctx context.Context, issueID, agentID pgtype.UUID, content, commentType string, parentID, sourceTaskID pgtype.UUID) {
	if content == "" {
		return
	}
	// Look up issue to get workspace ID for mention expansion and broadcasting.
	issue, err := s.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return
	}
	// Resolve the thread root for thread-level side effects without overwriting
	// parentID. The stored parent_id must remain the exact comment being replied
	// to; recursive thread reads recover the root when needed.
	var rootComment *db.Comment
	if parentID.Valid {
		if root, err := s.Queries.GetThreadRoot(ctx, db.GetThreadRootParams{
			CommentID:   parentID,
			WorkspaceID: issue.WorkspaceID,
		}); err == nil {
			rootComment = &root
		}
	}
	created, err := s.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:           dbid.NewV7(),
		IssueID:      issueID,
		WorkspaceID:  issue.WorkspaceID,
		AuthorType:   "agent",
		AuthorID:     agentID,
		Content:      content,
		Type:         commentType,
		ParentID:     parentID,
		SourceTaskID: sourceTaskID,
	})
	if err != nil {
		return
	}
	comment := created.Comment()
	commentFields := commentEventFields(comment)
	commentFields["revision"] = comment.Revision
	s.Bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "agent",
		ActorID:     util.UUIDToString(agentID),
		Payload: map[string]any{
			"comment":        commentFields,
			"issue_title":    issue.Title,
			"issue_status":   issue.Status,
			"issue_revision": created.IssueRevision,
		},
	})
	s.AutoUnresolveThreadOnReply(ctx, rootComment, util.UUIDToString(issue.WorkspaceID), "agent", util.UUIDToString(agentID))
}

// AutoUnresolveThreadOnReply clears resolved_at on the thread root when a
// reply lands in a resolved thread, and broadcasts comment:unresolved. Shared
// between the user-facing Handler.CreateComment path and the agent-facing
// TaskService.createAgentComment path so the resolved-then-replied state can
// never desync (one of the bugs Emacs flagged on PR #2300). Errors are logged
// — the reply itself already committed, the desync is recoverable on next read.
func (s *TaskService) AutoUnresolveThreadOnReply(ctx context.Context, parent *db.Comment, workspaceID, actorType, actorID string) {
	if parent == nil || !parent.ResolvedAt.Valid {
		return
	}
	updated, err := s.Queries.UnresolveComment(ctx, parent.ID)
	if err != nil {
		slog.Warn("auto-unresolve on reply failed", "error", err, "comment_id", util.UUIDToString(parent.ID))
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventCommentUnresolved,
		WorkspaceID: workspaceID,
		ActorType:   actorType,
		ActorID:     actorID,
		Payload: map[string]any{
			"comment": map[string]any{
				"id":               util.UUIDToString(updated.ID),
				"issue_id":         util.UUIDToString(updated.IssueID),
				"author_type":      updated.AuthorType,
				"author_id":        util.UUIDToString(updated.AuthorID),
				"content":          updated.Content,
				"type":             updated.Type,
				"parent_id":        util.UUIDToPtr(updated.ParentID),
				"created_at":       util.TimestampToString(updated.CreatedAt),
				"updated_at":       util.TimestampToString(updated.UpdatedAt),
				"resolved_at":      util.TimestampToPtr(updated.ResolvedAt),
				"resolved_by_type": util.TextToPtr(updated.ResolvedByType),
				"resolved_by_id":   util.UUIDToPtr(updated.ResolvedByID),
				"revision":         updated.Revision,
			},
		},
	})
}

// IssueToMap renders an issue row as the map shape the issue:created /
// issue:updated broadcast payloads carry under their "issue" key. It is the
// single source of truth for that shape wherever the event is published from
// outside the HTTP handler — the background stuck-issue status reset on
// issue:updated.
// The workspace WS fanout marshals it as-is for the UI, and cmd/server's
// extractIssueFields reads id / creator_id / workspace_id off it to decide who
// to auto-subscribe.
//
// The map must stay key-compatible with handler.IssueResponse, the other
// rendering of the same event. Clients type both as a complete Issue and
// insert it straight into the list cache without runtime validation, so a
// field missing here is a field that reads back undefined until the next
// refetch — see TestIssueToMap_KeysMatchIssueResponse, which fails if the two
// renderings drift apart.
// builtInStatusCategory returns a status's public lifecycle category when it
// can be known without a catalog read.
func builtInStatusCategory(status string) string {
	if category, ok := issuestatus.CategoryForBehavior(status); ok {
		return issuestatus.WireCategory(status, category)
	}
	return ""
}

// IssueToMapResolved is IssueToMap with an AUTHORITATIVE status_category and
// status_name, both resolved through the catalog so a custom status is not
// emitted with blanks. Background events go through here; clients treat this
// payload as a complete issue and bucket it by category. (MUL-6243)
//
// Both fields come from ONE catalog read. Resolving them separately would
// double the query on every event carrying a custom status, and the HTTP
// rendering already shares a single read through its Resolver. (MUL-6749)
func IssueToMapResolved(ctx context.Context, q issuestatus.Querier, issue db.Issue, issuePrefix string) map[string]any {
	m := IssueToMap(issue, issuePrefix)
	category, name := issuestatus.CategoryAndName(ctx, q, issue.WorkspaceID, issue.Status)
	m["status_category"] = issuestatus.WireCategory(issue.Status, category)
	m["status_name"] = name
	return m
}

func IssueToMap(issue db.Issue, issuePrefix string) map[string]any {
	m := map[string]any{
		"id":           util.UUIDToString(issue.ID),
		"workspace_id": util.UUIDToString(issue.WorkspaceID),
		"number":       issue.Number,
		"identifier":   IssueIdentifier(issuePrefix, issue.Number),
		"title":        issue.Title,
		"description":  util.TextToPtr(issue.Description),
		"status":       issue.Status,
		// Mirrors handler.IssueResponse.StatusCategory. Built-ins map to a
		// public lifecycle category without a catalog lookup; custom statuses
		// are filled by IssueToMapResolved. (MUL-6243)
		"status_category": builtInStatusCategory(issue.Status),
		// Mirrors handler.IssueResponse.StatusName. A built-in carries no name
		// — clients localize those from the key — and a CUSTOM one is filled in
		// by IssueToMapResolved, which has the catalog. Emitted unconditionally
		// so this rendering cannot lose a key the HTTP one carries. (MUL-6749)
		"status_name":      "",
		"priority":         issue.Priority,
		"assignee_type":    util.TextToPtr(issue.AssigneeType),
		"assignee_id":      util.UUIDToPtr(issue.AssigneeID),
		"creator_type":     issue.CreatorType,
		"creator_id":       util.UUIDToString(issue.CreatorID),
		"parent_issue_id":  util.UUIDToPtr(issue.ParentIssueID),
		"project_id":       util.UUIDToPtr(issue.ProjectID),
		"position":         issue.Position,
		"stage":            util.Int4ToPtr(issue.Stage),
		"start_date":       util.DateToPtr(issue.StartDate),
		"due_date":         util.DateToPtr(issue.DueDate),
		"created_at":       util.TimestampToString(issue.CreatedAt),
		"updated_at":       util.TimestampToString(issue.UpdatedAt),
		"last_activity_at": util.TimestampToNanoPtr(issue.LastActivityAt),
		"revision":         issue.Revision,
		"metadata":         util.JSONObjectOrEmpty(issue.Metadata),
		"properties":       util.JSONObjectOrEmpty(issue.Properties),
	}
	// Mirrors handler.IssueResponse.OriginType, which is omitempty.
	if issue.OriginType.Valid {
		m["origin_type"] = issue.OriginType.String
	}
	return m
}

// IssueIdentifier renders the human-facing issue key ("MUL-42"). Callers that
// resolve the workspace prefix defensively may pass "": a failed workspace
// lookup should not surface as a stray "-42", so the number stands alone as
// "#42". The HTTP layer never passes "" — handler.getIssuePrefix derives a
// prefix from the workspace name before rendering anything.
func IssueIdentifier(issuePrefix string, number int32) string {
	if issuePrefix == "" {
		return "#" + strconv.Itoa(int(number))
	}
	return issuePrefix + "-" + strconv.Itoa(int(number))
}

// agentToMap builds a simple map for broadcasting agent status updates.
func agentToMap(a db.Agent) map[string]any {
	var rc any
	if a.RuntimeConfig != nil {
		json.Unmarshal(a.RuntimeConfig, &rc)
	}
	return map[string]any{
		"id":                   util.UUIDToString(a.ID),
		"workspace_id":         util.UUIDToString(a.WorkspaceID),
		"runtime_id":           util.UUIDToString(a.RuntimeID),
		"name":                 a.Name,
		"description":          a.Description,
		"avatar_url":           util.TextToPtr(a.AvatarUrl),
		"runtime_mode":         a.RuntimeMode,
		"runtime_config":       rc,
		"visibility":           a.Visibility,
		"status":               a.Status,
		"max_concurrent_tasks": a.MaxConcurrentTasks,
		"owner_id":             util.UUIDToPtr(a.OwnerID),
		"created_at":           util.TimestampToString(a.CreatedAt),
		"updated_at":           util.TimestampToString(a.UpdatedAt),
		"archived_at":          util.TimestampToPtr(a.ArchivedAt),
		"archived_by":          util.UUIDToPtr(a.ArchivedBy),
	}
}
