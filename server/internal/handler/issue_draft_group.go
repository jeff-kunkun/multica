package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
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
func (h *Handler) issueParamsFromDraft(w http.ResponseWriter, r *http.Request, workspaceID string, session db.ChatSession, node issueDraftNode, projectPinned bool) (service.IssueCreateParams, bool) {
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
		if _, ok := h.visibleProjectInWorkspace(w, r, session.WorkspaceID, id); !ok {
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
		ProjectPinned: projectPinned,
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

// issueDraftGroupState is what this alignment has already produced, read once
// per confirm.
//
// A confirm needs two things from it at the same time: which payload nodes
// already own an issue (a continuation round adds only the others) and what the
// whole group looks like now (that is the answer every confirm returns), and
// both come out of the same two reads. Reading them once is also what keeps
// validation and the commit from disagreeing about which nodes exist: the
// params pass skips exactly the nodes this state says are already issues.
type issueDraftGroupState struct {
	// HasRoot is false for an alignment that has never been confirmed, which is
	// the ordinary first confirm.
	HasRoot bool
	Root    db.Issue
	// Group is the whole committed group, root first. Only meaningful when
	// HasRoot is true.
	Group []db.Issue
}

// readIssueDraftGroupState reads the group this alignment already produced, if
// any. The root is found by origin — the one id that is stable across rounds —
// and the children by parent, which is exactly the read §3.4 of the design
// prescribes for both the adopt and the answer path.
func (h *Handler) readIssueDraftGroupState(w http.ResponseWriter, r *http.Request, session db.ChatSession) (issueDraftGroupState, bool) {
	root, err := h.lookupIssueGroupRoot(r, session, issueDraftNodeID(session.ID, ""))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return issueDraftGroupState{}, true
		}
		writeError(w, http.StatusInternalServerError, "failed to look up issue for draft")
		return issueDraftGroupState{}, false
	}
	group, ok := h.loadIssueGroup(w, r, root)
	if !ok {
		return issueDraftGroupState{}, false
	}
	return issueDraftGroupState{HasRoot: true, Root: root, Group: group}, true
}

