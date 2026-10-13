package officemcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const serverInstructions = `You are the project manager of a Multica office. The boss starts projects;
member agents do the work. Typical loop: create_project -> open_topic (one
member agent drafts a plan on that issue) -> read_plan -> request_approval
(asks the boss) -> approve_plan (only if you were granted release authority,
otherwise the boss approves) -> the approved plan creates the work tickets ->
follow progress with get_events -> close_issue when done. Issues accept a
UUID or a key like ABC-12. Agents start working when an issue is assigned to
them or when a comment @mentions them as [@Name](mention://agent/<id>); every
run costs money, so mention only to start real work.`

type args map[string]any

func (a args) str(key string) string {
	switch v := a[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func (a args) required(key string) (string, error) {
	v := a.str(key)
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

// optional adds key to body when the argument is present.
func (a args) optional(body map[string]any, key string) {
	if v := a.str(key); v != "" {
		body[key] = v
	}
}

type tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Call        func(c *caller, a args) (any, error)
}

func schema(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strProp(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func enumProp(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

const issueDesc = "Issue UUID or key such as ABC-12."

func issuePath(a args, suffix string) (string, error) {
	id, err := a.required("issue")
	if err != nil {
		return "", err
	}
	return "/api/issues/" + url.PathEscape(id) + suffix, nil
}

// trim keeps only keys in every object of every list in raw, so list tools
// return what a model needs instead of full records.
func trim(raw json.RawMessage, keys ...string) json.RawMessage {
	keep := make(map[string]bool, len(keys))
	for _, k := range keys {
		keep[k] = true
	}
	trimList := func(list []map[string]any) []map[string]any {
		for _, obj := range list {
			for k := range obj {
				if !keep[k] {
					delete(obj, k)
				}
			}
		}
		return list
	}
	var list []map[string]any
	if json.Unmarshal(raw, &list) == nil {
		out, _ := json.Marshal(trimList(list))
		return out
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return raw
	}
	for k, v := range obj {
		var inner []map[string]any
		if json.Unmarshal(v, &inner) == nil {
			obj[k], _ = json.Marshal(trimList(inner))
		}
	}
	out, _ := json.Marshal(obj)
	return out
}

var (
	issueKeys   = []string{"id", "identifier", "title", "status", "priority", "assignee_type", "assignee_id", "project_id", "parent_issue_id", "updated_at"}
	projectKeys = []string{"id", "title", "description", "status", "priority", "lead_type", "lead_id", "updated_at"}
)

func officeTools() []tool {
	return []tool{
		{
			Name:        "whoami",
			Description: "Show this Bot's name, workspace and whether it may approve plans.",
			Schema:      schema(nil, map[string]any{}),
			Call: func(c *caller, a args) (any, error) {
				return map[string]any{
					"bot_id": c.bot.ID, "name": c.bot.Name,
					"workspace_id": c.bot.WorkspaceID, "can_release": c.bot.CanRelease,
				}, nil
			},
		},
		{
			Name:        "list_members",
			Description: "List the people (members) and member agents of the office, with ids to assign or mention.",
			Schema:      schema(nil, map[string]any{}),
			Call: func(c *caller, a args) (any, error) {
				members, err := c.do("GET", "/api/workspaces/"+c.bot.WorkspaceID+"/members", nil)
				if err != nil {
					return nil, err
				}
				agents, err := c.do("GET", "/api/agents", nil)
				if err != nil {
					return nil, err
				}
				return map[string]json.RawMessage{
					"members": trim(members, "user_id", "name", "email", "role"),
					"agents":  trim(agents, "id", "name", "description", "status", "archived_at"),
				}, nil
			},
		},
		{
			Name:        "list_projects",
			Description: "List projects.",
			Schema:      schema(nil, map[string]any{}),
			Call: func(c *caller, a args) (any, error) {
				raw, err := c.do("GET", "/api/projects", nil)
				return trim(raw, projectKeys...), err
			},
		},
		{
			Name:        "create_project",
			Description: "Create a project the boss asked for.",
			Schema: schema([]string{"title"}, map[string]any{
				"title":       strProp("Project title."),
				"description": strProp("Goal and scope in plain words."),
			}),
			Call: func(c *caller, a args) (any, error) {
				title, err := a.required("title")
				if err != nil {
					return nil, err
				}
				body := map[string]any{"title": title}
				a.optional(body, "description")
				return c.do("POST", "/api/projects", body)
			},
		},
		{
			Name: "open_topic",
			Description: "Open a topic: an issue handed to one member agent, who works out a plan " +
				"(what to do, what not to do, steps, done-when) for approval. Nothing is built before approval.",
			Schema: schema([]string{"title", "description", "agent_id"}, map[string]any{
				"title":       strProp("Topic title."),
				"description": strProp("What the boss wants and any constraints."),
				"agent_id":    strProp("Member agent who drafts the plan (see list_members)."),
				"project_id":  strProp("Project the topic belongs to."),
			}),
			Call: func(c *caller, a args) (any, error) {
				title, err := a.required("title")
				if err != nil {
					return nil, err
				}
				desc, err := a.required("description")
				if err != nil {
					return nil, err
				}
				agentID, err := a.required("agent_id")
				if err != nil {
					return nil, err
				}
				body := map[string]any{
					"title": title, "description": desc, "status": "todo",
					"assignee_type": "agent", "assignee_id": agentID,
				}
				a.optional(body, "project_id")
				return c.do("POST", "/api/issues", body)
			},
		},
		{
			Name:        "list_issues",
			Description: "List issues, optionally filtered by project or status.",
			Schema: schema(nil, map[string]any{
				"project_id": strProp("Only issues in this project."),
				"status":     strProp("Only issues in this status, e.g. todo, in_progress, in_review, done."),
				"limit":      map[string]any{"type": "integer", "description": "Max issues (default 50)."},
			}),
			Call: func(c *caller, a args) (any, error) {
				q := url.Values{}
				if v := a.str("project_id"); v != "" {
					q.Set("project_id", v)
				}
				if v := a.str("status"); v != "" {
					q.Set("status", v)
				}
				limit := a.str("limit")
				if limit == "" {
					limit = "50"
				}
				q.Set("limit", limit)
				raw, err := c.do("GET", "/api/issues?"+q.Encode(), nil)
				return trim(raw, issueKeys...), err
			},
		},
		{
			Name:        "get_issue",
			Description: "Read one issue in full.",
			Schema:      schema([]string{"issue"}, map[string]any{"issue": strProp(issueDesc)}),
			Call: func(c *caller, a args) (any, error) {
				path, err := issuePath(a, "")
				if err != nil {
					return nil, err
				}
				return c.do("GET", path, nil)
			},
		},
		{
			Name:        "list_comments",
			Description: "Read the discussion on an issue.",
			Schema:      schema([]string{"issue"}, map[string]any{"issue": strProp(issueDesc)}),
			Call: func(c *caller, a args) (any, error) {
				path, err := issuePath(a, "/comments")
				if err != nil {
					return nil, err
				}
				raw, err := c.do("GET", path, nil)
				return trim(raw, "id", "parent_id", "author_type", "author_id", "content", "type", "created_at"), err
			},
		},
		{
			Name: "add_comment",
			Description: "Comment on an issue. An @mention of an agent starts a paid run for it; " +
				"write [@Name](mention://agent/<id>) only to start real work.",
			Schema: schema([]string{"issue", "content"}, map[string]any{
				"issue":     strProp(issueDesc),
				"content":   strProp("Markdown body."),
				"parent_id": strProp("Reply under this comment."),
			}),
			Call: func(c *caller, a args) (any, error) {
				path, err := issuePath(a, "/comments")
				if err != nil {
					return nil, err
				}
				content, err := a.required("content")
				if err != nil {
					return nil, err
				}
				body := map[string]any{"content": content}
				a.optional(body, "parent_id")
				return c.do("POST", path, body)
			},
		},
		{
			Name:        "read_plan",
			Description: "Read the current plan on an issue: what to do, what not to do, steps with owners, done-when, version and approval state.",
			Schema:      schema([]string{"issue"}, map[string]any{"issue": strProp(issueDesc)}),
			Call: func(c *caller, a args) (any, error) {
				path, err := issuePath(a, "/plan")
				if err != nil {
					return nil, err
				}
				return c.do("GET", path, nil)
			},
		},
		{
			Name:        "request_approval",
			Description: "Ask the boss to approve the plan on an issue: posts your summary there and notifies the workspace owner.",
			Schema: schema([]string{"issue", "summary"}, map[string]any{
				"issue":   strProp(issueDesc),
				"summary": strProp("What will be done, what is cut, cost and risk, in plain words."),
			}),
			Call: func(c *caller, a args) (any, error) {
				path, err := issuePath(a, "/comments")
				if err != nil {
					return nil, err
				}
				summary, err := a.required("summary")
				if err != nil {
					return nil, err
				}
				members, err := c.do("GET", "/api/workspaces/"+c.bot.WorkspaceID+"/members", nil)
				if err != nil {
					return nil, err
				}
				var list []struct {
					UserID string `json:"user_id"`
					Name   string `json:"name"`
					Role   string `json:"role"`
				}
				_ = json.Unmarshal(members, &list)
				var mentions []string
				for _, m := range list {
					if m.Role == "owner" {
						mentions = append(mentions, fmt.Sprintf("[@%s](mention://member/%s)", m.Name, m.UserID))
					}
				}
				if len(mentions) == 0 {
					return nil, errors.New("the workspace has no owner to ask")
				}
				content := strings.Join(mentions, " ") + " 请批准这份方案：\n\n" + summary
				return c.do("POST", path, map[string]any{"content": content})
			},
		},
		{
			Name:        "approve_plan",
			Description: "Approve the current plan on an issue, which creates its work tickets and starts them. Only allowed when this Bot was granted release authority (see whoami).",
			Schema:      schema([]string{"issue"}, map[string]any{"issue": strProp(issueDesc)}),
			Call: func(c *caller, a args) (any, error) {
				path, err := issuePath(a, "/plan/approve")
				if err != nil {
					return nil, err
				}
				return c.do("POST", path, map[string]any{})
			},
		},
		{
			Name:        "create_issue",
			Description: "Create a work ticket. Assigning it to an agent starts that agent.",
			Schema: schema([]string{"title"}, map[string]any{
				"title":         strProp("Ticket title."),
				"description":   strProp("What to do and how to tell it is done."),
				"project_id":    strProp("Project."),
				"parent_issue":  strProp("Parent issue UUID."),
				"assignee_type": enumProp("Who does it.", "agent", "member"),
				"assignee_id":   strProp("Agent id or member user id."),
				"status":        strProp("Initial status (default todo)."),
			}),
			Call: func(c *caller, a args) (any, error) {
				title, err := a.required("title")
				if err != nil {
					return nil, err
				}
				body := map[string]any{"title": title}
				a.optional(body, "description")
				a.optional(body, "project_id")
				a.optional(body, "assignee_type")
				a.optional(body, "assignee_id")
				a.optional(body, "status")
				if v := a.str("parent_issue"); v != "" {
					body["parent_issue_id"] = v
				}
				return c.do("POST", "/api/issues", body)
			},
		},
		{
			Name:        "assign_issue",
			Description: "Assign an issue to an agent (starts it) or a member.",
			Schema: schema([]string{"issue", "assignee_type", "assignee_id"}, map[string]any{
				"issue":         strProp(issueDesc),
				"assignee_type": enumProp("Who does it.", "agent", "member"),
				"assignee_id":   strProp("Agent id or member user id."),
			}),
			Call: func(c *caller, a args) (any, error) {
				path, err := issuePath(a, "")
				if err != nil {
					return nil, err
				}
				typ, err := a.required("assignee_type")
				if err != nil {
					return nil, err
				}
				id, err := a.required("assignee_id")
				if err != nil {
					return nil, err
				}
				return c.do("PUT", path, map[string]any{"assignee_type": typ, "assignee_id": id})
			},
		},
		{
			Name:        "set_status",
			Description: "Change an issue's status (todo, in_progress, in_review, blocked, done, cancelled).",
			Schema: schema([]string{"issue", "status"}, map[string]any{
				"issue":  strProp(issueDesc),
				"status": strProp("New status."),
			}),
			Call: func(c *caller, a args) (any, error) {
				path, err := issuePath(a, "")
				if err != nil {
					return nil, err
				}
				status, err := a.required("status")
				if err != nil {
					return nil, err
				}
				return c.do("PUT", path, map[string]any{"status": status})
			},
		},
		{
			Name:        "close_issue",
			Description: "Close an issue as done, optionally leaving a closing note first.",
			Schema: schema([]string{"issue"}, map[string]any{
				"issue": strProp(issueDesc),
				"note":  strProp("Closing note posted as a comment."),
			}),
			Call: func(c *caller, a args) (any, error) {
				path, err := issuePath(a, "")
				if err != nil {
					return nil, err
				}
				if note := a.str("note"); note != "" {
					if _, err := c.do("POST", path+"/comments", map[string]any{"content": note}); err != nil {
						return nil, err
					}
				}
				return c.do("PUT", path, map[string]any{"status": "done"})
			},
		},
		{
			Name: "get_events",
			Description: "Read office events after a cursor (oldest first): issue, comment, project, plan, " +
				"member and run results, plus @mentions of this Bot. Store next_cursor and pass it back next time.",
			Schema: schema(nil, map[string]any{
				"after": map[string]any{"type": "integer", "description": "Cursor from the previous call (0 = oldest kept)."},
				"limit": map[string]any{"type": "integer", "description": "Max events (default and cap 200)."},
			}),
			Call: func(c *caller, a args) (any, error) {
				q := url.Values{}
				if v := a.str("after"); v != "" {
					q.Set("after", v)
				}
				if v := a.str("limit"); v != "" {
					q.Set("limit", v)
				}
				return c.do("GET", "/api/bot/events?"+q.Encode(), nil)
			},
		},
	}
}
