package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Overview deliberately composes the existing read handlers in-process. Their
// viewer/actor rules remain authoritative; this is one HTTP request, not a loop
// of authenticated requests back to our own server. Only allow-listed summary
// fields leave this endpoint, even when the caller can read full configuration.
type overviewRow = map[string]any

type overviewSource struct {
	name, envelope, fields string
	handler                http.HandlerFunc
	module                 permission.Module
}

type overviewBuffer struct {
	bytes.Buffer
	header http.Header
	status int
}

func (b *overviewBuffer) Header() http.Header    { return b.header }
func (b *overviewBuffer) WriteHeader(status int) { b.status = status }

func overviewRead(r *http.Request, fn http.HandlerFunc, envelope string) ([]overviewRow, bool, error) {
	b := &overviewBuffer{header: make(http.Header), status: http.StatusOK}
	fn(b, r)
	if b.status == http.StatusForbidden || b.status == http.StatusNotFound {
		return []overviewRow{}, true, nil
	}
	if b.status != http.StatusOK {
		return nil, false, fmt.Errorf("section returned HTTP %d", b.status)
	}
	raw := b.Bytes()
	if envelope != "" {
		var wrapped map[string]json.RawMessage
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, false, err
		}
		raw = wrapped[envelope]
	}
	rows := []overviewRow{}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, false, err
	}
	if rows == nil {
		rows = []overviewRow{}
	}
	return rows, false, nil
}

func overviewProject(row overviewRow, fields string) overviewRow {
	result := overviewRow{}
	for _, key := range strings.Fields(fields) {
		if value, ok := row[key]; ok {
			result[key] = value
		}
	}
	if description, ok := result["description"].(string); ok {
		result["description"] = directorySummary(description)
	}
	return result
}
func overviewString(row overviewRow, key string) string { value, _ := row[key].(string); return value }
func overviewStrings(value any) []string {
	result := []string{}
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		for _, item := range values {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
	}
	return result
}
func overviewRequest(r *http.Request, params map[string]string) *http.Request {
	clone := r.Clone(r.Context())
	u := *r.URL
	u.RawQuery = url.Values{"all_projects": {"true"}}.Encode()
	clone.URL = &u
	route := chi.NewRouteContext()
	for key, value := range params {
		route.URLParams.Add(key, value)
	}
	return clone.WithContext(context.WithValue(clone.Context(), chi.RouteCtxKey, route))
}