// issueDraftOwnedNodes answers, for one payload's nodes, which of them already
// own an issue.
//
// It asks by origin rather than by reading the group's children, and that
// distinction is the whole point: origin_id is what migration 486's partial
// unique index is on, so it is the only answer the next INSERT will agree with.
// A read by parent disagrees the moment a node's issue leaves the group — moved
// under a sibling, re-parented onto another epic, detached to the top level —
// and a round that believed such a node was new would collide on its origin and
// take every genuinely new node in the same transaction down with it.
func (h *Handler) issueDraftOwnedNodes(w http.ResponseWriter, r *http.Request, session db.ChatSession, origins []pgtype.UUID) (map[string]struct{}, bool) {
	owned := make(map[string]struct{}, len(origins))
	if len(origins) == 0 {
		return owned, true
	}
	issues, err := h.Queries.ListIssuesByOrigins(r.Context(), db.ListIssuesByOriginsParams{
		WorkspaceID: session.WorkspaceID,
		OriginType:  pgtype.Text{String: issueDraftOriginType, Valid: true},
		OriginIds:   origins,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to look up issues for draft")
		return nil, false
	}
	for _, issue := range issues {
		if issue.OriginID.Valid {
			owned[uuidToString(issue.OriginID)] = struct{}{}
		}
	}
	return owned, true
}

// issueGroupParamsFromDraft turns a ready draft into the set of create
// parameters the confirm will commit: the root first, then its children in
// payload order.
//
// Everything that needs the database happens here, before the group transaction
// opens: permission checks (validateAssigneePair reads members, agents and
// squads), status resolution, and the parent lookup. The group transaction is
// what a concurrent confirm blocks on, so it must contain nothing but inserts
// and the row locks those inserts need (§4.3).
//
// A continuation round — a draft that has been confirmed once already and
// reopened (finalize_round > 0) on a group that exists — returns only the nodes
// that own no issue yet, with group.RootIssueID naming the root it appends to.
// Those nodes are also the only ones validated: an existing node's assignee may
// have been archived since it was created, and a round that merely adds work
// must not be refused over a node it is not touching. Its fields are never
// rewritten either — the group is real work by then, edited by people and
// agents, and a follow-up round is not grounds for overwriting that.
func (h *Handler) issueGroupParamsFromDraft(w http.ResponseWriter, r *http.Request, workspaceID string, session db.ChatSession, draft db.IssueDraft, state issueDraftGroupState) (service.IssueGroupParams, bool) {
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
	if !state.HasRoot || draft.FinalizeRound == 0 {
		// Not a continuation round. Either nothing exists yet and the whole
		// payload is built, or the group is already committed and the caller
		// adopts it whole — a first confirm that finds the root there is a
		// crash recovery or a re-keyed save racing it (§3.3 timelines B and C),
		// and appending there would turn "confirm again" into "create those
		// children too". Reopening is the only thing that makes new keys an
		// increment, and the only thing that moves the round counter.
		rootProjectPinned := draftJSONFieldPresent(draft.Draft, "project_id")
		for _, node := range nodes {
			// Only the root carries a project choice. Children leave the
			// field out so they take the root's project, including an
			// explicit empty one.
			pinned := node.Key == "" && rootProjectPinned
			params, ok := h.issueParamsFromDraft(w, r, workspaceID, session, node, pinned)
			if !ok {
				return service.IssueGroupParams{}, false
			}
			group.Nodes = append(group.Nodes, service.IssueGroupNode{Params: params})
		}
		return group, true
	}

	group.RootIssueID = state.Root.ID
	origins := make([]pgtype.UUID, 0, len(nodes))
	for _, node := range nodes {
		origins = append(origins, issueDraftNodeID(session.ID, node.Key))
	}
	owned, ok := h.issueDraftOwnedNodes(w, r, session, origins)
	if !ok {
		return service.IssueGroupParams{}, false
	}
	for i, node := range nodes {
		if _, exists := owned[uuidToString(origins[i])]; exists {
			// Already an issue. Adopted, not rewritten: the group is real work
			// by the time a round is added to it, edited by people and agents,
			// and a follow-up round is not grounds for overwriting that.
			continue
		}
		params, ok := h.issueParamsFromDraft(w, r, workspaceID, session, node, false)
		if !ok {
			return service.IssueGroupParams{}, false
		}
		group.Nodes = append(group.Nodes, service.IssueGroupNode{Params: params})
	}
	return group, true
}

func draftJSONFieldPresent(raw []byte, field string) bool {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	_, ok := payload[field]
	return ok
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

// createIssueGroupForDraft commits the group this confirm is responsible for,
// or adopts what is already committed.
//
// Four mechanisms can hand back an existing group and all of them are needed:
//
//   - a continuation round appends only the nodes that own no issue yet, so a
//     round that adds one node writes one row and a round that adds nothing
//     writes none (group.RootIssueID is set and Nodes holds inserts only);
//   - the origin lookup in readIssueDraftGroupState catches the ordinary retry
//     — a double click, a lost response, a confirm whose process died after the
//     commit but before the draft was pointed at the group;
//   - a first round that arrives with the group already committed adopts it
//     whole, because only reopening makes a payload an increment;
//   - the unique violation on the root insert catches the genuine race, where
//     two confirms were both admitted before either created anything. The
//     loser's children die with its root because the whole group shares one
//     transaction, so a second group cannot exist.
//
// The partial unique index on issue (origin_id) WHERE origin_type =
// 'issue_draft' is the authority for all of them. See §3.3 of the design.
func (h *Handler) createIssueGroupForDraft(w http.ResponseWriter, r *http.Request, session db.ChatSession, state issueDraftGroupState, group service.IssueGroupParams) ([]db.Issue, bool) {
	if group.RootIssueID.Valid {
		if len(group.Nodes) == 0 {
			// The whole payload already exists: the round adds nothing, and the
			// group as it stands is also the answer a retry of this round gives.
			return state.Group, true
		}
		h.prepareIssueGroupOpts(r, session, &group)
		if _, err := h.IssueService.CreateGroup(r.Context(), group); err != nil {
			// A unique violation here means a node this confirm read as new was
			// committed by a concurrent confirm in between. The row exists,
			// which is all this round wanted from it; the read-back below
			// answers with the group as it stands. Anything else is a real
			// failure and reports the way an ordinary create failure does.
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
				writeIssueDraftCreateError(w, r, err)
				return nil, false
			}
		}
		return h.loadIssueGroup(w, r, state.Root)
	}

	// Not a continuation round and the group is already committed. Adopting it
	// whole is the whole point: a first confirm never appends to a group that
	// exists (§3.3 timelines B and C).
	if state.HasRoot {
		return state.Group, true
	}

	rootOrigin := group.Nodes[0].Params.OriginID
	h.prepareIssueGroupOpts(r, session, &group)

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

// carryIssueDraftAttachments binds the files this alignment produced to the
// group's ROOT issue (DENE-453).
//
// An alignment is where a prototype is usually made — a screenshot the user
// dropped in, an HTML mock the carrier uploaded — and the group is what has to
// keep it. Left where they are, those files stay chat-scoped: an attachment
// bound to a chat message is deleted with that message (the FK cascades), so
// the reference an issue's description points at would eventually outlive its
// own file. Binding them to the root is what makes the reference durable.
//
// Root only, deliberately. Every node of the group may link one of these from
// its description, and a markdown link needs no second copy of the bytes: one
// row, owned by the root, IS "the file this alignment produced". Re-uploading
// per node would multiply the storage and split one artifact into as many
// unrelated ones.
//
// Best-effort, like every other create-time attachment bind
// (IssueService.linkAttachments): the group is committed by the time this runs,
// so a failure here must not turn a successful confirm into an error response —
// the client would retry a confirm that already happened. It is also naturally
// idempotent: the query returns only rows nothing owns yet, and
// LinkAttachmentsToIssue only ever fills in a NULL issue_id, so a second
// confirm adopts the group and finds nothing left to carry.
func (h *Handler) carryIssueDraftAttachments(r *http.Request, session db.ChatSession, rootIssue db.Issue) {
	attachments, err := h.Queries.ListAttachmentsByChatSession(r.Context(), db.ListAttachmentsByChatSessionParams{
		WorkspaceID:   session.WorkspaceID,
		ChatSessionID: session.ID,
	})
	if err != nil {
		slog.Error("failed to load issue draft attachments", append(logger.RequestAttrs(r), "error", err)...)
		return
	}
	// Nothing was uploaded, so nothing about the group changes: the confirm
	// that never touched a file issues no extra statement.
	if len(attachments) == 0 {
		return
	}
	ids := make([]pgtype.UUID, 0, len(attachments))
	for _, attachment := range attachments {
		ids = append(ids, attachment.ID)
	}
	if _, err := h.Queries.LinkAttachmentsToIssue(r.Context(), db.LinkAttachmentsToIssueParams{
		IssueID:       rootIssue.ID,
		WorkspaceID:   session.WorkspaceID,
		AttachmentIds: ids,
		// The root was created moments ago and no reader holds a revision of it
		// yet; bumping here would only invalidate the number the create itself
		// just reported.
		BumpRevision: false,
	}); err != nil {
		slog.Error("failed to carry issue draft attachments", append(logger.RequestAttrs(r), "error", err)...)
		return
	}
	// The bytes are the issue's now, so every surface holding that issue's
	// attachments is stale — the confirm's own response among them.
	h.publish(protocol.EventIssueAttachmentsChanged, uuidToString(session.WorkspaceID), "member", uuidToString(session.CreatorID), map[string]any{
		"issue_id": uuidToString(rootIssue.ID),
	})
}

// prepareIssueGroupOpts fills in each node's post-commit options: who acted,
// which agent the analytics event belongs to, the platform, and the
// issue:created payload this transport broadcasts.
//
// Both commit paths go through it, because an appended node is created exactly
// like a first-round one — a follow-up round that skipped this would create
// real work that no board ever heard about.
func (h *Handler) prepareIssueGroupOpts(r *http.Request, session db.ChatSession, group *service.IssueGroupParams) {
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
