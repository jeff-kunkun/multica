package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Workspace-level events for the all-scope (DENE-1936). A person whose Bot
// subscribed with owner_visible_all hears about everything they could see in
// the workspace: PRs, runs, projects, settings, members, agent config,
// autopilots, skills, runtimes, integrations, squads, labels and routing rules,
// billing and quota — in short every write. They ride the same outbox and the
// same webhook body as the ticket/chat stream (activity.workspace block); what
// they never carry is a value that was written. A settings change names the
// fields it touched and nothing else.

const (
	ActivityWorkspaceWrite = "workspace.write"
	ActivityPRUpdated      = "pr.updated"
	ActivityPRLinked       = "pr.linked"
	ActivityPRUnlinked     = "pr.unlinked"
	ActivityRunCompleted   = "run.completed"
	ActivityRunFailed      = "run.failed"
	ActivityRunCancelled   = "run.cancelled"
	ActivityAutopilotRun   = "autopilot.run"
	// ActivityBotBlocked is a Bot write that was stopped at the door because it
	// is one of the four actions that need the owner's go-ahead first.
	ActivityBotBlocked = "bot.write_blocked"

	FamilyPR          = "pr"
	FamilyRun         = "run"
	FamilyProject     = "project"
	FamilySettings    = "settings"
	FamilyMember      = "member"
	FamilyAgent       = "agent"
	FamilyAutopilot   = "autopilot"
	FamilySkill       = "skill"
	FamilyRuntime     = "runtime"
	FamilyIntegration = "integration"
	FamilySquad       = "squad"
	FamilyRouting     = "routing"
	FamilyBilling     = "billing"
	FamilyAccess      = "access"
	FamilyIssue       = "issue"
	FamilyOther       = "other"

	// The four actions a Bot may only take after the owner said so in the Bot
	// chat (Kun's decision on DENE-1936).
	DangerDeleteWorkspace = "delete_workspace"
	DangerChangeMembers   = "change_members"
	DangerResetKeys       = "reset_keys"
	DangerChangeBilling   = "change_billing"
)

// WorkspaceEventBody is the workspace part of an activity body. Ids and names
// only.
type WorkspaceEventBody struct {
	Family           string            `json:"family"`
	Action           string            `json:"action"`
	Route            string            `json:"route,omitempty"`
	Fields           []string          `json:"fields,omitempty"`
	EntityID         string            `json:"entity_id,omitempty"`
	ProjectID        string            `json:"project_id,omitempty"` // the project the event belongs to: the send gate asks whether the person can still open it
	IssueIDs         []string          `json:"issue_ids,omitempty"`  // every ticket a PR event is linked to: the send gate asks about each
	ActorKind        string            `json:"actor_kind"`           // member | agent | bot | system
	ActorID          string            `json:"actor_id,omitempty"`
	ActorName        string            `json:"actor_name,omitempty"`
	ViaBotID         string            `json:"via_bot_id,omitempty"`
	ViaBotName       string            `json:"via_bot_name,omitempty"`
	OnBehalfOfUserID string            `json:"on_behalf_of_user_id,omitempty"`
	OnBehalfOfName   string            `json:"on_behalf_of_name,omitempty"`
	Dangerous        string            `json:"dangerous,omitempty"`
	Attrs            map[string]string `json:"attrs,omitempty"`
}

var familyLabels = map[string]string{
	FamilyPR: "PR", FamilyRun: "运行", FamilyProject: "项目", FamilySettings: "工作区设置", FamilyMember: "成员",
	FamilyAgent: "智能体", FamilyAutopilot: "autopilot", FamilySkill: "skill", FamilyRuntime: "运行环境",
	FamilyIntegration: "集成", FamilySquad: "小队", FamilyRouting: "标签/路由规则", FamilyBilling: "额度/付费",
	FamilyAccess: "密钥/Bot", FamilyIssue: "任务", FamilyOther: "工作区",
}

