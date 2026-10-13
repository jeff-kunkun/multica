package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issueguard"
	"github.com/multica-ai/multica/server/internal/issueposition"
	"github.com/multica-ai/multica/server/internal/issueproperty"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// IssueService is the single service-layer entry point for creating issues.
// The HTTP `POST /issues` handler and other create entries call into Create so that duplicate guard, issue numbering, attachment
// linking, broadcast, analytics, and agent enqueue stay aligned. The
// service deliberately does NOT depend on http.Request — callers parse
// their own transport and pass a fully-resolved IssueCreateParams.
type IssueService struct {
	Queries   *db.Queries
	TxStarter TxStarter
	Bus       *events.Bus
	Analytics analytics.Client
	// Metrics is the shared business-metrics collector. Wired by
	// cmd/server/router.go after construction; nil in tests / self-hosted
	// without the metrics listener — obsmetrics.RecordEvent treats a nil
	// Metrics as "PostHog only", so leaving it unset is safe.
	Metrics     *obsmetrics.BusinessMetrics
	TaskService *TaskService
}

func NewIssueService(q *db.Queries, tx TxStarter, bus *events.Bus, ac analytics.Client, ts *TaskService) *IssueService {
	return &IssueService{
		Queries:     q,
		TxStarter:   tx,
		Bus:         bus,
		Analytics:   ac,
		TaskService: ts,
	}
}

// IssueCreateParams carries the already-validated, already-resolved inputs
// to IssueService.Create. The handler owns the parsing step that turns its
// request payload into this struct; the service stays transport-agnostic.
type IssueCreateParams struct {
	WorkspaceID   pgtype.UUID
	Title         string
	Description   pgtype.Text
	Status        string
	Priority      string
	AssigneeType  pgtype.Text
	AssigneeID    pgtype.UUID
	CreatorType   string // "agent" or "member"
	CreatorID     pgtype.UUID
	ParentIssueID pgtype.UUID
	ProjectID     pgtype.UUID
	StartDate     pgtype.Date
	DueDate       pgtype.Date
	OriginType    pgtype.Text
	OriginID      pgtype.UUID
	AttachmentIDs []pgtype.UUID
	// LabelIDs are the issue-scoped labels to attach to the new issue. They
	// are validated and written inside the create transaction (see Create),
	// so the issue is never committed with a partial or wrong label set. An
	// unknown or non-issue label id fails the whole create with
	// ErrIssueLabelNotFound rather than being silently dropped.
	LabelIDs []pgtype.UUID
	// Properties is keyed only by property-definition UUID. Create validates
	// and canonicalizes every value while holding the same per-definition locks
	// used by the standalone PUT path, then inserts the complete bag atomically.
	Properties     map[pgtype.UUID]json.RawMessage
	AllowDuplicate bool
	// Stage groups this issue into an ordered barrier group under its parent
	// (NULL = unstaged). See issue_wakeup_system.go for the staged-barrier wake.
	Stage pgtype.Int4
	// SourceContext is set only by the comment-scoped manual create endpoint.
	// Its immutable snapshot and cloned attachment rows commit in the same
	// transaction as the new issue.
	SourceContext *SourceContextCapture
}

