package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This file is the "one alignment becomes a SET of issues" half of the draft
// protocol. The protocol itself — drafts, locks, statuses, the three-step
// finalize — is in issue_draft.go; what lives here is the identity model and
// the group commit.
//
// The model, in one line: every alignment NODE (the root, or one child) owns at
// most one issue, and which issue that is is derived from the conversation, not
// from anything the client sends. The contract is
// docs/design/issue-draft-group-finalize.md.

const issueDraftOriginType = "issue_draft"

// Bounds on one payload's children. Both are payload checks, deliberately made
// before the group transaction opens: a duplicate key would otherwise be a
// unique-index violation reported as a 500, and an oversized group would be
// discovered only after it had already done work.
const (
	maxIssueDraftChildKeyBytes = 64
	maxIssueDraftChildStage    = 20
)

// issueDraftNodeNamespace is the fixed namespace node ids are derived under.
// Never change it: it is half the input of every node id already minted, so
// changing it would strip every existing group of its identity and make the
// next confirm build the whole group a second time.
var issueDraftNodeNamespace = uuid.MustParse("076522a7-f3b6-414f-afb0-41823470299e")

// issueDraftNodeID resolves an alignment node to the origin_id of the issue it
// owns.
//
// The root node (key == "") is the chat session id itself. That single line is
// the load-bearing wall of the whole model:
//
//   - it keeps migration 486's partial unique index meaningful without editing
//     a character of it. The index now reads "at most one issue per alignment
//     NODE", and every row that already exists satisfies that, because a lone
//     issue is a group with only a root;
//   - it keeps GetIssueByOrigin(chat_session_id) pointing at exactly this
//     group's parent, which is what issue_draft.issue_id means;
//   - it keeps identity out of the client's hands. A client may invent any
//     child keys it likes, but the root's id comes from the session, so
//     "re-key everything and confirm again" collides on the root insert and
//     rolls the entire second group back.
//
// Children are UUIDv5 over (chat_session_id, key), so a client cannot construct
// a node id that points at another draft's node. The separator is NUL so
// ("a","bc") and ("ab","c") cannot hash to the same digest.
func issueDraftNodeID(sessionID pgtype.UUID, key string) pgtype.UUID {
	if key == "" {
		return sessionID
	}
	derived := uuid.NewSHA1(issueDraftNodeNamespace, []byte(uuidToString(sessionID)+"\x00"+key))
	return parseUUID(derived.String())
}

// issueDraftChildrenFromPayload validates the sub-issue array against the
// payload contract and returns it in payload order. The keys it returns are
// trimmed: validation, uniqueness and id derivation all have to agree on what a
// key IS, or " c1" and "c1" would be two different nodes that a reader sees as
// one.
func issueDraftChildrenFromPayload(w http.ResponseWriter, payload issueDraftPayload) ([]issueDraftChild, bool) {
	if len(payload.Children) > service.MaxIssueGroupChildren {
		writeError(w, http.StatusBadRequest, "draft has too many sub-issues")
		return nil, false
	}
	children := make([]issueDraftChild, 0, len(payload.Children))
	seen := make(map[string]struct{}, len(payload.Children))
	for _, child := range payload.Children {
		child.Key = strings.TrimSpace(child.Key)
		if child.Key == "" || len(child.Key) > maxIssueDraftChildKeyBytes {
			writeError(w, http.StatusBadRequest, "sub-issue key is required")
			return nil, false
		}
		if _, duplicate := seen[child.Key]; duplicate {
			writeError(w, http.StatusBadRequest, "duplicate sub-issue key")
			return nil, false
		}
		seen[child.Key] = struct{}{}
		if child.Stage != nil && (*child.Stage < 1 || *child.Stage > maxIssueDraftChildStage) {
			writeError(w, http.StatusBadRequest, "invalid sub-issue stage")
			return nil, false
		}
		children = append(children, child)
	}
	return children, true
}

// issueDraftNode is one node of the payload after the array bounds have been
// checked: the root (Key == "") plus every child. Both go through the same
// validation below, so a sub-issue cannot be created under rules the root is
// not held to.
type issueDraftNode struct {
	Key          string
	Title        string
	Description  string
	Status       string
	Priority     string
	AssigneeType *string
	AssigneeID   *string
	// ProjectID and ParentIssueID belong to the root alone: the payload's eight
	// flat fields describe the parent, and a child inherits its project from
	// that parent inside the create transaction.
	ProjectID     *string
	ParentIssueID *string
	Stage         pgtype.Int4
}

