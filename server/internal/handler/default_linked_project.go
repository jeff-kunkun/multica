package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/workspacelink"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Default references are kept separate from working projects. NULL on an
// autopilot inherits its project; an explicit empty array disables inheritance.
type LinkedProjectSettingsResponse struct {
	LinkedProjects          []ChatLinkedProjectResponse `json:"linked_projects"`
	EffectiveLinkedProjects []ChatLinkedProjectResponse `json:"effective_linked_projects"`
	Inherited               bool                        `json:"inherited"`
}

func decodeDefaultLinkedProjects(raw []byte) ([]ChatLinkedProjectResponse, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var refs []ChatLinkedProjectResponse
	err := json.Unmarshal(raw, &refs)
	return refs, err
}

func defaultLinkRefs(items []ChatLinkedProjectResponse) []workspacelink.Ref {
	refs := make([]workspacelink.Ref, 0, len(items))
	for _, item := range items {
		refs = append(refs, workspacelink.Ref{LinkID: parseUUID(item.LinkID), ProjectID: parseUUID(item.ProjectID)})
	}
	return refs
}

func (h *Handler) hydrateDefaultLinkedProjects(ctx context.Context, ws pgtype.UUID, role permission.Role, raw []byte) ([]ChatLinkedProjectResponse, error) {
	items, err := decodeDefaultLinkedProjects(raw)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []ChatLinkedProjectResponse{}
	}
	live, err := h.workspaceLinks().Reachable(ctx, ws, role, defaultLinkRefs(items))
	if err != nil {
		return nil, err
	}
	byID := map[string]workspacelink.ReferenceOption{}
	for _, ref := range live {
		byID[ref.LinkID+"/"+ref.ID] = ref
	}
	for i := range items {
		item := &items[i]
		item.Available = false
		item.Icon = nil
		if ref, ok := byID[item.LinkID+"/"+item.ProjectID]; ok {
			item.Title = ref.Title
			item.Icon = ref.Icon
			item.SourceName = ref.Source.Name
			item.Available = true
		}
	}
	return items, nil
}

// Resolve the human the request acts for, never a task token's runtime owner.
func (h *Handler) defaultReferenceMember(w http.ResponseWriter, r *http.Request, ws pgtype.UUID) (db.Member, bool) {
	person := h.autopilotActingUserID(r, uuidToString(ws))
	if person == "" {
		writeError(w, http.StatusForbidden, "a current run originator is required to access reference projects")
		return db.Member{}, false
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: parseUUID(person), WorkspaceID: ws})
	if err != nil || !workspacelink.Decide(workspacelink.OpView, workspacelink.SideViewer, workspacelink.Actor{Role: permission.Role(member.Role)}) {
		writeError(w, http.StatusNotFound, "link not found")
		return db.Member{}, false
	}
	return member, true
}

func (h *Handler) defaultReferenceProject(w http.ResponseWriter, r *http.Request) (db.Project, db.Member, bool) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return db.Project{}, db.Member{}, false
	}
	member, ok := h.defaultReferenceMember(w, r, project.WorkspaceID)
	if !ok {
		return db.Project{}, db.Member{}, false
	}
	viewer, err := h.visibilityViewerForUser(r.Context(), project.WorkspaceID, member.UserID)
	if err != nil || !viewer.canSeeProject(project) {
		writeError(w, http.StatusNotFound, "project not found")
		return db.Project{}, db.Member{}, false
	}
	return project, member, true
}

func (h *Handler) GetProjectLinkedProjects(w http.ResponseWriter, r *http.Request) {
	p, member, ok := h.defaultReferenceProject(w, r)
	if !ok {
		return
	}
	h.writeDefaultReferenceSettings(w, r, p.WorkspaceID, p.LinkedProjects, nil, false, permission.Role(member.Role))
}