// IssueCreateOpts groups optional knobs for IssueService.Create. Most
// callers leave it zero-valued.
type IssueCreateOpts struct {
	// BroadcastPayload, if non-nil, is invoked after the issue row is
	// created and attachments are linked. Its return value is sent as
	// the EventIssueCreated payload via the event bus. The HTTP handler
	// uses this hook to inject its IssueResponse without forcing this
	// package to depend on handler-layer types. If nil, the service
	// emits a minimal `{"issue_id": <uuid>}` payload — enough for cache
	// invalidation, but front-ends that expect the full response shape
	// must provide BroadcastPayload. The labels argument is the authoritative
	// snapshot attached in the create transaction, so the emitted payload can
	// carry the new issue's labels and every online client renders it already
	// labeled instead of blank until a refetch.
	BroadcastPayload func(issue db.Issue, attachments []db.Attachment, labels []db.IssueLabel) map[string]any

	// ActorID overrides the actor ID used for broadcast + analytics
	// when it differs from the creator on the row. Agent-created issues
	// use the agent UUID here (the creator_id column is the daemon
	// owner). Empty falls back to CreatorID.
	ActorID string

	// AnalyticsAgentID is the agent associated with the issue for
	// analytics purposes (assignee agent or, for agent-created issues,
	// the creator agent). Resolved by the caller because it depends on
	// transport context.
	AnalyticsAgentID string

	// Platform tags the IssueCreated analytics + business-metrics event
	// with the client surface the request came in on (web / desktop /
	// daemon). Derived from middleware's client
	// metadata at the handler layer.
	Platform string
}

// ErrActiveDuplicate signals that the duplicate guard found an active
// issue with the same (workspace, project, parent, title) tuple and
// AllowDuplicate was false. The IssueCreateResult.DuplicateIssue field is
// populated when this error is returned so callers can render the
// conflict (HTTP 409, etc.).
var ErrActiveDuplicate = errors.New("active duplicate issue exists")

// ErrParentIssueNotFound signals that the supplied ParentIssueID does
// not exist in the issue's workspace. The service refuses to create
// orphaned or cross-workspace child issues; callers translate this into
// their transport's 400.
var ErrParentIssueNotFound = errors.New("parent issue not found in this workspace")

// ErrProjectNotFound signals that the supplied ProjectID does not exist
// in the issue's workspace. Cross-workspace project IDs are rejected
// here so every create entry (HTTP `POST /issues`, future
// MCP / API key callers) enforces the same workspace boundary without
// having to remember it. Callers translate this into 400.
var ErrProjectNotFound = errors.New("project not found in this workspace")

// ErrIssueLabelNotFound signals that one of the supplied LabelIDs does not
// exist in the issue's workspace or is not an issue-scoped label. The whole
// create is rejected so a new issue is never born with a partial or wrong
// label set. Callers translate this into their transport's 400.
var ErrIssueLabelNotFound = errors.New("issue label not found in this workspace")

// ErrIssuePropertiesTooLarge is the existing row-level 16KB properties-bag
// constraint surfaced as a client error. The same database constraint applies
// to standalone PUT and atomic create writes.
var ErrIssuePropertiesTooLarge = errors.New("issue properties exceed the 16KB size limit")

// IssuePropertyValidationError identifies the definition whose value made an
// atomic create invalid. Unknown and foreign-workspace IDs intentionally share
// the same message so the API does not expose tenant existence.
type IssuePropertyValidationError struct {
	PropertyID string
	Message    string
}

func (e *IssuePropertyValidationError) Error() string { return e.Message }

// ErrIssueStatusUnavailable signals that the requested custom status was
// archived between the caller's pre-flight validation and the create
// transaction. Callers translate this into a 409 — the request was valid when
// it arrived, so retrying against the refreshed catalog is the remedy.
var ErrIssueStatusUnavailable = errors.New("issue status is no longer available")

var ErrSourceContextAlreadyAttached = errors.New("source context is already attached")

// IssueCreateResult is the typed return from IssueService.Create.
//
//   - On the happy path: Issue is the new row, Attachments lists the
//     linked attachments (may be empty), DuplicateIssue is nil.
//   - On ErrActiveDuplicate: DuplicateIssue is the row that blocked the
//     create; Issue and Attachments are zero.
type IssueCreateResult struct {
	Issue       db.Issue
	Attachments []db.Attachment
	// AssignedTaskID is populated when Create enqueues the automatic task for
	// an agent assignee.
	AssignedTaskID pgtype.UUID
	// Labels is the authoritative set of labels attached to the new issue in
	// the create transaction (empty when none were requested). Callers echo it
	// on the create response + issue:created event so every client renders the
	// new issue already labeled and a new client can detect that the backend
	// understood label_ids (see the create handler's compatibility contract).
	Labels         []db.IssueLabel
	DuplicateIssue *db.Issue
}