// workspaceFamilyRules classify a write by its route pattern (chi's pattern,
// with {placeholders}, so no id or token ever shows up). First match wins.
var workspaceFamilyRules = []struct{ sub, family string }{
	{"/cloud-billing", FamilyBilling}, {"/cloud-subscriptions", FamilyBilling}, {"/goal/budget", FamilyBilling},
	{"/pull-requests", FamilyPR}, {"/pull_requests", FamilyPR}, {"/github", FamilyPR},
	{"/vcs", FamilyIntegration}, {"/lark", FamilyIntegration}, {"/slack", FamilyIntegration},
	{"/telegram", FamilyIntegration}, {"/dingtalk", FamilyIntegration}, {"/wecom", FamilyIntegration},
	{"/plugins", FamilyIntegration}, {"/mcp-servers", FamilyIntegration}, {"/integrations", FamilyIntegration},
	{"/webhooks", FamilyIntegration}, {"/channels", FamilyIntegration},
	{"/api/tokens", FamilyAccess}, {"/cli-token", FamilyAccess}, {"/bots", FamilyAccess}, {"/rotate", FamilyAccess},
	{"/members", FamilyMember}, {"/invitations", FamilyMember}, {"/share-links", FamilyMember}, {"/leave", FamilyMember},
	{"/autopilots", FamilyAutopilot},
	{"/squads", FamilySquad},
	{"/skills", FamilySkill},
	{"/runtimes", FamilyRuntime}, {"/daemon", FamilyRuntime},
	{"/labels", FamilyRouting}, {"/routing", FamilyRouting},
	{"/agents", FamilyAgent},
	{"/projects", FamilyProject},
	{"/tasks", FamilyRun}, {"/runs", FamilyRun},
	{"/issues", FamilyIssue}, {"/comments", FamilyIssue}, {"/chat", FamilyIssue},
	{"/naming", FamilySettings}, {"/agent-spawn", FamilySettings}, {"/settings", FamilySettings},
}

// ClassifyWorkspaceRoute names the family of a write from its route pattern.
func ClassifyWorkspaceRoute(pattern string) string {
	p := strings.ToLower(pattern)
	for _, rule := range workspaceFamilyRules {
		if strings.Contains(p, rule.sub) {
			return rule.family
		}
	}
	// PATCH/PUT/DELETE straight on the workspace itself is its settings.
	if strings.HasPrefix(p, "/api/workspaces/{") && strings.Count(strings.Trim(p, "/"), "/") <= 2 {
		return FamilySettings
	}
	return FamilyOther
}

// WorkspaceWriteAction is the verb of a write, from its method.
func WorkspaceWriteAction(method string) string {
	switch strings.ToUpper(method) {
	case "POST":
		return "create"
	case "PUT", "PATCH":
		return "update"
	case "DELETE":
		return "delete"
	}
	return "act"
}

// familyMinRole is the lowest role that can see events of a family: the
// management surfaces (billing, members, keys, integrations, settings) are not
// something a plain member can open, so they are not pushed to a member's Bot.
func familyMinRole(family string) permission.Role {
	switch family {
	case FamilyBilling, FamilyMember, FamilyAccess, FamilyIntegration, FamilySettings:
		return permission.RoleAdmin
	}
	return permission.RoleMember
}

// RoleCanSeeFamily answers the visibility question for the all-scope: guests
// see no workspace events; the management families need admin or owner.
func RoleCanSeeFamily(role, family string) bool {
	r := permission.Role(role)
	switch r {
	case permission.RoleOwner, permission.RoleAdmin:
		return true
	case permission.RoleMember:
		return familyMinRole(family) == permission.RoleMember
	}
	return false
}

var (
	dangerWorkspacePath = regexp.MustCompile(`^/api/workspaces/[^/]+/?$`)
	dangerMembersPath   = regexp.MustCompile(`^/api/workspaces/[^/]+/(members|invitations|share-links|leave)(/|$)`)
	dangerKeysPath      = regexp.MustCompile(`(^/api/tokens(/|$))|(^/api/cli-token)|(/rotate([-/]|$))|(/plugins/[^/]+/token/?$)|(/reset-password/?$)`)
	dangerBillingPath   = regexp.MustCompile(`(^/api/cloud-billing(/|$))|(^/api/cloud-subscriptions(/|$))|(/goal/budget/?$)`)
)

