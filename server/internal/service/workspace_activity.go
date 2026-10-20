package service

import (
	"regexp"
	"sort"
	"strings"
)

// Route families and the four confirm-first actions of a Bot write
// (DENE-1936). The bot-write audit and the owner's confirmation read them; the
// workspace activity stream that first used them is retired.

const (
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

// workspaceFamilyRules classify a write by its route pattern (chi's pattern,
// with {placeholders}, so no id or token ever shows up). First match wins.
var workspaceFamilyRules = []struct{ sub, family string }{
	{"/cloud-billing", FamilyBilling}, {"/cloud-subscriptions", FamilyBilling},
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

var (
	dangerWorkspacePath = regexp.MustCompile(`^/api/workspaces/[^/]+/?$`)
	dangerMembersPath   = regexp.MustCompile(`^/api/workspaces/[^/]+/(members|invitations|share-links|leave)(/|$)`)
	dangerKeysPath      = regexp.MustCompile(`(^/api/tokens(/|$))|(^/api/cli-token)|(/rotate([-/]|$))|(/plugins/[^/]+/token/?$)|(/reset-password/?$)`)
	dangerBillingPath   = regexp.MustCompile(`(^/api/cloud-billing(/|$))|(^/api/cloud-subscriptions(/|$))`)
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