// Create runs the full issue-creation pipeline atomically end-to-end:
//
//  1. Begin transaction.
//  2. Resolve & validate parent / project belong to the same workspace.
//  3. Lock & check the duplicate guard.
//  4. Increment the workspace issue counter.
//  5. Insert the issue row (with optional origin stamping).
//  6. Commit.
//  7. Link any pre-uploaded attachments (post-commit, idempotent).
//  8. Publish EventIssueCreated to the bus (payload via opts.BroadcastPayload).
//  9. Capture the IssueCreated analytics event.
//  10. Enqueue the agent task when the issue is assigned and not in
//     `backlog`.
//
// Validation that lives in the service (parent existence, project
// workspace membership, parent → project back-fill) is enforced here so
// every create entry — HTTP `POST /issues`, future
// MCP/API-key callers — shares the same workspace boundary semantics.
// Caller-owned validation is limited to transport-shaped checks: title
// required, RFC3339 date format, assignee pair sanity.
func (s *IssueService) Create(ctx context.Context, p IssueCreateParams, opts IssueCreateOpts) (IssueCreateResult, error) {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)

	properties, err := validateIssueCreateProperties(ctx, tx, qtx, p.WorkspaceID, p.Properties)
	if err != nil {
		return IssueCreateResult{}, err
	}

	if p.SourceContext != nil {
		if _, err := qtx.LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{
			ID: p.SourceContext.SourceIssueID, WorkspaceID: p.WorkspaceID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return IssueCreateResult{}, ErrSourceIssueDeleted
			}
			return IssueCreateResult{}, fmt.Errorf("lock source issue: %w", err)
		}
		locked, err := qtx.LockCommentAncestorPath(ctx, db.LockCommentAncestorPathParams{
			CommentID: p.SourceContext.AnchorCommentID, WorkspaceID: p.WorkspaceID,
			IssueID: p.SourceContext.SourceIssueID,
		})
		if err != nil {
			return IssueCreateResult{}, fmt.Errorf("lock anchor comment thread: %w", err)
		}
		if len(locked) == 0 {
			return IssueCreateResult{}, ErrAnchorCommentDeleted
		}
		current, err := BuildSourceContext(ctx, qtx, p.WorkspaceID, p.SourceContext.AnchorCommentID)
		if err != nil {
			return IssueCreateResult{}, err
		}
		if current.Digest != p.SourceContext.Digest {
			return IssueCreateResult{}, ErrSourceContextChanged
		}
	}

	// A create landing on a CUSTOM status takes the shared catalog lock AND
	// re-resolves the status inside this transaction. The caller validated the
	// status before the transaction opened, which is early enough to return a
	// clean 400 but too early to be safe: an archive can commit in between.
	// Re-checking under the lock is what makes the status provably active at
	// the moment the row is written. Built-in statuses skip both — they can
	// never be archived, so the common path is unchanged. (MUL-6243)
	if !issuestatus.IsBuiltIn(p.Status) {
		if err := qtx.LockIssueStatusCatalogShared(ctx, p.WorkspaceID); err != nil {
			return IssueCreateResult{}, err
		}
		if _, err := issuestatus.Resolve(ctx, qtx, p.WorkspaceID, p.Status); err != nil {
			if errors.Is(err, issuestatus.ErrUnknownStatus) {
				return IssueCreateResult{}, ErrIssueStatusUnavailable
			}
			return IssueCreateResult{}, err
		}
	}

	// Resolve and validate parent / project before reading from the
	// duplicate guard so a forged parent or project ID is rejected
	// before we touch the issue counter. Both checks scope by
	// WorkspaceID — there is no path from this service to a row in a
	// foreign workspace.
	projectID := p.ProjectID
	if p.ParentIssueID.Valid {
		parent, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID:          p.ParentIssueID,
			WorkspaceID: p.WorkspaceID,
		})
		if err != nil || !parent.ID.Valid {
			return IssueCreateResult{}, ErrParentIssueNotFound
		}
		// Back-fill project from parent when the caller did not pin
		// one explicitly. Matches the long-standing HTTP behavior: a
		// sub-issue inherits its parent's project unless overridden.
		if !projectID.Valid {
			projectID = parent.ProjectID
		}
	}
	if projectID.Valid {
		if _, err := qtx.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID:          projectID,
			WorkspaceID: p.WorkspaceID,
		}); err != nil {
			return IssueCreateResult{}, ErrProjectNotFound
		}
	}

	// Validate labels before we increment the issue counter so a stale or
	// wrong-scope selection fails the create cheaply. The de-duplicated rows
	// are attached to the issue below, inside this same transaction, and
	// echoed back as the authoritative snapshot in the result.
	labels, err := validateIssueLabels(ctx, qtx, p.WorkspaceID, p.LabelIDs)
	if err != nil {
		return IssueCreateResult{}, err
	}

	duplicate, found, err := issueguard.LockAndFindActiveDuplicate(ctx, qtx, p.WorkspaceID, projectID, p.ParentIssueID, p.Title, p.AllowDuplicate)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("duplicate guard: %w", err)
	}
	if found {
		dup := duplicate
		return IssueCreateResult{DuplicateIssue: &dup}, ErrActiveDuplicate
	}

	issueNumber, err := qtx.IncrementIssueCounter(ctx, p.WorkspaceID)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("allocate issue number: %w", err)
	}

	// New issues sort to the top of their (workspace, status) column for
	// manual ordering. Computed inside the tx, after IncrementIssueCounter
	// has already taken the workspace row lock, so two concurrent creates
	// in the same workspace see each other's positions and don't both
	// land on the same min-1 slot. Concurrent manual reorder via
	// UpdateIssue(position) does NOT take this lock, so a create racing
	// a reorder is still allowed to collide on position — manual ordering
	// is best-effort and the UI tolerates equal positions by falling back
	// to the secondary ORDER BY key.
	newPosition, err := issueposition.NextTopPosition(ctx, tx, p.WorkspaceID, p.Status)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("next top position: %w", err)
	}

	var issue db.Issue
	if p.OriginType.Valid {
		issue, err = qtx.CreateIssueWithOrigin(ctx, db.CreateIssueWithOriginParams{
			ID:            dbid.NewV7(),
			WorkspaceID:   p.WorkspaceID,
			Title:         p.Title,
			Description:   p.Description,
			Status:        p.Status,
			Priority:      p.Priority,
			AssigneeType:  p.AssigneeType,
			AssigneeID:    p.AssigneeID,
			CreatorType:   p.CreatorType,
			CreatorID:     p.CreatorID,
			ParentIssueID: p.ParentIssueID,
			Position:      newPosition,
			StartDate:     p.StartDate,
			DueDate:       p.DueDate,
			Number:        issueNumber,
			ProjectID:     projectID,
			OriginType:    p.OriginType,
			OriginID:      p.OriginID,
			Stage:         p.Stage,
			Properties:    properties,
		})
	} else {
		issue, err = qtx.CreateIssue(ctx, db.CreateIssueParams{
			ID:            dbid.NewV7(),
			WorkspaceID:   p.WorkspaceID,
			Title:         p.Title,
			Description:   p.Description,
			Status:        p.Status,
			Priority:      p.Priority,
			AssigneeType:  p.AssigneeType,
			AssigneeID:    p.AssigneeID,
			CreatorType:   p.CreatorType,
			CreatorID:     p.CreatorID,
			ParentIssueID: p.ParentIssueID,
			Position:      newPosition,
			StartDate:     p.StartDate,
			DueDate:       p.DueDate,
			Number:        issueNumber,
			ProjectID:     projectID,
			Stage:         p.Stage,
			Properties:    properties,
		})
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "issue_properties_size_limit" {
			return IssueCreateResult{}, ErrIssuePropertiesTooLarge
		}
		return IssueCreateResult{}, fmt.Errorf("create issue: %w", err)
	}

	if p.SourceContext != nil {
		if _, err := PersistSourceContext(ctx, qtx, *p.SourceContext, issue.ID, pgtype.UUID{}); err != nil {
			return IssueCreateResult{}, fmt.Errorf("persist source context: %w", err)
		}
	}

	// Attach labels inside the create transaction so the issue and its
	// labels commit together — the old flow created the issue first and
	// attached labels in a second, non-atomic round-trip whose partial
	// failure left the issue mis-categorized. AttachLabelToIssue is
	// workspace/resource_type-guarded and ON CONFLICT DO NOTHING, and the
	// ids were already validated above.
	for _, label := range labels {
		if err := qtx.AttachLabelToIssueOnCreate(ctx, db.AttachLabelToIssueOnCreateParams{
			IssueID:     issue.ID,
			LabelID:     label.ID,
			WorkspaceID: p.WorkspaceID,
		}); err != nil {
			return IssueCreateResult{}, fmt.Errorf("attach issue label: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return IssueCreateResult{}, fmt.Errorf("commit: %w", err)
	}

	attachments := s.linkAttachments(ctx, issue, p.AttachmentIDs)

	actorID := opts.ActorID
	if actorID == "" {
		actorID = util.UUIDToString(issue.CreatorID)
	}

	s.publishIssueCreated(issue, attachments, labels, p.CreatorType, actorID, opts)
	s.captureCreatedAnalytics(issue, p.CreatorType, actorID, opts)
	assignedTaskID := s.maybeEnqueueOnAssign(ctx, issue, p.CreatorType, actorID)

	return IssueCreateResult{Issue: issue, Attachments: attachments, Labels: labels, AssignedTaskID: assignedTaskID}, nil
}

// validateIssueCreateProperties returns the canonical JSONB bag to put on the
// issue row. Locks are acquired for every definition in UUID order before any
// definition is read, matching definition updates and preventing two
// multi-property creates from choosing opposite lock orders.
func validateIssueCreateProperties(ctx context.Context, tx pgx.Tx, qtx *db.Queries, workspaceID pgtype.UUID, values map[pgtype.UUID]json.RawMessage) ([]byte, error) {
	if len(values) == 0 {
		return []byte(`{}`), nil
	}

	ids := make([]pgtype.UUID, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool {
		return util.UUIDToString(ids[left]) < util.UUIDToString(ids[right])
	})
	for _, id := range ids {
		key := "prop:" + util.UUIDToString(id)
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", key); err != nil {
			return nil, fmt.Errorf("lock issue property: %w", err)
		}
	}

	canonical := make(map[string]json.RawMessage, len(ids))
	for _, id := range ids {
		propertyID := util.UUIDToString(id)
		definition, err := qtx.GetIssueProperty(ctx, db.GetIssuePropertyParams{
			ID: id, WorkspaceID: workspaceID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, &IssuePropertyValidationError{PropertyID: propertyID, Message: "property not found in this workspace"}
			}
			return nil, fmt.Errorf("get issue property: %w", err)
		}
		if definition.ArchivedAt.Valid {
			return nil, &IssuePropertyValidationError{PropertyID: propertyID, Message: fmt.Sprintf("property %q is archived and cannot receive new values", definition.Name)}
		}
		value, err := issueproperty.ValidateValue(definition, values[id])
		if err != nil {
			return nil, &IssuePropertyValidationError{PropertyID: propertyID, Message: err.Error()}
		}
		if issueproperty.IsActor(definition.Type) {
			refs, err := issueproperty.ActorRefsInValue(definition.Type, value)
			if err != nil {
				return nil, &IssuePropertyValidationError{PropertyID: propertyID, Message: err.Error()}
			}
			for _, ref := range refs {
				actorID, parseErr := util.ParseUUID(ref.ID)
				if parseErr != nil {
					return nil, &IssuePropertyValidationError{PropertyID: propertyID, Message: fmt.Sprintf("actor id in %q must be a UUID", ref)}
				}
				if _, lookupErr := qtx.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
					UserID: actorID, WorkspaceID: workspaceID,
				}); lookupErr != nil {
					if errors.Is(lookupErr, pgx.ErrNoRows) {
						return nil, &IssuePropertyValidationError{PropertyID: propertyID, Message: fmt.Sprintf("%q does not refer to a member of this workspace", ref)}
					}
					return nil, fmt.Errorf("resolve issue property actor: %w", lookupErr)
				}
			}
		}
		canonical[propertyID] = value
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("encode issue properties: %w", err)
	}
	return encoded, nil
}

