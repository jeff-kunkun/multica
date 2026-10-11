package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/workspacelink"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var errFastScope = errors.New("requested scope is not accessible")
var errFastLarge = errors.New("snapshot exceeds 10000 items; narrow workspace scope or since")

type fastWorkspace struct {
	ID             string
	Name           string
	Viewer         visibilityViewer
	Agents         []pgtype.UUID
	LinkedProjects []pgtype.UUID
	Managed        bool
	IssuesAllowed  bool
}

// fastScopes uses the originator for source-object visibility. Task credentials
// can cross their home workspace only via the existing linked/managed gate.
func (h *Handler) fastScopes(r *http.Request, user string, req fastRequest) ([]fastWorkspace, error) {
	ctx := r.Context()
	home := ctxWorkspaceID(ctx)
	task := r.Header.Get("X-Actor-Source") == "task_token"
	members, err := h.Queries.ListDaemonWorkspaces(ctx, parseUUID(user))
	if err != nil {
		return nil, err
	}
	scopes := map[string]fastWorkspace{}
	for _, m := range members {
		id := uuidToString(m.ID)
		managed := false
		if task && id != home {
			_, err := h.workspaceLinks().AuthorizeManaged(ctx, workspacelink.ManagedCall{ViewerWS: parseUUID(home), AgentID: parseUUID(r.Header.Get("X-Agent-ID")), TaskID: parseUUID(r.Header.Get("X-Task-ID")), Source: id})
			if err != nil {
				var denied *workspacelink.Error
				if errors.As(err, &denied) {
					continue
				}
				return nil, err
			}
			managed = true
		}
		v, err := h.visibilityViewerForUser(ctx, m.ID, parseUUID(user))
		if err != nil {
			return nil, err
		}
		allowed, ok := h.accessibleAgentIDs(ctx, id, "member", user, string(v.role))
		if !ok {
			return nil, fmt.Errorf("cannot resolve agent access")
		}
		agents := []pgtype.UUID{}
		for a := range allowed {
			agents = append(agents, parseUUID(a))
		}
		module, err := h.loadModuleVisibility(ctx, m.ID, permission.ModuleIssues)
		if err != nil {
			return nil, err
		}
		scopes[id] = fastWorkspace{ID: id, Name: m.Name, Viewer: v, Agents: agents, Managed: managed, IssuesAllowed: v.canSeeModule(module)}
	}
	// Non-managed links grant project status only, never chats or agent identity.
	// Use the same source/project predicates and the central decision table.
	for _, m := range members {
		viewerWS := uuidToString(m.ID)
		if task && viewerWS != home {
			continue
		}
		member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{WorkspaceID: m.ID, UserID: parseUUID(user)})
		if err != nil {
			return nil, err
		}
		if !workspacelink.Decide(workspacelink.OpView, workspacelink.SideViewer, workspacelink.Actor{Role: permission.Role(member.Role), IsAgent: task}) {
			continue
		}
		links, err := h.Queries.ListWorkspaceLinksForWorkspace(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		for _, link := range links {
			if link.TargetWorkspaceID != m.ID || link.Status != "active" {
				continue
			}
			id := uuidToString(link.SourceWorkspaceID)
			if existing, ok := scopes[id]; ok && existing.Viewer.userID.Valid {
				continue
			}
			projects, err := h.Queries.ListWorkspaceLinkProjects(ctx, db.ListWorkspaceLinkProjectsParams{LinkID: link.ID, SourceWorkspaceID: link.SourceWorkspaceID})
			if err != nil {
				return nil, err
			}
			s := scopes[id]
			s.ID = id
			s.Name = link.SourceName
			s.IssuesAllowed = true
			s.Agents = []pgtype.UUID{}
			for _, p := range projects {
				if p.Visibility != "private" {
					s.LinkedProjects = append(s.LinkedProjects, p.ID)
				}
			}
			scopes[id] = s
		}
	}
	if req.Scope.Mode == "workspace_ids" {
		selected := map[string]fastWorkspace{}
		for _, id := range req.Scope.WorkspaceIDs {
			normalized := uuidToString(parseUUID(id))
			s, ok := scopes[normalized]
			if !ok {
				return nil, errFastScope
			}
			selected[normalized] = s
		}
		scopes = selected
	}
	out := make([]fastWorkspace, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

type fastSQL struct{ args []any }

func (q *fastSQL) add(v any) string {
	q.args = append(q.args, v)
	return fmt.Sprintf("$%d", len(q.args))
}
func (s fastWorkspace) issueSQL(q *fastSQL, alias string) string {
	if !s.IssuesAllowed {
		return "FALSE"
	}
	if s.Viewer.userID.Valid {
		return s.Viewer.issueVisibilitySQL(alias, q.add)
	}
	return fmt.Sprintf("(%s.visibility <> 'private' AND %s.triage_state IS NULL AND %s.project_id=ANY(%s::uuid[]) AND EXISTS(SELECT 1 FROM project fp WHERE fp.id=%s.project_id AND fp.workspace_id=%s.workspace_id AND fp.visibility<>'private'))", alias, alias, alias, q.add(s.LinkedProjects), alias, alias)
}
func (s fastWorkspace) chatSQL(q *fastSQL, alias string) string {
	if !s.Viewer.userID.Valid {
		return "FALSE"
	}
	user := q.add(s.Viewer.userID)
	projects := q.add(s.Viewer.projectIDs)
	workspace := "FALSE"
	if s.Viewer.role != permission.RoleGuest {
		workspace = alias + ".visibility='workspace'"
	}
	predicate := fmt.Sprintf(`(%[1]s.creator_id=%[2]s::uuid OR %[3]s OR (%[1]s.visibility='project' AND (%[1]s.project_id=ANY(%[4]s::uuid[]) OR EXISTS(SELECT 1 FROM chat_session_project fp WHERE fp.chat_session_id=%[1]s.id AND fp.project_id=ANY(%[4]s::uuid[])) OR EXISTS(SELECT 1 FROM resource_share fs WHERE fs.workspace_id=%[1]s.workspace_id AND fs.resource_type='chat_session' AND fs.resource_id=%[1]s.id::text AND fs.member_id=%[2]s::uuid))))`, alias, user, workspace, projects)
	if s.Managed {
		predicate += " AND " + alias + ".creator_id=" + user + "::uuid"
	}
	return "(" + predicate + ")"
}
func fastKey(item fastItem) string { return item.WorkspaceID + "/" + item.Kind + "/" + item.ID }
func fastIDs(items []fastItem, ws, kind string) []pgtype.UUID {
	out := []pgtype.UUID{}
	for _, item := range items {
		if item.WorkspaceID == ws && item.Kind == kind {
			out = append(out, parseUUID(item.ID))
		}
	}
	return out
}

// Every query projects only compact text and identity/visibility columns. No
// task context/prompt/result, chat transcript or issue description is loaded.
func (h *Handler) fastRows(ctx context.Context, scopes []fastWorkspace, req fastRequest, asOf time.Time, authorize []fastItem) ([]fastItem, error) {
	out := []fastItem{}
	since := time.Unix(0, 0)
	if req.Since != nil {
		since = *req.Since
	}
	for _, s := range scopes {
		if fastIncludes(req, "issues") {
			q := &fastSQL{}
			ws := q.add(parseUUID(s.ID))
			visibility := s.issueSQL(q, "i")
			window := q.add(since)
			countWindow := window
			if req.countSince != nil {
				countWindow = q.add(*req.countSince)
			}
			filter := "GREATEST(i.updated_at,COALESCE(cm.changed_at,i.updated_at)) >= " + window + "::timestamptz"
			if authorize != nil {
				filter = "i.id=ANY(" + q.add(fastIDs(authorize, s.ID, "issue")) + "::uuid[])"
			}
			query := `SELECT i.id::text,left(i.title,81),w.issue_prefix||'-'||i.number::text,i.status,GREATEST(i.updated_at,COALESCE(cm.changed_at,i.updated_at)),COALESCE(cm.new_count,0)::int
    FROM issue i JOIN workspace w ON w.id=i.workspace_id
    LEFT JOIN LATERAL(SELECT max(GREATEST(c.created_at,c.updated_at)) changed_at,count(*) FILTER(WHERE c.created_at >= ` + countWindow + `::timestamptz AND c.deleted_at IS NULL) new_count FROM comment c WHERE c.issue_id=i.id AND c.workspace_id=i.workspace_id) cm ON true
    WHERE i.workspace_id=` + ws + `::uuid AND ` + visibility + ` AND ` + filter + ` ORDER BY i.id LIMIT 10001`
			rows, err := h.DB.Query(ctx, query, q.args...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				item := fastItem{WorkspaceID: s.ID, Kind: "issue", Op: "upsert"}
				var count int
				if err = rows.Scan(&item.ID, &item.Title, &item.Identifier, &item.Status, &item.ChangedAt, &count); err != nil {
					rows.Close()
					return nil, err
				}
				if s.Viewer.userID.Valid {
					item.NewCommentCount = &count
				}
				item.ChangedFields = []string{}
				out = append(out, item)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
		if fastIncludes(req, "chats") && s.Viewer.userID.Valid {
			q := &fastSQL{}
			ws := q.add(parseUUID(s.ID))
			visibility := s.chatSQL(q, "cs")
			user := q.add(s.Viewer.userID)
			window := q.add(since)
			filter := "GREATEST(cs.updated_at,COALESCE(lm.created_at,cs.updated_at),COALESCE(cr.last_read_at,cs.updated_at)) >= " + window + "::timestamptz"
			if authorize != nil {
				filter = "cs.id=ANY(" + q.add(fastIDs(authorize, s.ID, "chat")) + "::uuid[]) AND " + window + "::timestamptz IS NOT NULL"
			}
			query := `SELECT cs.id::text,left(cs.title,81),left(COALESCE(lm.content,''),161),cs.status,
    GREATEST(cs.updated_at,COALESCE(lm.created_at,cs.updated_at),COALESCE(cr.last_read_at,cs.updated_at)),GREATEST(cs.updated_at,COALESCE(lm.created_at,cs.updated_at)),
    CASE WHEN cs.status='archived' THEN 0 ELSE (SELECT count(*) FROM chat_message m WHERE m.chat_session_id=cs.id AND m.message_kind NOT IN ('channel_command','onboarding_kickoff') AND m.created_at>COALESCE(cr.last_read_at,CASE WHEN cs.creator_id=` + user + `::uuid THEN cs.last_read_at ELSE now() END) AND (m.role='assistant' OR (m.role='user' AND (m.message_kind='linked_delivery' OR COALESCE(m.sender_user_id,cs.creator_id)<>` + user + `::uuid)))) END::int
    FROM chat_session cs LEFT JOIN chat_session_read cr ON cr.chat_session_id=cs.id AND cr.user_id=` + user + `::uuid
    LEFT JOIN LATERAL(SELECT left(m.content,161) content,m.created_at FROM chat_message m WHERE m.chat_session_id=cs.id AND m.message_kind NOT IN ('channel_command','onboarding_kickoff') ORDER BY m.created_at DESC,m.id DESC LIMIT 1) lm ON true
    WHERE cs.workspace_id=` + ws + `::uuid AND ` + visibility + ` AND (cs.explicitly_created_at IS NOT NULL OR lm.created_at IS NOT NULL) AND ` + filter + ` ORDER BY cs.id LIMIT 10001`
			rows, err := h.DB.Query(ctx, query, q.args...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				item := fastItem{WorkspaceID: s.ID, Kind: "chat", Op: "upsert"}
				var count int
				var at time.Time
				if err = rows.Scan(&item.ID, &item.Title, &item.Preview, &item.Status, &item.ChangedAt, &at, &count); err != nil {
					rows.Close()
					return nil, err
				}
				item.LastActiveAt = &at
				item.UnreadCount = &count
				out = append(out, item)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
		if (fastIncludes(req, "active_runs") || fastIncludes(req, "run_changes")) && s.Viewer.userID.Valid {
			items, err := h.fastRunRows(ctx, s, req, since, authorize)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
		}
		if len(out) > 10000 {
			return nil, errFastLarge
		}
	}
	names := map[string]string{}
	for _, scope := range scopes {
		names[scope.ID] = scope.Name
	}
	for i := range out {
		item := &out[i]
		item.WorkspaceName, _ = fastClip(names[item.WorkspaceID], 80, 240)
		var a, b bool
		item.Title, a = fastClip(item.Title, 80, 240)
		item.Preview, b = fastClip(item.Preview, 160, 480)
		item.ContentTruncated = a || b
		item.Revision = item.ChangedAt.UTC().Format(time.RFC3339Nano)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ChangedAt.Equal(out[j].ChangedAt) {
			return fastKey(out[i]) < fastKey(out[j])
		}
		return out[i].ChangedAt.Before(out[j].ChangedAt)
	})
	return out, nil
}

const fastActiveSQL = `(t.status IN ('queued','dispatched','running','waiting_local_directory') OR (t.status='deferred' AND t.context->>'wakeup_id' IS NOT NULL))`

func (h *Handler) fastRunRows(ctx context.Context, s fastWorkspace, req fastRequest, since time.Time, authorize []fastItem) ([]fastItem, error) {
	q := &fastSQL{}
	agents := q.add(s.Agents)
	ws := q.add(parseUUID(s.ID))
	issue := s.issueSQL(q, "i")
	chat := s.chatSQL(q, "cs")
	window := q.add(since)
	changed := "GREATEST(t.created_at,t.dispatched_at,t.started_at,t.completed_at)"
	filter := fastActiveSQL
	if fastIncludes(req, "run_changes") {
		filter = "(" + filter + " OR " + changed + ">=" + window + "::timestamptz)"
	} else {
		filter += " AND " + window + "::timestamptz IS NOT NULL"
	}
	if authorize != nil {
		filter = "t.id=ANY(" + q.add(fastIDs(authorize, s.ID, "run")) + "::uuid[]) AND " + window + "::timestamptz IS NOT NULL"
	}
	query := `SELECT t.id::text,t.agent_id::text,t.status,` + changed + `,t.started_at,t.completed_at,
 CASE WHEN t.chat_session_id IS NOT NULL THEN 'chat' WHEN t.autopilot_run_id IS NOT NULL THEN 'autopilot_run' WHEN t.issue_id IS NOT NULL THEN 'issue' ELSE 'agent' END,
 COALESCE(t.chat_session_id,t.autopilot_run_id,t.issue_id,t.agent_id)::text,` + fastActiveSQL + `,
 left(COALESCE((SELECT title FROM chat_session WHERE id=t.chat_session_id),(SELECT ap.title FROM autopilot_run ar JOIN autopilot ap ON ap.id=ar.autopilot_id WHERE ar.id=t.autopilot_run_id),(SELECT title FROM issue WHERE id=t.issue_id),(SELECT name FROM agent WHERE id=t.agent_id)),81)
 FROM agent_task_queue t
 WHERE t.agent_id=ANY(` + agents + `::uuid[]) AND ` + filter + `
 AND (t.issue_id IS NULL OR EXISTS(SELECT 1 FROM issue i WHERE i.id=t.issue_id AND i.workspace_id=` + ws + `::uuid AND ` + issue + `))
 AND (t.chat_session_id IS NULL OR EXISTS(SELECT 1 FROM chat_session cs WHERE cs.id=t.chat_session_id AND cs.workspace_id=` + ws + `::uuid AND ` + chat + ` AND (cs.explicitly_created_at IS NOT NULL OR EXISTS(SELECT 1 FROM chat_message cm WHERE cm.chat_session_id=cs.id AND cm.message_kind NOT IN ('channel_command','onboarding_kickoff')))))
 AND (t.autopilot_run_id IS NULL OR EXISTS(SELECT 1 FROM autopilot_run ar JOIN autopilot ap ON ap.id=ar.autopilot_id WHERE ar.id=t.autopilot_run_id AND ap.workspace_id=` + ws + `::uuid AND (ar.issue_id IS NULL OR EXISTS(SELECT 1 FROM issue i WHERE i.id=ar.issue_id AND i.workspace_id=ap.workspace_id AND ` + issue + `))))
 ORDER BY t.id LIMIT 10001`
	rows, err := h.DB.Query(ctx, query, q.args...)
	if err != nil {
		return nil, err
	}
	out := []fastItem{}
	for rows.Next() {
		item := fastItem{WorkspaceID: s.ID, Kind: "run", Op: "upsert"}
		var start, end pgtype.Timestamptz
		if err = rows.Scan(&item.ID, &item.AgentID, &item.Status, &item.ChangedAt, &start, &end, &item.SourceType, &item.SourceID, &item.active, &item.Title); err != nil {
			rows.Close()
			return nil, err
		}
		if start.Valid {
			item.StartedAt = &start.Time
		}
		if end.Valid {
			item.FinishedAt = &end.Time
		}
		item.Phase = fastPhase(item.Status)
		out = append(out, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Autopilot executions are distinct from their agent task attempts.
	q = &fastSQL{}
	ws = q.add(parseUUID(s.ID))
	agents = q.add(s.Agents)
	window = q.add(since)
	filter = "ar.status IN ('issue_created','running')"
	if fastIncludes(req, "run_changes") {
		filter = "(" + filter + " OR GREATEST(ar.created_at,ar.triggered_at,ar.completed_at)>=" + window + "::timestamptz)"
	} else {
		filter += " AND " + window + "::timestamptz IS NOT NULL"
	}
	if authorize != nil {
		filter = "ar.id=ANY(" + q.add(fastIDs(authorize, s.ID, "autopilot_run")) + "::uuid[]) AND " + window + "::timestamptz IS NOT NULL"
	}
	issue = s.issueSQL(q, "i")
	query = `SELECT ar.id::text,ap.id::text,ar.status,GREATEST(ar.created_at,ar.triggered_at,ar.completed_at),ar.triggered_at,ar.completed_at,ar.status IN ('issue_created','running'),left(ap.title,81)
 FROM autopilot_run ar JOIN autopilot ap ON ap.id=ar.autopilot_id
 WHERE ap.workspace_id=` + ws + `::uuid AND ` + filter + ` AND ((ap.assignee_type='agent' AND ap.assignee_id=ANY(` + agents + `::uuid[])) OR (ap.assignee_type='squad' AND EXISTS(SELECT 1 FROM squad sq WHERE sq.id=ap.assignee_id AND sq.workspace_id=ap.workspace_id AND sq.leader_id=ANY(` + agents + `::uuid[]))))
 AND (ar.issue_id IS NULL OR EXISTS(SELECT 1 FROM issue i WHERE i.id=ar.issue_id AND i.workspace_id=ap.workspace_id AND ` + issue + `))
 ORDER BY ar.id LIMIT 10001`
	rows, err = h.DB.Query(ctx, query, q.args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		item := fastItem{WorkspaceID: s.ID, Kind: "autopilot_run", Op: "upsert", SourceType: "autopilot"}
		var start, end pgtype.Timestamptz
		if err = rows.Scan(&item.ID, &item.SourceID, &item.Status, &item.ChangedAt, &start, &end, &item.active, &item.Title); err != nil {
			rows.Close()
			return nil, err
		}
		if start.Valid {
			item.StartedAt = &start.Time
		}
		if end.Valid {
			item.FinishedAt = &end.Time
		}
		item.Phase = fastPhase(item.Status)
		out = append(out, item)
	}
	err = rows.Err()
	rows.Close()
	return out, err
}
func fastPhase(status string) string {
	switch status {
	case "running", "dispatched":
		return "executing"
	case "deferred", "waiting_local_directory":
		return "waiting"
	case "queued", "issue_created":
		return "queued"
	default:
		return ""
	}
}

// Authentication already binds these headers. Read attribution columns only;
// resolving the principal must not fetch the calling task's context/prompt.
func (h *Handler) fastViewer(r *http.Request, fallback string) (string, error) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		return fallback, nil
	}
	var viewer pgtype.UUID
	err := h.DB.QueryRow(r.Context(), `SELECT t.originator_user_id FROM agent_task_queue t JOIN agent a ON a.id=t.agent_id WHERE t.id=$1 AND a.workspace_id=$2 AND t.agent_id=$3`, parseUUID(r.Header.Get("X-Task-ID")), parseUUID(ctxWorkspaceID(r.Context())), parseUUID(r.Header.Get("X-Agent-ID"))).Scan(&viewer)
	if err != nil {
		return "", err
	}
	return uuidToString(viewer), nil
}