// issueParamsFromDraft validates one alignment node and resolves it into create
// parameters. Every gate the ordinary create path applies to a client-supplied
// field applies here too — the draft is client-supplied, and an alignment
// conversation must not become a way to assign work to an agent the caller
// cannot invoke, or to name a parent in another workspace.
func (h *Handler) issueParamsFromDraft(w http.ResponseWriter, r *http.Request, workspaceID string, session db.ChatSession, node issueDraftNode) (service.IssueCreateParams, bool) {
	title := strings.TrimSpace(node.Title)
	if title == "" {
		writeError(w, http.StatusBadRequest, "draft title is required")
		return service.IssueCreateParams{}, false
	}

	status := node.Status
	if status == "" {
		status = "todo"
	}
	status, ok := h.resolveIssueStatusKey(w, r, session.WorkspaceID, status)
	if !ok {
		return service.IssueCreateParams{}, false
	}
	priority := node.Priority
	if priority == "" {
		priority = "none"
	}
	if !validateIssueEnum(w, "priority", priority, validIssuePriorities) {
		return service.IssueCreateParams{}, false
	}

	var assigneeType pgtype.Text
	var assigneeID pgtype.UUID
	if node.AssigneeType != nil {
		assigneeType = pgtype.Text{String: *node.AssigneeType, Valid: true}
	}
	if node.AssigneeID != nil {
		id, ok := parseUUIDOrBadRequest(w, *node.AssigneeID, "assignee_id")
		if !ok {
			return service.IssueCreateParams{}, false
		}
		assigneeID = id
	}
	if code, msg := h.validateAssigneePair(r.Context(), r, workspaceID, assigneeType, assigneeID); code != 0 {
		writeError(w, code, msg)
		return service.IssueCreateParams{}, false
	}

	var projectID pgtype.UUID
	if node.ProjectID != nil && *node.ProjectID != "" {
		id, ok := parseUUIDOrBadRequest(w, *node.ProjectID, "project_id")
		if !ok {
			return service.IssueCreateParams{}, false
		}
		projectID = id
	}
	var parentIssueID pgtype.UUID
	if node.ParentIssueID != nil && *node.ParentIssueID != "" {
		id, ok := parseUUIDOrBadRequest(w, *node.ParentIssueID, "parent_issue_id")
		if !ok {
			return service.IssueCreateParams{}, false
		}
		// Project membership and the parent's workspace boundary are re-checked
		// inside the create transaction atomically with the create; this read only
		// turns a cross-workspace parent into a 400 naming the field.
		parent, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID:          id,
			WorkspaceID: session.WorkspaceID,
		})
		if err != nil || !parent.ID.Valid {
			writeError(w, http.StatusBadRequest, "parent issue not found in this workspace")
			return service.IssueCreateParams{}, false
		}
		parentIssueID = id
	}

	return service.IssueCreateParams{
		WorkspaceID:   session.WorkspaceID,
		Title:         title,
		Description:   pgtype.Text{String: node.Description, Valid: node.Description != ""},
		Status:        status,
		Priority:      priority,
		AssigneeType:  assigneeType,
		AssigneeID:    assigneeID,
		CreatorType:   "member",
		CreatorID:     session.CreatorID,
		ParentIssueID: parentIssueID,
		ProjectID:     projectID,
		Stage:         node.Stage,
		// The draft's conversation IS the issue's provenance: it is how the
		// created issue points back at what was agreed, and how a crashed
		// confirm finds its own result on the next attempt. The node id — not
		// the session alone — is what makes that true per issue rather than per
		// conversation (see issueDraftNodeID).
		OriginType: pgtype.Text{String: issueDraftOriginType, Valid: true},
		OriginID:   issueDraftNodeID(session.ID, node.Key),
		// An alignment draft is confirmed deliberately, by a human who has just
		// read it. The duplicate guard's "did you mean this existing issue"
		// prompt belongs to the quick-create path, not here.
		AllowDuplicate: true,
	}, true
}