// validateIssueLabels checks that every requested label exists in the
// workspace and is issue-scoped, returning the de-duplicated label rows to
// attach. Returning the full rows (not just ids) lets Create echo an
// authoritative label snapshot on the create response + issue:created event
// without a second query. It mirrors the workspace + resource_type='issue'
// guard already enforced by AttachLabelToIssue so an unknown or wrong-scope id
// surfaces as ErrIssueLabelNotFound instead of a silent no-op insert. Invalid
// (zero) UUIDs are skipped. The label count per issue is small, so a GetLabel
// per distinct id is fine and avoids introducing a new batch query.
func validateIssueLabels(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, labelIDs []pgtype.UUID) ([]db.IssueLabel, error) {
	if len(labelIDs) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(labelIDs))
	deduped := make([]db.IssueLabel, 0, len(labelIDs))
	for _, labelID := range labelIDs {
		if !labelID.Valid {
			continue
		}
		key := util.UUIDToString(labelID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		label, err := qtx.GetLabel(ctx, db.GetLabelParams{
			ID:          labelID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrIssueLabelNotFound
			}
			return nil, fmt.Errorf("get issue label: %w", err)
		}
		if label.ResourceType != "issue" {
			return nil, ErrIssueLabelNotFound
		}
		deduped = append(deduped, label)
	}
	return deduped, nil
}