// DangerousBotAction names which of the four confirm-first actions a request
// is, or "". It reads method and URL path only, so it can run before the
// request is routed. Reads are never dangerous.
func DangerousBotAction(method, path string) string {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "OPTIONS":
		return ""
	}
	switch {
	case strings.ToUpper(method) == "DELETE" && dangerWorkspacePath.MatchString(path):
		return DangerDeleteWorkspace
	case dangerMembersPath.MatchString(path):
		return DangerChangeMembers
	case dangerKeysPath.MatchString(path):
		return DangerResetKeys
	case dangerBillingPath.MatchString(path):
		return DangerChangeBilling
	}
	return ""
}

var fieldNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_\-]{0,63}$`)

// SafeFieldNames keeps the top-level field names of a write that are plain
// identifiers. A field called "password" is listed (it says what changed, not
// what it changed to); a name that is itself a token is data, and is dropped.
func SafeFieldNames(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !fieldNameRe.MatchString(n) || MaskSecrets(n) != n || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// WorkspaceActivityInput is one workspace-level thing that happened.
type WorkspaceActivityInput struct {
	WorkspaceID  pgtype.UUID
	IssueID      pgtype.UUID // set when the event belongs to a ticket: the ticket gate then applies
	Target       WriteTarget // the ticket, project and chat a write touched; merged with IssueID
	Visible      TargetVisibility
	ActivityType string
	DedupeKey    string
	Body         WorkspaceEventBody
	ActorType    string
	ActorID      pgtype.UUID
	Importance   string
	Categories   []string
	OccurredAt   time.Time
}

// WorkspaceWriteImportance tags a write: destructive and management writes are
// worth a look, the rest is information.
func WorkspaceWriteImportance(family, action, dangerous string) (string, []string) {
	switch {
	case dangerous != "":
		return ImportanceHigh, []string{CategoryRisk, CategoryDecision}
	case action == "delete":
		return ImportanceHigh, []string{CategoryRisk}
	case familyMinRole(family) == permission.RoleAdmin:
		return ImportanceHigh, []string{CategoryInfo}
	}
	return ImportanceNormal, []string{CategoryInfo}
}

// WorkspaceActivityExcerpt is the one line a Bot reads about a write.
func WorkspaceActivityExcerpt(b WorkspaceEventBody) string {
	who := b.ActorName
	if who == "" {
		who = b.ActorKind
	}
	if b.ViaBotID != "" {
		owner := b.OnBehalfOfName
		if owner == "" {
			owner = "主人"
		}
		who = fmt.Sprintf("Bot〈%s〉代表〈%s〉", b.ViaBotName, owner)
	}
	label := familyLabels[b.Family]
	if label == "" {
		label = b.Family
	}
	verb := map[string]string{"create": "新建/触发了", "update": "修改了", "delete": "删除了", "act": "操作了"}[b.Action]
	if verb == "" {
		verb = b.Action + " "
	}
	line := who + verb + label
	if len(b.Fields) > 0 {
		line += "（字段：" + strings.Join(b.Fields, "、") + "）"
	}
	if b.Dangerous != "" {
		line += " [需主人确认的操作：" + b.Dangerous + "]"
	}
	return ActivityExcerpt(line)
}

// RouteWorkspaceActivity writes the event for every person whose Bot asked for
// the whole workspace and whose role may see this kind of event. Like
// RouteActivity it does not trust itself for visibility: the worker decides
// again at send time.
func RouteWorkspaceActivity(ctx context.Context, q *db.Queries, in WorkspaceActivityInput) (int, error) {
	if !in.WorkspaceID.Valid || strings.TrimSpace(in.DedupeKey) == "" || in.ActivityType == "" {
		return 0, nil
	}
	bindings, err := q.ListWorkspaceActivityBindings(ctx, in.WorkspaceID)
	if err != nil || len(bindings) == 0 {
		return 0, err
	}
	occurred := in.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now()
	}
	in.Body.Fields = SafeFieldNames(in.Body.Fields)
	if in.IssueID.Valid {
		in.Target.IssueID = in.IssueID
	}
	if in.Target.ProjectID.Valid {
		in.Body.ProjectID = util.UUIDToString(in.Target.ProjectID)
	}
	if in.Body.Family == FamilyPR && !in.Target.Established(FamilyPR) {
		return 0, nil // a PR linked to no ticket has no access to judge: not told
	}
	in.Body.IssueIDs = nil
	for _, id := range in.Target.IssueIDs {
		in.Body.IssueIDs = append(in.Body.IssueIDs, util.UUIDToString(id))
	}
	body, err := marshalWorkspaceBody(in, occurred)
	if err != nil {
		return 0, err
	}
	callerType := in.ActorType
	if callerType == "" {
		callerType = "system"
	}
	sent := 0
	for _, b := range bindings {
		if in.ActorType == "member" && in.ActorID.Valid && in.ActorID == b.UserID {
			continue
		}
		if in.Body.OnBehalfOfUserID != "" && in.Body.OnBehalfOfUserID == util.UUIDToString(b.UserID) {
			continue // done in their own name, by one of their Bots: not news
		}
		if in.Body.ViaBotID != "" && in.Body.ViaBotID == util.UUIDToString(b.BotID) {
			continue
		}
		member, mErr := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: b.UserID, WorkspaceID: b.WorkspaceID})
		if mErr != nil || !RoleCanSeeFamily(member.Role, in.Body.Family) {
			continue
		}
		if in.Target.Any() && (in.Visible == nil || !in.Visible(ctx, b.WorkspaceID, b.UserID, in.Target)) {
			continue // the person could not open what this is about; no judgement means no push
		}
		channel, chErr := q.GetHumanChannel(ctx, db.GetHumanChannelParams{WorkspaceID: b.WorkspaceID, UserID: b.UserID})
		if chErr != nil || !HumanChannelUsable(channel) {
			continue
		}
		imp := body.importance
		if imp == ImportanceLow {
			backlog, countErr := q.CountQueuedActivityForBot(ctx, b.BotID)
			if countErr == nil && backlog >= activityLowBacklog {
				slog.Warn("activity stream: dropping low-importance event, Bot backlog full", "bot_id", util.UUIDToString(b.BotID))
				continue
			}
		}
		_, createErr := q.CreateHumanNeededEvent(ctx, db.CreateHumanNeededEventParams{
			WorkspaceID:   b.WorkspaceID,
			BotID:         b.BotID,
			BindingID:     b.ID,
			TargetUserID:  b.UserID,
			EventType:     HumanEventActivity,
			Kind:          in.ActivityType,
			DedupeKey:     in.DedupeKey,
			IssueID:       in.Target.IssueID,
			ChatSessionID: in.Target.ChatSessionID,
			CallerType:    callerType,
			CallerID:      in.ActorID,
			Options:       []byte("[]"),
			Urgency:       HumanUrgencyNormal,
			ExpiresAt:     pgtype.Timestamptz{Time: time.Now().Add(activityRetention), Valid: true},
			Activity:      body.raw,
		})
		if errors.Is(createErr, pgx.ErrNoRows) {
			continue
		}
		if createErr != nil {
			slog.Warn("activity stream: workspace write failed", "error", createErr, "type", in.ActivityType)
			continue
		}
		sent++
	}
	return sent, nil
}

type workspaceBodyJSON struct {
	raw        []byte
	importance string
}

func marshalWorkspaceBody(in WorkspaceActivityInput, occurred time.Time) (workspaceBodyJSON, error) {
	importance, categories := in.Importance, in.Categories
	if importance == "" {
		importance, categories = WorkspaceWriteImportance(in.Body.Family, in.Body.Action, in.Body.Dangerous)
	}
	wb := in.Body
	out, err := json.Marshal(ActivityBody{
		ActivityType: in.ActivityType, Importance: importance, Categories: categories,
		Excerpt: WorkspaceActivityExcerpt(wb), SourceID: wb.EntityID,
		OccurredAt: occurred.UTC().Format(time.RFC3339Nano),
		Workspace:  &wb,
	})
	return workspaceBodyJSON{raw: out, importance: importance}, err
}