// issueGroupParamsFromDraft turns a ready draft into the whole set of create
// parameters the confirm will commit: the root first, then its children in
// payload order.
//
// Everything that needs the database happens here, before the group transaction
// opens: permission checks (validateAssigneePair reads members, agents and
// squads), status resolution, and the parent lookup. The group transaction is
// what a concurrent confirm blocks on, so it must contain nothing but inserts
// and the row locks those inserts need (§4.3).
func (h *Handler) issueGroupParamsFromDraft(w http.ResponseWriter, r *http.Request, workspaceID string, session db.ChatSession, draft db.IssueDraft) (service.IssueGroupParams, bool) {
	var payload issueDraftPayload
	if err := json.Unmarshal(draft.Draft, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "draft is not a valid issue draft")
		return service.IssueGroupParams{}, false
	}
	children, ok := issueDraftChildrenFromPayload(w, payload)
	if !ok {
		return service.IssueGroupParams{}, false
	}

	nodes := make([]issueDraftNode, 0, len(children)+1)
	nodes = append(nodes, issueDraftNode{
		// The root's key is empty by definition. issueDraftNodeID maps that to
		// the chat session id, which is the id every draft row already
		// produced before groups existed.
		Title:         payload.Title,
		Description:   payload.Description,
		Status:        payload.Status,
		Priority:      payload.Priority,
		AssigneeType:  payload.AssigneeType,
		AssigneeID:    payload.AssigneeID,
		ProjectID:     payload.ProjectID,
		ParentIssueID: payload.ParentIssueID,
	})
	for _, child := range children {
		nodes = append(nodes, issueDraftNode{
			Key:          child.Key,
			Title:        child.Title,
			Description:  child.Description,
			Status:       child.Status,
			Priority:     child.Priority,
			AssigneeType: child.AssigneeType,
			AssigneeID:   child.AssigneeID,
			Stage:        int4FromPtr(child.Stage),
		})
	}

	group := service.IssueGroupParams{Nodes: make([]service.IssueGroupNode, 0, len(nodes))}
	for _, node := range nodes {
		params, ok := h.issueParamsFromDraft(w, r, workspaceID, session, node)
		if !ok {
			return service.IssueGroupParams{}, false
		}
		group.Nodes = append(group.Nodes, service.IssueGroupNode{Params: params})
	}
	return group, true
}

// lookupIssueGroupRoot finds the group this alignment already produced, by the
// one id that is stable across confirms: the root's origin_id, which is the
// chat session id. Migration 486's partial unique index makes "at most one" a
// database fact, so this is exact rather than best-effort.
func (h *Handler) lookupIssueGroupRoot(r *http.Request, session db.ChatSession, rootOrigin pgtype.UUID) (db.Issue, error) {
	return h.Queries.GetIssueByOrigin(r.Context(), db.GetIssueByOriginParams{
		WorkspaceID: session.WorkspaceID,
		OriginType:  pgtype.Text{String: issueDraftOriginType, Valid: true},
		OriginID:    rootOrigin,
	})
}

// loadIssueGroup reads a committed group back: the root plus every child of its
// parent. ListChildIssues orders by number ASC, and the numbers were allocated
// in payload order, so what a reader gets back is the order the alignment
// settled on. Nothing is filtered — a child somebody added by hand under the
// root still belongs to the answer to "what does this group look like now".
//
// That query has no workspace predicate, which is safe only because the root is
// always a row that came through a workspace-scoped lookup (lookupIssueGroupRoot
// above, or the draft's own issue_id). Never call it with a parent id taken
// straight out of a request body.
func (h *Handler) loadIssueGroup(w http.ResponseWriter, r *http.Request, root db.Issue) ([]db.Issue, bool) {
	children, err := h.Queries.ListChildIssues(r.Context(), root.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load issues for draft")
		return nil, false
	}
	group := make([]db.Issue, 0, len(children)+1)
	group = append(group, root)
	return append(group, children...), true
}