// linkAttachments links the given attachment IDs to the newly created
// issue and returns the re-fetched attachment rows so callers can build
// their response without a second query. Errors are logged and swallowed
// — attachment linking is a best-effort post-commit step, and a stale
// attachment row doesn't justify failing the whole create.
func (s *IssueService) linkAttachments(ctx context.Context, issue db.Issue, ids []pgtype.UUID) []db.Attachment {
	if len(ids) == 0 {
		return nil
	}
	if _, err := s.Queries.LinkAttachmentsToIssue(ctx, db.LinkAttachmentsToIssueParams{
		IssueID:       issue.ID,
		WorkspaceID:   issue.WorkspaceID,
		AttachmentIds: ids,
		BumpRevision:  false,
	}); err != nil {
		slog.Error("failed to link attachments to issue",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err)
		return nil
	}
	list, err := s.Queries.ListAttachmentsByIssue(ctx, db.ListAttachmentsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("failed to list attachments for new issue",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err)
		return nil
	}
	return list
}

func (s *IssueService) publishIssueCreated(issue db.Issue, attachments []db.Attachment, labels []db.IssueLabel, creatorType, actorID string, opts IssueCreateOpts) {
	if s.Bus == nil {
		return
	}
	var payload map[string]any
	if opts.BroadcastPayload != nil {
		payload = opts.BroadcastPayload(issue, attachments, labels)
	} else {
		// Minimal fallback so cache invalidations still fire even if the
		// caller forgot to supply a builder. Front-ends that expect the
		// full IssueResponse must pass BroadcastPayload.
		payload = map[string]any{"issue_id": util.UUIDToString(issue.ID)}
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventIssueCreated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   creatorType,
		ActorID:     actorID,
		Payload:     payload,
	})
}