func (h *Handler) PutProjectLinkedProjects(w http.ResponseWriter, r *http.Request) {
	p, member, ok := h.defaultReferenceProject(w, r)
	if !ok {
		return
	}
	raw, ok := h.parseDefaultReferenceWrite(w, r, p.WorkspaceID, permission.Role(member.Role), p.LinkedProjects, false)
	if !ok {
		return
	}
	count, err := h.Queries.SetProjectLinkedProjects(r.Context(), db.SetProjectLinkedProjectsParams{ID: p.ID, WorkspaceID: p.WorkspaceID, LinkedProjects: raw})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save reference projects")
		return
	}
	if count == 0 {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	h.writeDefaultReferenceSettings(w, r, p.WorkspaceID, raw, nil, false, permission.Role(member.Role))
}

func (h *Handler) GetAutopilotLinkedProjects(w http.ResponseWriter, r *http.Request) {
	ap, ok := h.loadAutopilotInWorkspace(w, r, chi.URLParam(r, "id"), h.resolveWorkspaceID(r))
	if !ok {
		return
	}
	member, ok := h.defaultReferenceMember(w, r, ap.WorkspaceID)
	if !ok {
		return
	}
	h.writeAutopilotReferenceSettings(w, r, ap, permission.Role(member.Role))
}

func (h *Handler) PutAutopilotLinkedProjects(w http.ResponseWriter, r *http.Request) {
	ap, ok := h.loadAutopilotInWorkspace(w, r, chi.URLParam(r, "id"), h.resolveWorkspaceID(r))
	if !ok {
		return
	}
	member, ok := h.requireAutopilotWrite(w, r, ap, uuidToString(ap.WorkspaceID))
	if !ok {
		return
	}
	if !workspacelink.Decide(workspacelink.OpView, workspacelink.SideViewer, workspacelink.Actor{Role: permission.Role(member.Role)}) {
		writeError(w, http.StatusNotFound, "link not found")
		return
	}
	raw, ok := h.parseDefaultReferenceWrite(w, r, ap.WorkspaceID, permission.Role(member.Role), ap.LinkedProjects, true)
	if !ok {
		return
	}
	count, err := h.Queries.SetAutopilotLinkedProjects(r.Context(), db.SetAutopilotLinkedProjectsParams{ID: ap.ID, WorkspaceID: ap.WorkspaceID, LinkedProjects: raw})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save reference projects")
		return
	}
	if count == 0 {
		writeError(w, http.StatusNotFound, "autopilot not found")
		return
	}
	ap.LinkedProjects = raw
	h.writeAutopilotReferenceSettings(w, r, ap, permission.Role(member.Role))
}

func (h *Handler) projectDefaultReferences(ctx context.Context, ws, project pgtype.UUID) ([]byte, error) {
	if !project.Valid {
		return []byte("[]"), nil
	}
	p, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: project, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return []byte("[]"), nil
	}
	if err != nil {
		return nil, err
	}
	return p.LinkedProjects, nil
}

func (h *Handler) writeAutopilotReferenceSettings(w http.ResponseWriter, r *http.Request, ap db.Autopilot, role permission.Role) {
	var inherited []byte
	if ap.LinkedProjects == nil {
		var err error
		inherited, err = h.projectDefaultReferences(r.Context(), ap.WorkspaceID, ap.ProjectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load project reference projects")
			return
		}
	}
	h.writeDefaultReferenceSettings(w, r, ap.WorkspaceID, ap.LinkedProjects, inherited, ap.LinkedProjects == nil, role)
}

func (h *Handler) writeDefaultReferenceSettings(w http.ResponseWriter, r *http.Request, ws pgtype.UUID, raw, inherited []byte, isInherited bool, role permission.Role) {
	effective := raw
	if isInherited {
		effective = inherited
	}
	items, err := h.hydrateDefaultLinkedProjects(r.Context(), ws, role, effective)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load reference projects")
		return
	}
	resp := LinkedProjectSettingsResponse{LinkedProjects: items, EffectiveLinkedProjects: items, Inherited: isInherited}
	if isInherited {
		resp.LinkedProjects = nil
	}
	writeJSON(w, http.StatusOK, resp)
}