// createIssueGroupForDraft commits the whole group, or adopts the one another
// confirm already committed.
//
// Two different mechanisms can hand back an existing group and both are needed:
//
//   - the origin lookup catches the ordinary retry — a double click, a lost
//     response, a confirm whose process died after the commit but before the
//     draft was pointed at the group;
//   - the unique violation on the root insert catches the genuine race, where
//     two confirms were both admitted before either created anything. The
//     loser's children die with its root because the whole group shares one
//     transaction, so a second group cannot exist.
//
// The partial unique index on issue (origin_id) WHERE origin_type =
// 'issue_draft' is the authority for both. See §3.3 of the design.
func (h *Handler) createIssueGroupForDraft(w http.ResponseWriter, r *http.Request, session db.ChatSession, group service.IssueGroupParams) ([]db.Issue, bool) {
	rootOrigin := group.Nodes[0].Params.OriginID

	existing, err := h.lookupIssueGroupRoot(r, session, rootOrigin)
	if err == nil {
		return h.loadIssueGroup(w, r, existing)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to look up issue for draft")
		return nil, false
	}

	// One filler for the whole group: it shares a single status-catalog
	// Resolver, so broadcasting N issues costs one catalog read rather than N.
	prefix := h.getIssuePrefix(r.Context(), session.WorkspaceID)
	fillCreated := h.newStatusCategoryFiller(r.Context(), session.WorkspaceID)
	platform, _, _ := middleware.ClientMetadataFromContext(r.Context())
	actorID := uuidToString(session.CreatorID)
	for i := range group.Nodes {
		params := group.Nodes[i].Params
		analyticsAgentID := ""
		if params.AssigneeType.Valid && params.AssigneeType.String == "agent" {
			analyticsAgentID = uuidToString(params.AssigneeID)
		}
		group.Nodes[i].Opts = service.IssueCreateOpts{
			ActorID:          actorID,
			AnalyticsAgentID: analyticsAgentID,
			Platform:         platform,
			BroadcastPayload: func(issue db.Issue, _ []db.Attachment, labels []db.IssueLabel) map[string]any {
				payload := issueToResponse(issue, prefix)
				fillCreated(&payload)
				labelResponses := labelsToResponse(labels)
				payload.Labels = &labelResponses
				return map[string]any{"issue": payload}
			},
		}
	}

	result, err := h.IssueService.CreateGroup(r.Context(), group)
	if err == nil {
		return result.Issues, true
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		if won, lookupErr := h.lookupIssueGroupRoot(r, session, rootOrigin); lookupErr == nil {
			return h.loadIssueGroup(w, r, won)
		}
	}
	writeIssueDraftCreateError(w, r, err)
	return nil, false
}

// IssueDraftCreatedIssue is one row of a confirmed group on the wire: enough
// for a confirmation page to render it as a line without a second request.
//
// Fields are omitted rather than sent as null. A client that does not know the
// field treats an absent one exactly as it treats a null one, and omitting
// keeps the ordinary single-issue confirm (the root, unassigned, unstaged)
// byte-shaped like it was before groups existed.
type IssueDraftCreatedIssue struct {
	ID            string  `json:"id"`
	Identifier    string  `json:"identifier"`
	Title         string  `json:"title"`
	Status        string  `json:"status"`
	Stage         *int32  `json:"stage,omitempty"`
	AssigneeType  *string `json:"assignee_type,omitempty"`
	AssigneeID    *string `json:"assignee_id,omitempty"`
	ParentIssueID *string `json:"parent_issue_id,omitempty"`
}

// issueDraftCreatedIssues maps a committed group to the response rows, root
// first. The identifier is built the way every other issue response builds it:
// a confirmation page shows people numbers, not UUIDs.
func issueDraftCreatedIssues(issues []db.Issue, issuePrefix string) []IssueDraftCreatedIssue {
	out := make([]IssueDraftCreatedIssue, 0, len(issues))
	for _, issue := range issues {
		out = append(out, IssueDraftCreatedIssue{
			ID:            uuidToString(issue.ID),
			Identifier:    issueIdentifier(issuePrefix, issue.Number),
			Title:         issue.Title,
			Status:        issue.Status,
			Stage:         int4ToPtr(issue.Stage),
			AssigneeType:  textToPtr(issue.AssigneeType),
			AssigneeID:    uuidToPtr(issue.AssigneeID),
			ParentIssueID: uuidToPtr(issue.ParentIssueID),
		})
	}
	return out
}

// issueDraftGroupResponse answers "what did this alignment produce" for a
// confirm that arrives after the draft is already completed. It reads the root
// by origin rather than through the draft's issue_id column so that a draft
// whose column is stale still answers with the group that actually exists.
func (h *Handler) issueDraftGroupResponse(w http.ResponseWriter, r *http.Request, session db.ChatSession) ([]IssueDraftCreatedIssue, bool) {
	root, err := h.lookupIssueGroupRoot(r, session, issueDraftNodeID(session.ID, ""))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// A completed draft with no group behind it is not something to
			// paper over: the confirm would answer 200 naming an issue that
			// does not exist.
			writeError(w, http.StatusInternalServerError, "completed draft has no issue")
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load issues for draft")
		return nil, false
	}
	group, ok := h.loadIssueGroup(w, r, root)
	if !ok {
		return nil, false
	}
	return issueDraftCreatedIssues(group, h.getIssuePrefix(r.Context(), session.WorkspaceID)), true
}

// int4FromPtr converts an optional payload integer into the nullable column
// type the create params carry.
func int4FromPtr(v *int32) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *v, Valid: true}
}