func (s *IssueService) captureCreatedAnalytics(issue db.Issue, creatorType, actorID string, opts IssueCreateOpts) {
	if s.Analytics == nil {
		return
	}
	source, taskID := classifyOrigin(issue)
	analyticsActorID := actorID
	if creatorType == "agent" {
		analyticsActorID = "agent:" + actorID
	}
	obsmetrics.RecordEvent(s.Analytics, s.Metrics, analytics.IssueCreated(
		analyticsActorID,
		util.UUIDToString(issue.WorkspaceID),
		util.UUIDToString(issue.ID),
		opts.AnalyticsAgentID,
		taskID,
		source,
		opts.Platform,
	))
}

// classifyOrigin maps the issue's origin_type / origin_id columns into the
// analytics source labels. Unknown origin_type falls back to SourceManual
// with the warning logged — analytics drift is preferable to dropping the
// event entirely.
func classifyOrigin(issue db.Issue) (source, taskID string) {
	source = analytics.SourceManual
	if !issue.OriginType.Valid {
		return source, ""
	}
	originID := util.UUIDToString(issue.OriginID)
	switch issue.OriginType.String {
	case "quick_create", "agent_create":
		// Both link the issue back to the agent_task_queue row that created it
		// (agent_create is the ordinary agent `issue create` path, MUL-4305);
		// surface that task id and keep the manual source label.
		return analytics.SourceManual, originID
	default:
		slog.Warn("analytics: unknown issue origin type",
			"origin_type", issue.OriginType.String,
			"issue_id", util.UUIDToString(issue.ID),
		)
		return analytics.SourceManual, ""
	}
}