func (h *Handler) GetOverview(w http.ResponseWriter, r *http.Request) {
	ws := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, ws); !ok {
		return
	}
	compact := r.URL.Query().Get("compact") == "true" || r.URL.Query().Get("compact") == "1"
	request := overviewRequest(r, nil)
	sections := map[string][]overviewRow{}
	omitted := []string{}
	sources := []overviewSource{
		{"agents", "", "id name description model runtime_id runtime_mode runtime_availability parent_agent_id parent_agent_name domain_id routing_tier thinking_level service_tier work_enabled work_pause dispatch_mode dispatch_projects skills", h.ListAgents, ""},
		{"projects", "projects", "id title description status lead_type lead_id domain_ids", h.ListProjects, permission.ModuleProjects},
		{"chats", "", "id title project_id agent_id agent_name status last_active_at", h.ListChatDirectory, ""},
		{"automations", "autopilots", "id title description project_id assignee_type assignee_id status execution_mode trigger_kinds next_run_at", h.ListAutopilots, ""},
		{"skills", "", "id name description", h.ListSkills, ""},
		{"squads", "", "id name description leader_id members member_count", h.ListSquads, ""},
		{"runtimes", "", "id name custom_name runtime_mode provider status device_info daemon_id", h.ListAgentRuntimes, permission.ModuleRuntimes},
		{"workspace_links", "links", "id side status source target projects managed", h.ListWorkspaceLinks, ""},
	}
	for _, source := range sources {
		fn := source.handler
		if source.module != "" {
			fn = h.RequireModule(source.module)(fn).ServeHTTP
		}
		rows, denied, err := overviewRead(request, fn, source.envelope)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load overview "+source.name)
			return
		}
		sections[source.name] = make([]overviewRow, 0, len(rows))
		for _, row := range rows {
			if source.name == "chats" && overviewString(row, "status") != "active" {
				continue
			}
			sections[source.name] = append(sections[source.name], overviewProject(row, source.fields))
		}
		if denied {
			omitted = append(omitted, source.name)
		}
	}
	// Bot-key inventory follows the same owner/admin and human-actor gate as
	// settings. Bot authentication itself is supported by the normal auth layer.
	sections["external_connections"] = []overviewRow{}
	member, _ := h.getWorkspaceMember(r.Context(), requestUserID(r), ws)
	if roleAllowed(member.Role, "owner", "admin") {
		rows, denied, err := overviewRead(overviewRequest(r, map[string]string{"id": ws}), RequireHumanActor(http.HandlerFunc(h.ListBots)).ServeHTTP, "")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load overview external_connections")
			return
		}
		if denied {
			omitted = append(omitted, "external_connections")
		}
		for _, row := range rows {
			sections["external_connections"] = append(sections["external_connections"], overviewProject(row, "id name type status last_used_at"))
		}
	} else {
		omitted = append(omitted, "external_connections")
	}
	if err := h.enrichOverview(request, ws, sections); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load overview relationships")
		return
	}
	if err := filterOverview(sections, r.URL.Query().Get("project"), r.URL.Query().Get("agent")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	counts := map[string]int{}
	result := map[string]any{"workspace_id": ws, "compact": compact, "omitted_sections": omitted, "counts": counts}
	for name, rows := range sections {
		counts[name] = len(rows)
		if compact {
			for i, row := range rows {
				rows[i] = overviewProject(row, "id name title model runtime_id routing_tier runtime_availability work_enabled status project_id agent_id assignee_id next_run_at leader_id source target type")
			}
		}
		result[name] = rows
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) enrichOverview(r *http.Request, ws string, sections map[string][]overviewRow) error {
	projectIDs := []pgtype.UUID{}
	projects := map[string]overviewRow{}
	for _, project := range sections["projects"] {
		id := overviewString(project, "id")
		projects[id] = project
		projectIDs = append(projectIDs, parseUUID(id))
		project["resources"] = []overviewRow{}
	}
	if len(projectIDs) > 0 {
		resources, err := h.Queries.ListProjectResourcesForProjectsInWorkspace(r.Context(), db.ListProjectResourcesForProjectsInWorkspaceParams{WorkspaceID: parseUUID(ws), ProjectIds: projectIDs})
		if err != nil {
			return err
		}
		for _, resource := range resources {
			project := projects[uuidToString(resource.ProjectID)]
			if project == nil {
				continue
			}
			var ref overviewRow
			if err := json.Unmarshal(resource.ResourceRef, &ref); err != nil {
				return err
			}
			project["resources"] = append(project["resources"].([]overviewRow), overviewRow{"id": uuidToString(resource.ID), "resource_type": resource.ResourceType, "resource_ref": overviewProject(ref, "url repo_key local_path real_path daemon_id execution_mode")})
		}
	}
	// Display names use the already-visible agent/squad set and the ordinary
	// member directory. Missing/deleted principals keep their id without a name.
	domains, err := h.Queries.ListWorkspaceDomains(r.Context(), parseUUID(ws))
	if err != nil {
		return err
	}
	domainNames := map[string]string{}
	for _, domain := range domains {
		domainNames[uuidToString(domain.ID)] = domain.Name
	}
	for _, agent := range sections["agents"] {
		agent["domain_name"] = domainNames[overviewString(agent, "domain_id")]
	}
	names := map[string]string{}
	members, err := h.Queries.ListMembersWithUser(r.Context(), parseUUID(ws))
	if err != nil {
		return err
	}
	for _, member := range members {
		names[uuidToString(member.UserID)] = member.UserName
	}
	for _, section := range []string{"agents", "squads"} {
		for _, row := range sections[section] {
			names[overviewString(row, "id")] = overviewString(row, "name")
		}
	}
	for _, project := range sections["projects"] {
		project["lead_name"] = names[overviewString(project, "lead_id")]
	}
	for _, automation := range sections["automations"] {
		automation["assignee_name"] = names[overviewString(automation, "assignee_id")]
	}
	for _, chat := range sections["chats"] {
		ids := []string{}
		if pid := overviewString(chat, "project_id"); projects[pid] != nil {
			ids = append(ids, pid)
		}
		attached, err := h.Queries.ListChatSessionProjectsInWorkspace(r.Context(), db.ListChatSessionProjectsInWorkspaceParams{WorkspaceID: parseUUID(ws), ChatSessionID: parseUUID(overviewString(chat, "id"))})
		if err != nil {
			return err
		}
		for _, project := range attached {
			if id := uuidToString(project.ID); projects[id] != nil {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		chat["project_ids"] = slices.Compact(ids)
	}
	for _, automation := range sections["automations"] {
		triggers, err := h.Queries.ListAutopilotTriggers(r.Context(), parseUUID(overviewString(automation, "id")))
		if err != nil {
			return err
		}
		schedules := []overviewRow{}
		for _, trigger := range triggers {
			schedules = append(schedules, overviewRow{"kind": trigger.Kind, "enabled": trigger.Enabled, "cron_expression": textToPtr(trigger.CronExpression), "timezone": textToPtr(trigger.Timezone), "next_run_at": timestampToPtr(trigger.NextRunAt)})
		}
		automation["schedules"] = schedules
	}
	memberships, err := h.Queries.ListWorkspaceProjectMemberships(r.Context(), parseUUID(ws))
	if err != nil {
		return err
	}
	for _, agent := range sections["agents"] {
		id := overviewString(agent, "id")
		related := []string{}
		for _, project := range sections["projects"] {
			pid := overviewString(project, "id")
			if overviewString(project, "lead_type") == "agent" && overviewString(project, "lead_id") == id {
				related = append(related, pid)
			}
			if slices.Contains(overviewStrings(agent["dispatch_projects"]), pid) {
				related = append(related, pid)
			}
		}
		for _, membership := range memberships {
			if uuidToString(membership.MemberID) == id && projects[uuidToString(membership.ProjectID)] != nil {
				related = append(related, uuidToString(membership.ProjectID))
			}
		}
		for _, chat := range sections["chats"] {
			if overviewString(chat, "agent_id") == id {
				related = append(related, overviewStrings(chat["project_ids"])...)
			}
		}
		for _, automation := range sections["automations"] {
			pid := overviewString(automation, "project_id")
			assigned := overviewString(automation, "assignee_type") == "agent" && overviewString(automation, "assignee_id") == id
			if overviewString(automation, "assignee_type") == "squad" {
				for _, squad := range sections["squads"] {
					if overviewString(squad, "id") != overviewString(automation, "assignee_id") {
						continue
					}
					assigned = overviewString(squad, "leader_id") == id
					if members, ok := squad["members"].([]any); ok {
						for _, value := range members {
							if member, ok := value.(map[string]any); ok && overviewString(member, "member_type") == "agent" && overviewString(member, "member_id") == id {
								assigned = true
							}
						}
					}
				}
			}
			if assigned && projects[pid] != nil {
				related = append(related, pid)
			}
		}
		slices.Sort(related)
		agent["project_ids"] = slices.Compact(related)
		if bindings, ok := agent["skills"].([]any); ok {
			for i, value := range bindings {
				if skill, ok := value.(map[string]any); ok {
					bindings[i] = overviewProject(skill, "id name enabled")
				}
			}
		}
		// Do not expose invisible project ids from dispatch configuration.
		agent["dispatch_projects"] = intersectOverviewIDs(overviewStrings(agent["dispatch_projects"]), projects)
	}
	// A visible specialization can also use its visible base role's skills.
	// Keep the binding projection small and deduplicate an explicit override.
	agentsByID := map[string]overviewRow{}
	for _, agent := range sections["agents"] {
		agentsByID[overviewString(agent, "id")] = agent
	}
	for _, agent := range sections["agents"] {
		parent := agentsByID[overviewString(agent, "parent_agent_id")]
		if parent == nil {
			continue
		}
		bindings, _ := agent["skills"].([]any)
		seen := map[string]bool{}
		for _, value := range bindings {
			if binding, ok := value.(map[string]any); ok {
				seen[overviewString(binding, "id")] = true
			}
		}
		inherited, _ := parent["skills"].([]any)
		for _, value := range inherited {
			if binding, ok := value.(map[string]any); ok && !seen[overviewString(binding, "id")] {
				bindings = append(bindings, overviewProject(binding, "id name enabled"))
			}
		}
		agent["skills"] = bindings
	}
	issuesAllowed, err := h.callerCanSeeModule(r, parseUUID(ws), permission.ModuleIssues)
	if err != nil {
		return err
	}
	viewer, err := h.visibilityViewerFor(r, parseUUID(ws))
	if err != nil {
		return err
	}
	for _, chat := range sections["chats"] {
		tickets := []overviewRow{}
		if issuesAllowed {
			rows, err := h.Queries.ListChatSessionTicketIssues(r.Context(), db.ListChatSessionTicketIssuesParams{WorkspaceID: parseUUID(ws), ChatSessionID: parseUUID(overviewString(chat, "id"))})
			if err != nil {
				return err
			}
			for _, row := range rows {
				if viewer.canSeeIssue(row.Issue) {
					tickets = append(tickets, overviewRow{"id": uuidToString(row.Issue.ID), "title": row.Issue.Title, "status": row.Issue.Status})
				}
			}
		}
		chat["tickets"] = tickets
	}
	return nil
}
func intersectOverviewIDs(ids []string, visible map[string]overviewRow) []string {
	result := []string{}
	for _, id := range ids {
		if visible[id] != nil {
			result = append(result, id)
		}
	}
	return result
}

func overviewResolve(rows []overviewRow, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	for _, row := range rows {
		if overviewString(row, "id") == ref {
			return ref, nil
		}
	}
	matches := []string{}
	for _, row := range rows {
		if overviewString(row, "name") == ref || overviewString(row, "title") == ref {
			matches = append(matches, overviewString(row, "id"))
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return "", fmt.Errorf("filter must identify one visible item by ID or exact name")
}

func filterOverview(sections map[string][]overviewRow, projectRef, agentRef string) error {
	project, err := overviewResolve(sections["projects"], projectRef)
	if err != nil {
		return err
	}
	agent, err := overviewResolve(sections["agents"], agentRef)
	if err != nil {
		return err
	}
	if project == "" && agent == "" {
		return nil
	}
	keep := func(name string, predicate func(overviewRow) bool) {
		rows := []overviewRow{}
		for _, row := range sections[name] {
			if predicate(row) {
				rows = append(rows, row)
			}
		}
		sections[name] = rows
	}
	keep("agents", func(row overviewRow) bool {
		return (agent == "" || overviewString(row, "id") == agent) && (project == "" || slices.Contains(overviewStrings(row["project_ids"]), project))
	})
	agents := map[string]bool{}
	projects := map[string]bool{}
	runtimes := map[string]bool{}
	skills := map[string]bool{}
	for _, row := range sections["agents"] {
		agents[overviewString(row, "id")] = true
		runtimes[overviewString(row, "runtime_id")] = true
		for _, id := range overviewStrings(row["project_ids"]) {
			projects[id] = true
		}
		if bindings, ok := row["skills"].([]any); ok {
			for _, binding := range bindings {
				if b, ok := binding.(map[string]any); ok {
					skills[overviewString(b, "id")] = true
				}
			}
		}
	}
	keep("projects", func(row overviewRow) bool {
		id := overviewString(row, "id")
		return (project == "" || id == project) && (agent == "" || projects[id])
	})
	keep("squads", func(row overviewRow) bool {
		if agents[overviewString(row, "leader_id")] {
			return true
		}
		if members, ok := row["members"].([]any); ok {
			for _, member := range members {
				if m, ok := member.(map[string]any); ok && agents[overviewString(m, "member_id")] {
					return true
				}
			}
		}
		return false
	})
	squads := map[string]bool{}
	for _, row := range sections["squads"] {
		squads[overviewString(row, "id")] = true
	}
	keep("automations", func(row overviewRow) bool {
		id := overviewString(row, "assignee_id")
		return (project == "" || overviewString(row, "project_id") == project) && (agent == "" || agents[id] || squads[id])
	})
	keep("chats", func(row overviewRow) bool {
		return (project == "" || slices.Contains(overviewStrings(row["project_ids"]), project)) && (agent == "" || agents[overviewString(row, "agent_id")])
	})
	keep("runtimes", func(row overviewRow) bool { return runtimes[overviewString(row, "id")] })
	keep("skills", func(row overviewRow) bool { return skills[overviewString(row, "id")] })
	keep("workspace_links", func(row overviewRow) bool {
		if links, ok := row["projects"].([]any); ok {
			for _, item := range links {
				if p, ok := item.(map[string]any); ok && ((project != "" && overviewString(p, "id") == project) || (project == "" && projects[overviewString(p, "id")])) {
					return true
				}
			}
		}
		return false
	})
	selectedProjects := map[string]overviewRow{}
	for _, row := range sections["projects"] {
		selectedProjects[overviewString(row, "id")] = row
	}
	for _, section := range []string{"agents", "chats"} {
		for _, row := range sections[section] {
			row["project_ids"] = intersectOverviewIDs(overviewStrings(row["project_ids"]), selectedProjects)
			if section == "agents" {
				row["dispatch_projects"] = intersectOverviewIDs(overviewStrings(row["dispatch_projects"]), selectedProjects)
			}
		}
	}
	for _, link := range sections["workspace_links"] {
		filtered := []any{}
		if bound, ok := link["projects"].([]any); ok {
			for _, value := range bound {
				if p, ok := value.(map[string]any); ok && selectedProjects[overviewString(p, "id")] != nil {
					filtered = append(filtered, value)
				}
			}
		}
		link["projects"] = filtered
	}
	sections["external_connections"] = []overviewRow{} // keys are workspace-scoped, without project/agent bindings
	return nil
}