// Like chat replacement, retain already-selected stale snapshots so a user can
// remove one without being forced to drop every other unavailable reference.
func (h *Handler) parseDefaultReferenceWrite(w http.ResponseWriter, r *http.Request, ws pgtype.UUID, role permission.Role, previous []byte, allowInherit bool) ([]byte, bool) {
	var body struct {
		LinkedProjects json.RawMessage `json:"linked_projects"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.LinkedProjects) == 0 {
		writeError(w, http.StatusBadRequest, "linked_projects is required; use an array of link_id/project_id pairs")
		return nil, false
	}
	var raw []ChatLinkedProjectRef
	if err := json.Unmarshal(body.LinkedProjects, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "linked_projects must be an array of link_id/project_id pairs")
		return nil, false
	}
	if raw == nil {
		if allowInherit {
			return nil, true
		}
		writeError(w, http.StatusBadRequest, "project linked_projects must be an array; use [] to clear")
		return nil, false
	}
	refs, ok := parseChatLinkedProjectRefs(w, raw)
	if !ok {
		return nil, false
	}
	refs = dedupeLinkRefs(refs)
	old, err := decodeDefaultLinkedProjects(previous)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load reference projects")
		return nil, false
	}
	bound := map[string]ChatLinkedProjectResponse{}
	for _, item := range old {
		bound[item.LinkID+"/"+item.ProjectID] = item
	}
	live, err := h.workspaceLinks().Reachable(r.Context(), ws, role, refs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate reference projects")
		return nil, false
	}
	for _, item := range live {
		bound[item.LinkID+"/"+item.ID] = ChatLinkedProjectResponse{LinkID: item.LinkID, ProjectID: item.ID, Title: item.Title, SourceName: item.Source.Name}
	}
	out := make([]ChatLinkedProjectResponse, 0, len(refs))
	for _, ref := range refs {
		item, ok := bound[uuidToString(ref.LinkID)+"/"+uuidToString(ref.ProjectID)]
		if !ok {
			writeError(w, http.StatusNotFound, "link not found")
			return nil, false
		}
		item.Available = false
		item.Icon = nil
		out = append(out, item)
	}
	data, err := json.Marshal(out)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save reference projects")
		return nil, false
	}
	return data, true
}

// A run carries its existing attribution. Missing originators never borrow the
// autopilot publisher, runtime owner or project owner to read another workspace.
func (h *Handler) resolveClaimDefaultLinkedProjects(ctx context.Context, task db.AgentTaskQueue, ws, project, autopilot pgtype.UUID) ([]TaskLinkedProjectData, error) {
	if !task.OriginatorUserID.Valid {
		return nil, nil
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: task.OriginatorUserID, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw []byte
	if autopilot.Valid {
		ap, err := h.Queries.GetAutopilotInWorkspace(ctx, db.GetAutopilotInWorkspaceParams{ID: autopilot, WorkspaceID: ws})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			raw = ap.LinkedProjects
			project = ap.ProjectID
		}
	}
	if raw == nil {
		raw, err = h.projectDefaultReferences(ctx, ws, project)
		if err != nil {
			return nil, err
		}
	}
	items, err := decodeDefaultLinkedProjects(raw)
	if err != nil {
		return nil, err
	}
	resolved, err := h.workspaceLinks().References(ctx, ws, permission.Role(member.Role), defaultLinkRefs(items))
	if err != nil {
		return nil, err
	}
	out := make([]TaskLinkedProjectData, 0, len(resolved))
	for _, ref := range resolved {
		out = append(out, TaskLinkedProjectData{Title: ref.Title, SourceName: ref.Source.Name, Description: ref.Description, Resources: ref.Resources, MemoryLine: ref.MemoryLine})
	}
	return out, nil
}