func (s *IssueService) maybeEnqueueOnAssign(ctx context.Context, issue db.Issue, creatorType, actorID string) pgtype.UUID {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return pgtype.UUID{}
	}
	// Backlog is the parking lot: nothing runs from it, so nothing here needs
	// explaining either. Custom unstarted statuses do not inherit parking.
	if issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, issue.Status) == "backlog" {
		return pgtype.UUID{}
	}
	verdict, admitted := agentAssigneeVerdict(ctx, s.runtimeLookup(s.Queries), issue)
	if !admitted && RuntimeBlockedNeedsNotice(verdict.Reason) {
		// Assignment has no response the assigner reads for this outcome, so the
		// refusal explains itself on the issue instead of vanishing (MUL-6164).
		// Only here, not in the create-with-assignee path above: that one runs
		// inside the issue's transaction, and a notice about a machine has no
		// business deciding whether the issue itself commits.
		s.noteRuntimeUnusable(ctx, issue, verdict)
	}
	if admitted {
		task, err := s.TaskService.EnqueueTaskForIssue(ctx, issue)
		if err != nil {
			slog.Warn("enqueue agent task on create failed",
				"issue_id", util.UUIDToString(issue.ID),
				"error", err)
		} else {
			return task.ID
		}
	}
	return pgtype.UUID{}
}

func isAgentAssigneeReadyWithQueries(ctx context.Context, lookup RuntimeLookup, issue db.Issue) bool {
	_, ok := agentAssigneeVerdict(ctx, lookup, issue)
	return ok
}

// agentAssigneeVerdict resolves the issue's agent assignee through the shared
// readiness check and reports whether work may be enqueued for it, plus the
// verdict when it may not.
//
// Only a BLOCKED verdict stops the enqueue. A merely offline machine still
// queues: that work runs when the laptop comes back, and people rely on it.
func agentAssigneeVerdict(ctx context.Context, lookup RuntimeLookup, issue db.Issue) (AgentVerdict, bool) {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		return AgentVerdict{}, false
	}
	agent, err := lookup.Queries.GetAgent(ctx, issue.AssigneeID)
	if err != nil {
		return AgentVerdict{}, false
	}
	verdict, err := AgentReadiness(ctx, lookup, agent)
	if err != nil {
		return AgentVerdict{}, false
	}
	return verdict, !verdict.Blocked()
}
