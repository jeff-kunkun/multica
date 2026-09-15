package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

type TransferSourceClient interface {
	GetJSON(ctx context.Context, path string, out any) error
	GetBytes(ctx context.Context, path string) ([]byte, error)
}

type TransferHTTPError struct {
	Status int
	Err    error
}

func (e *TransferHTTPError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("http %d", e.Status)
}

func (e *TransferHTTPError) Unwrap() error { return e.Err }

func transferStatus(err error) int {
	var he *TransferHTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

type TransferExportOpts struct {
	Include         []string
	ExcludeArchived bool
	People          bool
	Estimate        bool
	ClientVersion   string
	BaseURLHost     string
	WorkspaceRef    string
}

type TransferExportFiles struct {
	Manifest      TransferManifest
	Config        ConfigBundle
	People        []TransferPerson
	Runtimes      TransferRuntimesFile
	Preferences   TransferPreferences
	SessionShards [][]TransferSessionRow
	MessageShards [][]TransferMessageRow
	Attachments   []TransferAttachmentRow
	Blobs         map[string][]byte
	Secrets       []SecretOmitted
	Estimate      *TransferEstimate
}

type TransferEstimate struct {
	Sessions         int   `json:"sessions"`
	Messages         int   `json:"messages"`
	Attachments      int   `json:"attachments"`
	AttachmentBodies int   `json:"attachment_bodies"`
	EstimatedBytes   int64 `json:"estimated_bytes"`
}

func includeSet(include []string) map[string]bool {
	out := map[string]bool{}
	if len(include) == 0 {
		out["config"] = true
		out["conversations"] = true
		out["attachments"] = true
		return out
	}
	for _, p := range include {
		out[strings.TrimSpace(p)] = true
	}
	return out
}

func ExportFromSource(ctx context.Context, src TransferSourceClient, opts TransferExportOpts) (*TransferExportFiles, error) {
	inc := includeSet(opts.Include)
	out := &TransferExportFiles{Blobs: map[string][]byte{}}
	now := time.Now().UTC()

	var me struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := src.GetJSON(ctx, "/api/me", &me); err != nil {
		return nil, fmt.Errorf("fetch /api/me: %w", err)
	}

	ws, err := fetchWorkspace(ctx, src, opts.WorkspaceRef)
	if err != nil {
		return nil, err
	}

	gaps := []TransferExportGap{}
	peopleByID := map[string]TransferPerson{}
	if me.ID != "" {
		peopleByID[me.ID] = TransferPerson{SourceUserID: me.ID, Email: me.Email, Role: "owner"}
	}

	bundle := ConfigBundle{
		Format:        ConfigBundleFormat,
		SchemaVersion: ConfigBundleSchemaVersion,
		BundleID:      uuid.NewString(),
		ExportedAt:    now,
		Source: ConfigBundleSource{
			WorkspaceID: ws.ID,
			Slug:        ws.Slug,
			Name:        ws.Name,
			IssuePrefix: ws.IssuePrefix,
			ExportedBy:  me.ID,
		},
		Entities:       ConfigEntities{},
		SecretsOmitted: []SecretOmitted{},
		Stats:          map[string]int{},
	}

	if inc["config"] {
		cfgGaps := exportConfigGroups(ctx, src, ws.ID, &bundle, peopleByID)
		gaps = append(gaps, cfgGaps...)
		sanitizeBundleSecrets(&bundle)
	}

	runtimes := TransferRuntimesFile{}
	prefs := TransferPreferences{}
	if inc["config"] {
		runtimes, _ = exportRuntimeProfiles(ctx, src, ws.ID, &bundle)
		prefs = exportPinnedAgents(ctx, src)
	}

	var people []TransferPerson
	if opts.People {
		people = peopleFromMap(peopleByID)
		if err := src.GetJSON(ctx, "/api/workspaces/"+url.PathEscape(ws.ID)+"/members", &[]json.RawMessage{}); err != nil {
			st := transferStatus(err)
			if st == 0 || st >= 400 {
				// members fetch for emails
			}
		}
		members := []struct {
			UserID string `json:"user_id"`
			Email  string `json:"email"`
			Role   string `json:"role"`
		}{}
		if err := src.GetJSON(ctx, "/api/workspaces/"+url.PathEscape(ws.ID)+"/members", &members); err == nil {
			for _, m := range members {
				if m.Email == "" {
					continue
				}
				if _, ok := peopleByID[m.UserID]; !ok {
					peopleByID[m.UserID] = TransferPerson{SourceUserID: m.UserID, Email: m.Email, Role: m.Role}
				} else {
					p := peopleByID[m.UserID]
					p.Email = m.Email
					p.Role = m.Role
					peopleByID[m.UserID] = p
				}
			}
			people = peopleFromMap(peopleByID)
		}
	}

	refs := TransferRefs{
		Agents:       map[string]TransferAgentRef{},
		SystemAgents: map[string]TransferAgentRef{},
		Projects:     map[string]TransferProjRef{},
	}
	for _, a := range bundle.Entities.Agents {
		refs.Agents[a.SourceID] = TransferAgentRef{Name: a.Name}
	}
	for _, a := range bundle.Entities.SystemAgents {
		refs.SystemAgents[a.SystemKey] = TransferAgentRef{SystemKey: a.SystemKey}
	}
	for _, p := range bundle.Entities.Projects {
		refs.Projects[p.SourceID] = TransferProjRef{Title: p.Title}
	}

	if inc["conversations"] {
		sessShards, msgShards, atts, blobs, secrets, est, convGaps := exportConversations(ctx, src, opts)
		gaps = append(gaps, convGaps...)
		out.SessionShards = sessShards
		out.MessageShards = msgShards
		out.Attachments = atts
		out.Blobs = blobs
		bundle.SecretsOmitted = append(bundle.SecretsOmitted, secrets...)
		if opts.Estimate {
			out.Estimate = est
		}
		for _, shard := range sessShards {
			for _, s := range shard {
				if _, ok := refs.Agents[s.AgentID]; !ok {
					refs.Agents[s.AgentID] = TransferAgentRef{}
				}
			}
		}
	}

	out.Config = bundle
	out.People = people
	out.Runtimes = runtimes
	out.Preferences = prefs
	out.Secrets = bundle.SecretsOmitted

	stats := map[string]int{}
	for k, v := range bundle.Stats {
		stats[k] = v
	}
	sessCount, msgCount := 0, 0
	for _, s := range out.SessionShards {
		sessCount += len(s)
	}
	for _, m := range out.MessageShards {
		msgCount += len(m)
	}
	stats["chat_sessions"] = sessCount
	stats["chat_messages"] = msgCount
	stats["attachments"] = len(out.Attachments)
	bodyCount := 0
	for _, a := range out.Attachments {
		if a.BodyOmittedReason == nil {
			bodyCount++
		}
	}
	stats["attachment_bodies"] = bodyCount
	redacted := 0
	for _, s := range bundle.SecretsOmitted {
		if s.Reason == secretContentReason {
			redacted++
		}
	}
	stats["secrets_redacted_in_content"] = redacted

	includeList := opts.Include
	if len(includeList) == 0 {
		includeList = []string{"config", "conversations", "attachments"}
	}
	out.Manifest = TransferManifest{
		Format:        TransferBundleFormat,
		SchemaVersion: TransferBundleSchemaVersion,
		BundleID:      uuid.NewString(),
		ExportedAt:    now,
		Exporter:      TransferExporterInfo{Kind: "cli", Version: opts.ClientVersion},
		Source: TransferSourceInfo{
			BaseURLHost:   opts.BaseURLHost,
			ServerVersion: "unknown",
			WorkspaceID:   ws.ID,
			Slug:          ws.Slug,
			Name:          ws.Name,
			ExportedBy:    me.ID,
		},
		Options: TransferExportOptions{
			Include:              includeList,
			ExcludeArchivedChats: opts.ExcludeArchived,
			People:               opts.People,
		},
		Refs:       refs,
		ExportGaps: gaps,
		Stats:      stats,
	}
	return out, nil
}

type wsLite struct {
	ID                    string          `json:"id"`
	Name                  string          `json:"name"`
	Slug                  string          `json:"slug"`
	Context               *string         `json:"context"`
	Settings              json.RawMessage `json:"settings"`
	Repos                 json.RawMessage `json:"repos"`
	IssuePrefix           string          `json:"issue_prefix"`
	AttributionFailClosed *bool           `json:"attribution_fail_closed"`
}

func fetchWorkspace(ctx context.Context, src TransferSourceClient, ref string) (wsLite, error) {
	var list []wsLite
	if err := src.GetJSON(ctx, "/api/workspaces", &list); err != nil {
		return wsLite{}, fmt.Errorf("list workspaces: %w", err)
	}
	ref = strings.TrimSpace(ref)
	for _, w := range list {
		if w.ID == ref || w.Slug == ref {
			var full wsLite
			if err := src.GetJSON(ctx, "/api/workspaces/"+url.PathEscape(w.ID), &full); err != nil {
				return w, nil
			}
			if full.ID == "" {
				full = w
			}
			return full, nil
		}
	}
	return wsLite{}, fmt.Errorf("workspace %q not found", ref)
}

func exportConfigGroups(ctx context.Context, src TransferSourceClient, wsID string, bundle *ConfigBundle, people map[string]TransferPerson) []TransferExportGap {
	var gaps []TransferExportGap
	gap := func(group string, err error) {
		g := TransferExportGap{Group: group, Reason: "read_api_error"}
		if st := transferStatus(err); st == 404 {
			g.Reason = "read_api_missing"
			g.Status = 404
		} else if st > 0 {
			g.Status = st
		}
		gaps = append(gaps, g)
	}

	bundle.Entities.Workspace = &ConfigWorkspace{
		Settings:    rawOrEmpty(bundleSettings(src, wsID), "{}"),
		Context:     "",
		Repos:       json.RawMessage("[]"),
		IssuePrefix: "",
	}
	var ws wsLite
	if err := src.GetJSON(ctx, "/api/workspaces/"+url.PathEscape(wsID), &ws); err == nil {
		if len(ws.Settings) > 0 {
			bundle.Entities.Workspace.Settings = ws.Settings
		}
		if ws.Context != nil {
			bundle.Entities.Workspace.Context = *ws.Context
		}
		if len(ws.Repos) > 0 {
			bundle.Entities.Workspace.Repos = ws.Repos
		}
		bundle.Entities.Workspace.IssuePrefix = ws.IssuePrefix
		if ws.AttributionFailClosed != nil {
			bundle.Entities.Workspace.AttributionFailClosed = *ws.AttributionFailClosed
		}
	} else {
		gap("workspace", err)
	}

	var labels []map[string]any
	if err := getList(ctx, src, "/api/labels", &labels); err != nil {
		gap("labels", err)
	} else {
		for _, raw := range labels {
			bundle.Entities.Labels = append(bundle.Entities.Labels, ConfigLabel{
				SourceID:     strField(raw, "id"),
				ResourceType: strField(raw, "resource_type"),
				Name:         strField(raw, "name"),
				Color:        strField(raw, "color"),
				Description:  strField(raw, "description"),
			})
		}
		bundle.Stats["labels"] = len(bundle.Entities.Labels)
	}

	var statuses []map[string]any
	if err := getList(ctx, src, "/api/issue-statuses", &statuses); err != nil {
		gap("issue_statuses", err)
	} else {
		for _, raw := range statuses {
			bundle.Entities.IssueStatuses = append(bundle.Entities.IssueStatuses, ConfigIssueStatus{
				SourceID:    strField(raw, "id"),
				Key:         strField(raw, "key"),
				Name:        strField(raw, "name"),
				Description: strField(raw, "description"),
				Category:    strField(raw, "category"),
				Color:       strField(raw, "color"),
				Position:    floatField(raw, "position"),
				IsSystem:    boolField(raw, "is_system"),
			})
		}
		bundle.Stats["issue_statuses"] = len(bundle.Entities.IssueStatuses)
	}

	var props []map[string]any
	if err := getList(ctx, src, "/api/properties", &props); err != nil {
		gap("issue_properties", err)
	} else {
		for _, raw := range props {
			bundle.Entities.IssueProperties = append(bundle.Entities.IssueProperties, ConfigProperty{
				SourceID:    strField(raw, "id"),
				Name:        strField(raw, "name"),
				Type:        strField(raw, "type"),
				Description: strField(raw, "description"),
				Icon:        strField(raw, "icon"),
				Config:      rawField(raw, "config"),
				Position:    floatField(raw, "position"),
			})
		}
		bundle.Stats["issue_properties"] = len(bundle.Entities.IssueProperties)
	}

	sourceExportSkills(ctx, src, wsID, bundle, &gaps, gap)
	exportAgentsGroup(ctx, src, bundle, &gaps, gap)
	sourceExportSquads(ctx, src, bundle, people, &gaps, gap)
	sourceExportProjects(ctx, src, bundle, &gaps, gap)
	sourceExportAutopilots(ctx, src, bundle, &gaps, gap)
	sourceExportQuickActions(ctx, src, bundle, &gaps, gap)
	sourceExportIssueViews(ctx, src, bundle, &gaps, gap)
	sourceExportIntegrations(ctx, src, wsID, bundle, &gaps, gap)
	return gaps
}

func bundleSettings(_ TransferSourceClient, _ string) []byte { return []byte("{}") }

func sourceExportSkills(ctx context.Context, src TransferSourceClient, wsID string, bundle *ConfigBundle, gaps *[]TransferExportGap, gap func(string, error)) {
	pluginSkills, pluginErr := pluginContributedSkillNames(ctx, src, wsID)
	if pluginErr != nil {
		g := TransferExportGap{Group: "skills", Reason: "plugin_skills_unfiltered"}
		if st := transferStatus(pluginErr); st > 0 {
			g.Status = st
		}
		*gaps = append(*gaps, g)
		pluginSkills = nil
	}
	var skills []map[string]any
	if err := getList(ctx, src, "/api/skills", &skills); err != nil {
		gap("skills", err)
		return
	}
	for _, raw := range skills {
		name := strField(raw, "name")
		if pluginSkills[name] {
			continue
		}
		id := strField(raw, "id")
		sk := ConfigSkill{
			SourceID:    id,
			Name:        name,
			Description: strField(raw, "description"),
			Content:     strField(raw, "content"),
			Config:      rawField(raw, "config"),
		}
		var files []map[string]any
		if err := getList(ctx, src, "/api/skills/"+url.PathEscape(id)+"/files", &files); err == nil {
			for _, f := range files {
				sk.Files = append(sk.Files, ConfigSkillFile{Path: strField(f, "path"), Content: strField(f, "content")})
			}
		}
		var labs []map[string]any
		if err := getList(ctx, src, "/api/skills/"+url.PathEscape(id)+"/labels", &labs); err == nil {
			for _, l := range labs {
				sk.LabelIDs = append(sk.LabelIDs, strField(l, "id"))
			}
		}
		bundle.Entities.Skills = append(bundle.Entities.Skills, sk)
	}
	bundle.Stats["skills"] = len(bundle.Entities.Skills)
}

// pluginContributedSkillNames reads GET /api/workspaces/{id}/plugins and
// returns skill names contributed by installed plugins (resources[].type ==
// "skill", key is the workspace-unique skill name). A 503 (PluginsV1 off) or
// any other read failure is returned so the caller can export every skill and
// record plugin_skills_unfiltered.
func pluginContributedSkillNames(ctx context.Context, src TransferSourceClient, wsID string) (map[string]bool, error) {
	var plugins []map[string]any
	if err := getList(ctx, src, "/api/workspaces/"+url.PathEscape(wsID)+"/plugins", &plugins); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, raw := range plugins {
		resources, _ := raw["resources"].([]any)
		for _, r := range resources {
			m, ok := r.(map[string]any)
			if !ok {
				continue
			}
			if strField(m, "type") != "skill" {
				continue
			}
			if key := strField(m, "key"); key != "" {
				out[key] = true
			}
		}
	}
	return out, nil
}

func exportAgentsGroup(ctx context.Context, src TransferSourceClient, bundle *ConfigBundle, _ *[]TransferExportGap, gap func(string, error)) {
	var agents []map[string]any
	if err := getList(ctx, src, "/api/agents", &agents); err != nil {
		gap("agents", err)
		return
	}
	for _, raw := range agents {
		id := strField(raw, "id")
		sysKey := strField(raw, "system_key")
		if strings.HasPrefix(sysKey, "agent_builder:") {
			continue
		}
		if sysKey != "" {
			bundle.Entities.SystemAgents = append(bundle.Entities.SystemAgents, ConfigSystemAgent{
				SystemKey:             sysKey,
				Instructions:          strField(raw, "instructions"),
				Model:                 strPtrField(raw, "model"),
				ThinkingLevel:         strPtrField(raw, "thinking_level"),
				ServiceTier:           strPtrField(raw, "service_tier"),
				ConversationStarters:  rawField(raw, "conversation_starters"),
				DisabledRuntimeSkills: rawField(raw, "disabled_runtime_skills"),
			})
			continue
		}
		a := ConfigAgent{
			SourceID:              id,
			Name:                  strField(raw, "name"),
			Description:           strField(raw, "description"),
			Instructions:          strField(raw, "instructions"),
			AvatarURL:             strPtrField(raw, "avatar_url"),
			RuntimeMode:           strField(raw, "runtime_mode"),
			RuntimeConfig:         rawField(raw, "runtime_config"),
			CustomArgs:            rawField(raw, "custom_args"),
			CustomEnv:             jsonNull(),
			McpConfig:             jsonNull(),
			Model:                 strPtrField(raw, "model"),
			ThinkingLevel:         strPtrField(raw, "thinking_level"),
			ServiceTier:           strPtrField(raw, "service_tier"),
			Visibility:            strField(raw, "visibility"),
			PermissionMode:        strField(raw, "permission_mode"),
			MaxConcurrentTasks:    int32(floatField(raw, "max_concurrent_tasks")),
			ConversationStarters:  rawField(raw, "conversation_starters"),
			DisabledRuntimeSkills: rawField(raw, "disabled_runtime_skills"),
		}
		if v, ok := raw["composio_toolkit_allowlist"].([]any); ok {
			for _, x := range v {
				if s, ok := x.(string); ok {
					a.ComposioToolkitAllowlist = append(a.ComposioToolkitAllowlist, s)
				}
			}
		}
		rc, had := stripGatewayToken(a.RuntimeConfig)
		a.RuntimeConfig = rc
		if had || strings.Contains(string(a.RuntimeConfig), "***") {
			a.RuntimeConfig = stripMaskedGateway(a.RuntimeConfig)
			bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
				Entity: "agent", SourceID: id, Name: a.Name, Field: "runtime_config.gateway.token",
				Reason: secretReasonMaterial,
			})
		}
		args, masked := redactSecretArgs(a.CustomArgs)
		a.CustomArgs = args
		if masked > 0 {
			bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
				Entity: "agent", SourceID: id, Name: a.Name, Field: "custom_args",
				Reason: secretReasonMaterial, Hint: map[string]any{"value_count": masked},
			})
		}
		if boolField(raw, "has_custom_env") || floatField(raw, "custom_env_key_count") > 0 {
			bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
				Entity: "agent", SourceID: id, Name: a.Name, Field: "custom_env",
				Reason: secretReasonMaterial, Hint: map[string]any{"key_count": int(floatField(raw, "custom_env_key_count"))},
			})
		}
		if raw["mcp_config"] != nil && !isJSONNull(rawField(raw, "mcp_config")) {
			bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
				Entity: "agent", SourceID: id, Name: a.Name, Field: "mcp_config",
				Reason: secretReasonMaterial,
			})
		}
		if skills, ok := raw["skills"].([]any); ok {
			for _, s := range skills {
				if m, ok := s.(map[string]any); ok {
					a.Skills = append(a.Skills, ConfigAgentSkill{Skill: strField(m, "id"), Enabled: boolField(m, "enabled")})
				}
			}
		}
		if targets, ok := raw["invocation_targets"].([]any); ok {
			for _, t := range targets {
				if m, ok := t.(map[string]any); ok {
					it := ConfigInvocationTarget{TargetType: strField(m, "target_type")}
					if id := strField(m, "target_id"); id != "" {
						it.TargetID = &id
					}
					a.InvocationTargets = append(a.InvocationTargets, it)
				}
			}
		}
		var labs []map[string]any
		if err := getList(ctx, src, "/api/agents/"+url.PathEscape(id)+"/labels", &labs); err == nil {
			for _, l := range labs {
				a.LabelIDs = append(a.LabelIDs, strField(l, "id"))
			}
		}
		var mcps []map[string]any
		if err := getList(ctx, src, "/api/agents/"+url.PathEscape(id)+"/mcp-servers", &mcps); err == nil {
			for _, m := range mcps {
				a.McpServers = append(a.McpServers, ConfigAgentMcp{Server: strField(m, "id"), Enabled: boolField(m, "enabled")})
			}
		}
		bundle.Entities.Agents = append(bundle.Entities.Agents, a)
	}
	bundle.Stats["agents"] = len(bundle.Entities.Agents)
	bundle.Stats["system_agents"] = len(bundle.Entities.SystemAgents)
}

func stripMaskedGateway(raw json.RawMessage) json.RawMessage {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return raw
	}
	if gw, ok := obj["gateway"].(map[string]any); ok {
		if tok, ok := gw["token"].(string); ok && isMaskedSecretValue(tok) {
			gw["token"] = nil
			obj["gateway"] = gw
		}
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return b
}

func sourceExportSquads(ctx context.Context, src TransferSourceClient, bundle *ConfigBundle, _ map[string]TransferPerson, _ *[]TransferExportGap, gap func(string, error)) {
	var rows []map[string]any
	if err := getList(ctx, src, "/api/squads", &rows); err != nil {
		gap("squads", err)
		return
	}
	for _, raw := range rows {
		id := strField(raw, "id")
		sq := ConfigSquad{
			SourceID:     id,
			Name:         strField(raw, "name"),
			Description:  strField(raw, "description"),
			Instructions: strField(raw, "instructions"),
			AvatarURL:    strPtrField(raw, "avatar_url"),
			LeaderID:     strField(raw, "leader_id"),
		}
		var members []map[string]any
		if err := getList(ctx, src, "/api/squads/"+url.PathEscape(id)+"/members", &members); err == nil {
			for _, m := range members {
				role := strField(m, "role")
				if role == "leader" {
					continue
				}
				sq.Members = append(sq.Members, ConfigSquadMember{
					MemberType: strField(m, "member_type"),
					MemberID:   firstNonEmpty(strField(m, "member_id"), strField(m, "id")),
					Role:       role,
				})
			}
		}
		bundle.Entities.Squads = append(bundle.Entities.Squads, sq)
	}
	bundle.Stats["squads"] = len(bundle.Entities.Squads)
}

func sourceExportProjects(ctx context.Context, src TransferSourceClient, bundle *ConfigBundle, _ *[]TransferExportGap, gap func(string, error)) {
	var rows []map[string]any
	if err := getList(ctx, src, "/api/projects", &rows); err != nil {
		gap("projects", err)
		return
	}
	for _, raw := range rows {
		id := strField(raw, "id")
		p := ConfigProject{
			SourceID:    id,
			Title:       strField(raw, "title"),
			Description: strField(raw, "description"),
			Icon:        strPtrField(raw, "icon"),
			Status:      strField(raw, "status"),
			Priority:    strField(raw, "priority"),
		}
		if leadType := strField(raw, "lead_type"); leadType != "" {
			p.Lead = &ConfigPolymorphicRef{Type: leadType, ID: strField(raw, "lead_id")}
		}
		var resources []map[string]any
		if err := getList(ctx, src, "/api/projects/"+url.PathEscape(id)+"/resources", &resources); err == nil {
			for _, r := range resources {
				p.Resources = append(p.Resources, ConfigProjectResource{
					ResourceType: strField(r, "resource_type"),
					ResourceRef:  rawField(r, "resource_ref"),
					Label:        strPtrField(r, "label"),
					Position:     int32(floatField(r, "position")),
				})
			}
		}
		bundle.Entities.Projects = append(bundle.Entities.Projects, p)
	}
	bundle.Stats["projects"] = len(bundle.Entities.Projects)
}

func sourceExportAutopilots(ctx context.Context, src TransferSourceClient, bundle *ConfigBundle, _ *[]TransferExportGap, gap func(string, error)) {
	var rows []map[string]any
	if err := getList(ctx, src, "/api/autopilots", &rows); err != nil {
		gap("autopilots", err)
		return
	}
	for _, raw := range rows {
		id := strField(raw, "id")
		var detail map[string]any
		if err := src.GetJSON(ctx, "/api/autopilots/"+url.PathEscape(id), &detail); err == nil && len(detail) > 0 {
			raw = detail
		}
		ap := ConfigAutopilot{
			SourceID:           id,
			Title:              strField(raw, "title"),
			Description:        strField(raw, "description"),
			ExecutionMode:      strField(raw, "execution_mode"),
			IssueTitleTemplate: strPtrField(raw, "issue_title_template"),
			Status:             strField(raw, "status"),
		}
		if at := strField(raw, "assignee_type"); at != "" {
			ap.Assignee = &ConfigPolymorphicRef{Type: at, ID: strField(raw, "assignee_id")}
		}
		if pid := strField(raw, "project_id"); pid != "" {
			ap.ProjectID = &pid
		}
		if trigs, ok := raw["triggers"].([]any); ok {
			for _, t := range trigs {
				m, _ := t.(map[string]any)
				ap.Triggers = append(ap.Triggers, ConfigAutopilotTrigger{
					Kind:           strField(m, "kind"),
					Enabled:        boolField(m, "enabled"),
					CronExpression: strPtrField(m, "cron_expression"),
					Timezone:       strPtrField(m, "timezone"),
					Label:          strPtrField(m, "label"),
					Provider:       strField(m, "provider"),
					EventFilters:   rawField(m, "event_filters"),
					WebhookToken:   jsonNull(),
					SigningSecret:  jsonNull(),
				})
				if strField(m, "kind") == "webhook" {
					bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
						Entity: "autopilot_trigger", SourceID: id, Name: ap.Title, Field: "webhook_token",
						Reason: secretReasonMaterial,
					}, SecretOmitted{
						Entity: "autopilot_trigger", SourceID: id, Name: ap.Title, Field: "signing_secret",
						Reason: secretReasonMaterial,
					})
				}
			}
		}
		if subs, ok := raw["subscribers"].([]any); ok {
			for _, s := range subs {
				m, _ := s.(map[string]any)
				ap.Subscribers = append(ap.Subscribers, ConfigAutopilotPerson{UserType: strField(m, "user_type"), UserID: strField(m, "user_id")})
			}
		}
		if cols, ok := raw["collaborators"].([]any); ok {
			for _, s := range cols {
				m, _ := s.(map[string]any)
				ap.Collaborators = append(ap.Collaborators, ConfigAutopilotPerson{UserType: strField(m, "user_type"), UserID: strField(m, "user_id")})
			}
		}
		bundle.Entities.Autopilots = append(bundle.Entities.Autopilots, ap)
	}
	bundle.Stats["autopilots"] = len(bundle.Entities.Autopilots)
}

func sourceExportQuickActions(ctx context.Context, src TransferSourceClient, bundle *ConfigBundle, _ *[]TransferExportGap, gap func(string, error)) {
	var rows []map[string]any
	if err := getList(ctx, src, "/api/quick-actions", &rows); err != nil {
		gap("quick_actions", err)
		return
	}
	for _, raw := range rows {
		qa := ConfigQuickAction{
			SourceID:    strField(raw, "id"),
			Name:        strField(raw, "name"),
			Description: strField(raw, "description"),
			Prompt:      strField(raw, "prompt"),
			Visibility:  strField(raw, "visibility"),
			Status:      strField(raw, "status"),
		}
		if at := strField(raw, "assignee_type"); at != "" {
			qa.Assignee = &ConfigPolymorphicRef{Type: at, ID: strField(raw, "assignee_id")}
		}
		bundle.Entities.QuickActions = append(bundle.Entities.QuickActions, qa)
	}
	bundle.Stats["quick_actions"] = len(bundle.Entities.QuickActions)
}

func sourceExportIssueViews(ctx context.Context, src TransferSourceClient, bundle *ConfigBundle, _ *[]TransferExportGap, gap func(string, error)) {
	var rows []map[string]any
	if err := getList(ctx, src, "/api/issue-views", &rows); err != nil {
		gap("issue_views", err)
		return
	}
	for _, raw := range rows {
		if strField(raw, "visibility") != "workspace" {
			continue
		}
		bundle.Entities.IssueViews = append(bundle.Entities.IssueViews, ConfigIssueView{
			SourceID:          strField(raw, "id"),
			Name:              strField(raw, "name"),
			ScopeType:         strField(raw, "scope_type"),
			ScopeID:           strPtrField(raw, "scope_id"),
			ScopeVariant:      strPtrField(raw, "scope_variant"),
			Visibility:        strField(raw, "visibility"),
			DefinitionVersion: int32(floatField(raw, "definition_version")),
			Query:             rawField(raw, "query"),
			Display:           rawField(raw, "display"),
		})
	}
	bundle.Stats["issue_views"] = len(bundle.Entities.IssueViews)
}

func sourceExportIntegrations(ctx context.Context, src TransferSourceClient, wsID string, bundle *ConfigBundle, _ *[]TransferExportGap, gap func(string, error)) {
	base := "/api/workspaces/" + url.PathEscape(wsID)
	var mcp []map[string]any
	if err := getList(ctx, src, base+"/mcp-servers", &mcp); err != nil {
		gap("mcp_servers", err)
	} else {
		for _, raw := range mcp {
			bundle.Entities.McpServers = append(bundle.Entities.McpServers, ConfigMcpServer{
				SourceID:  strField(raw, "id"),
				Name:      strField(raw, "name"),
				Transport: strField(raw, "transport"),
			})
			bundle.Integrations = append(bundle.Integrations, ConfigIntegration{
				Kind: "mcp_server", Name: strField(raw, "name"), Transport: strField(raw, "transport"),
			})
		}
		bundle.Stats["mcp_servers"] = len(bundle.Entities.McpServers)
	}
	var gh []map[string]any
	if err := getList(ctx, src, base+"/github/installations", &gh); err == nil {
		for _, raw := range gh {
			bundle.Integrations = append(bundle.Integrations, ConfigIntegration{
				Kind: "github", AccountLogin: strField(raw, "account_login"), AccountType: strField(raw, "account_type"),
			})
		}
	}
	var vcs []map[string]any
	if err := getList(ctx, src, base+"/vcs/connections", &vcs); err == nil {
		for _, raw := range vcs {
			bundle.Integrations = append(bundle.Integrations, ConfigIntegration{
				Kind: "vcs", Provider: strField(raw, "provider"), InstanceURL: strField(raw, "instance_url"), AccountLogin: strField(raw, "account_login"),
			})
		}
	}
	var plugins []map[string]any
	if err := getList(ctx, src, base+"/plugins", &plugins); err == nil {
		for _, raw := range plugins {
			bundle.PluginsToReinstall = append(bundle.PluginsToReinstall, ConfigPlugin{
				PluginKey:     strField(raw, "plugin_key"),
				Version:       strField(raw, "version"),
				Enabled:       boolField(raw, "enabled"),
				GrantedScopes: rawField(raw, "granted_scopes"),
				Config:        rawField(raw, "config"),
			})
		}
	}
}

func exportRuntimeProfiles(ctx context.Context, src TransferSourceClient, wsID string, bundle *ConfigBundle) (TransferRuntimesFile, error) {
	out := TransferRuntimesFile{}
	var wrapped struct {
		RuntimeProfiles []map[string]any `json:"runtime_profiles"`
	}
	path := "/api/workspaces/" + url.PathEscape(wsID) + "/runtime-profiles"
	if err := src.GetJSON(ctx, path, &wrapped); err != nil {
		return out, err
	}
	for _, raw := range wrapped.RuntimeProfiles {
		args := []string{}
		if v, ok := raw["fixed_args"].([]any); ok {
			b, _ := json.Marshal(v)
			redacted, n := redactSecretArgs(b)
			_ = json.Unmarshal(redacted, &args)
			if n > 0 {
				bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
					Entity: "runtime_profile", SourceID: strField(raw, "id"), Name: strField(raw, "display_name"),
					Field: "fixed_args", Reason: secretReasonMaterial, Hint: map[string]any{"value_count": n},
				})
			}
		}
		out.Profiles = append(out.Profiles, TransferRuntimeProfile{
			SourceID:       strField(raw, "id"),
			DisplayName:    strField(raw, "display_name"),
			ProtocolFamily: strField(raw, "protocol_family"),
			CommandName:    strField(raw, "command_name"),
			Description:    strField(raw, "description"),
			FixedArgs:      args,
			Visibility:     strField(raw, "visibility"),
			Enabled:        boolField(raw, "enabled"),
		})
	}
	var runtimes []map[string]any
	if err := getList(ctx, src, "/api/runtimes", &runtimes); err == nil {
		for _, raw := range runtimes {
			name := strField(raw, "custom_name")
			if name == "" {
				name = strField(raw, "name")
			}
			out.RuntimesHint = append(out.RuntimesHint, TransferRuntimeHint{
				SourceRuntimeID: strField(raw, "id"),
				Provider:        strField(raw, "provider"),
				RuntimeMode:     strField(raw, "runtime_mode"),
				ProfileSourceID: strField(raw, "profile_id"),
				DisplayName:     name,
			})
		}
	}
	return out, nil
}

func exportPinnedAgents(ctx context.Context, src TransferSourceClient) TransferPreferences {
	var rows []map[string]any
	_ = getList(ctx, src, "/api/chat/pinned-agents", &rows)
	out := TransferPreferences{}
	for _, raw := range rows {
		out.PinnedAgents = append(out.PinnedAgents, TransferPinnedAgent{
			AgentID:  strField(raw, "agent_id"),
			Position: floatField(raw, "position"),
		})
	}
	return out
}

func exportConversations(ctx context.Context, src TransferSourceClient, opts TransferExportOpts) (
	[][]TransferSessionRow, [][]TransferMessageRow, []TransferAttachmentRow, map[string][]byte, []SecretOmitted, *TransferEstimate, []TransferExportGap,
) {
	gaps := []TransferExportGap{}
	var sessions []map[string]any
	path := "/api/chat/sessions"
	if !opts.ExcludeArchived {
		path += "?status=all"
	}
	if err := getList(ctx, src, path, &sessions); err != nil {
		gaps = append(gaps, TransferExportGap{Group: "conversations", Reason: "read_api_error", Status: transferStatus(err)})
		return nil, nil, nil, nil, nil, nil, gaps
	}

	est := &TransferEstimate{Sessions: len(sessions)}
	sessRows := []TransferSessionRow{}
	msgBySession := map[string][]TransferMessageRow{}
	atts := []TransferAttachmentRow{}
	blobs := map[string][]byte{}
	secrets := []SecretOmitted{}
	sampleBytes := int64(0)
	sampleMsgs := 0

	for _, raw := range sessions {
		id := strField(raw, "id")
		title := strField(raw, "title")
		scanned := ScanTransferContent(title)
		if scanned.Changed {
			title = scanned.Text
			secrets = append(secrets, secretOmittedFromHits("chat_session", id, "title", scanned.Hits))
		}
		row := TransferSessionRow{
			SourceID:     id,
			AgentID:      strField(raw, "agent_id"),
			Title:        title,
			Status:       strField(raw, "status"),
			IsAgentIntro: boolField(raw, "is_agent_intro"),
			CreatedAt:    strField(raw, "created_at"),
			UpdatedAt:    strField(raw, "updated_at"),
		}
		if pid := strField(raw, "project_id"); pid != "" {
			row.ProjectID = &pid
		}
		if boolField(raw, "pinned") {
			t := strField(raw, "created_at")
			row.PinnedAt = &t
		}
		if ch, ok := raw["channel_source"].(map[string]any); ok {
			ct := strField(ch, "channel_type")
			row.ChannelType = &ct
		}
		var pending map[string]any
		if err := src.GetJSON(ctx, "/api/chat/sessions/"+url.PathEscape(id)+"/pending-task", &pending); err == nil {
			if strField(pending, "task_id") != "" {
				row.HadPendingTask = true
			}
		}
		sessRows = append(sessRows, row)

		if opts.Estimate {
			page, next, err := fetchMessagePage(ctx, src, id, "", "")
			if err == nil {
				est.Messages += len(page)
				for _, m := range page {
					sampleBytes += int64(len(strField(m, "content")))
					sampleMsgs++
				}
				if next != nil {
					est.Messages += 50
				}
			}
			continue
		}

		msgs, msgSecrets, msgAtts, msgBlobs := fetchAllMessages(ctx, src, id, opts)
		msgBySession[id] = msgs
		secrets = append(secrets, msgSecrets...)
		atts = append(atts, msgAtts...)
		for k, v := range msgBlobs {
			blobs[k] = v
		}
	}

	if opts.Estimate {
		avg := int64(400)
		if sampleMsgs > 0 {
			avg = 400 + sampleBytes/int64(sampleMsgs)
		}
		est.EstimatedBytes = int64(est.Messages)*avg + int64(est.Attachments)*512*1024
		return nil, nil, nil, nil, secrets, est, gaps
	}

	sessShards, msgShards := shardConversations(sessRows, msgBySession)
	return sessShards, msgShards, atts, blobs, secrets, nil, gaps
}

func fetchAllMessages(ctx context.Context, src TransferSourceClient, sessionID string, opts TransferExportOpts) ([]TransferMessageRow, []SecretOmitted, []TransferAttachmentRow, map[string][]byte) {
	var msgs []TransferMessageRow
	var secrets []SecretOmitted
	var atts []TransferAttachmentRow
	blobs := map[string][]byte{}
	beforeCreated, beforeID := "", ""
	for {
		page, next, err := fetchMessagePage(ctx, src, sessionID, beforeCreated, beforeID)
		if err != nil {
			break
		}
		for i := len(page) - 1; i >= 0; i-- {
			raw := page[i]
			id := strField(raw, "id")
			content := strField(raw, "content")
			scanned := ScanTransferContent(content)
			if scanned.Changed {
				content = scanned.Text
				secrets = append(secrets, secretOmittedFromHits("chat_message", id, "content", scanned.Hits))
			}
			row := TransferMessageRow{
				SourceID:      id,
				ChatSessionID: sessionID,
				Role:          strField(raw, "role"),
				MessageKind:   strField(raw, "message_kind"),
				Content:       content,
				CreatedAt:     strField(raw, "created_at"),
			}
			if v := strPtrField(raw, "failure_reason"); v != nil {
				row.FailureReason = v
			}
			if n := floatField(raw, "elapsed_ms"); n != 0 {
				v := int64(n)
				row.ElapsedMs = &v
			}
			if attsRaw, ok := raw["attachments"].([]any); ok {
				for _, a := range attsRaw {
					m, _ := a.(map[string]any)
					aid := strField(m, "id")
					row.AttachmentIDs = append(row.AttachmentIDs, aid)
					if !includeSet(opts.Include)["attachments"] && len(opts.Include) > 0 {
						continue
					}
					att, blob, sec := exportOneAttachment(ctx, src, sessionID, id, m)
					atts = append(atts, att)
					secrets = append(secrets, sec...)
					if blob != nil && att.SHA256 != "" {
						blobs[att.SHA256] = blob
					}
				}
			}
			msgs = append(msgs, row)
		}
		if next == nil {
			break
		}
		beforeCreated, beforeID = next.CreatedAt, next.ID
	}
	return msgs, secrets, atts, blobs
}

type msgCursor struct {
	CreatedAt string
	ID        string
}

func fetchMessagePage(ctx context.Context, src TransferSourceClient, sessionID, beforeCreated, beforeID string) ([]map[string]any, *msgCursor, error) {
	q := url.Values{}
	q.Set("limit", "100")
	if beforeCreated != "" && beforeID != "" {
		q.Set("before_created_at", beforeCreated)
		q.Set("before_id", beforeID)
	}
	path := "/api/chat/sessions/" + url.PathEscape(sessionID) + "/messages/page?" + q.Encode()
	var page struct {
		Messages   []map[string]any `json:"messages"`
		HasMore    bool             `json:"has_more"`
		NextCursor *struct {
			CreatedAt string `json:"created_at"`
			ID        string `json:"id"`
		} `json:"next_cursor"`
	}
	if err := src.GetJSON(ctx, path, &page); err != nil {
		return nil, nil, err
	}
	var cur *msgCursor
	if page.HasMore && page.NextCursor != nil {
		cur = &msgCursor{CreatedAt: page.NextCursor.CreatedAt, ID: page.NextCursor.ID}
	}
	return page.Messages, cur, nil
}

func exportOneAttachment(ctx context.Context, src TransferSourceClient, sessionID, messageID string, raw map[string]any) (TransferAttachmentRow, []byte, []SecretOmitted) {
	id := strField(raw, "id")
	filename := strField(raw, "filename")
	ct := strField(raw, "content_type")
	size := int64(floatField(raw, "size_bytes"))
	row := TransferAttachmentRow{
		SourceID:      id,
		ChatSessionID: &sessionID,
		ChatMessageID: &messageID,
		Filename:      filename,
		ContentType:   ct,
		SizeBytes:     size,
		CreatedAt:     strField(raw, "created_at"),
	}
	exportBody, reason := shouldExportAttachmentBody(ct, filename, size)
	if !exportBody {
		row.BodyOmittedReason = &reason
		return row, nil, nil
	}
	body, err := src.GetBytes(ctx, "/api/attachments/"+url.PathEscape(id)+"/download")
	if err != nil {
		r := "attachment_body_not_exported"
		row.BodyOmittedReason = &r
		return row, nil, nil
	}
	if isTextAttachment(ct, filename) {
		scanned := ScanTransferContent(string(body))
		if scanned.Changed {
			r := "content_pattern"
			row.BodyOmittedReason = &r
			return row, nil, []SecretOmitted{secretOmittedFromHits("attachment", id, "body", scanned.Hits)}
		}
	}
	sum := sha256.Sum256(body)
	row.SHA256 = hex.EncodeToString(sum[:])
	row.Body = "attachments/blobs/" + row.SHA256
	return row, body, nil
}

func shardConversations(sessions []TransferSessionRow, msgs map[string][]TransferMessageRow) ([][]TransferSessionRow, [][]TransferMessageRow) {
	var sessShards [][]TransferSessionRow
	var msgShards [][]TransferMessageRow
	var curS []TransferSessionRow
	var curM []TransferMessageRow
	curBytes := 0
	flush := func() {
		if len(curS) == 0 && len(curM) == 0 {
			return
		}
		sessShards = append(sessShards, curS)
		msgShards = append(msgShards, curM)
		curS, curM, curBytes = nil, nil, 0
	}
	for _, s := range sessions {
		m := msgs[s.SourceID]
		raw, _ := json.Marshal(m)
		need := len(raw)
		if (len(curM)+len(m) > TransferMessageShardMaxRows || curBytes+need > TransferMessageShardMaxBytes) && len(curM) > 0 {
			flush()
		}
		if need > TransferMessageShardMaxBytes && len(m) > 1 {
			flush()
			start := 0
			for start < len(m) {
				end := start + 1
				b, _ := json.Marshal(m[start:end])
				for end < len(m) {
					nb, _ := json.Marshal(m[start : end+1])
					if len(nb) > TransferMessageShardMaxBytes {
						break
					}
					b = nb
					end++
				}
				_ = b
				sessShards = append(sessShards, []TransferSessionRow{s})
				msgShards = append(msgShards, m[start:end])
				start = end
			}
			continue
		}
		curS = append(curS, s)
		curM = append(curM, m...)
		curBytes += need
	}
	flush()
	return sessShards, msgShards
}

func getList(ctx context.Context, src TransferSourceClient, path string, dest *[]map[string]any) error {
	var raw json.RawMessage
	if err := src.GetJSON(ctx, path, &raw); err != nil {
		return err
	}
	if len(raw) == 0 || string(raw) == "null" {
		*dest = nil
		return nil
	}
	if raw[0] == '[' {
		return json.Unmarshal(raw, dest)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	for _, key := range []string{"items", "data", "labels", "agents", "skills", "squads", "projects", "autopilots", "quick_actions", "issue_views", "issue_statuses", "properties", "members", "mcp_servers", "runtime_profiles", "runtimes", "plugins", "installations", "connections", "messages", "resources", "files"} {
		if v, ok := obj[key]; ok && len(v) > 0 && v[0] == '[' {
			return json.Unmarshal(v, dest)
		}
	}
	*dest = nil
	return nil
}

func strField(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	switch v := m[k].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

func strPtrField(m map[string]any, k string) *string {
	if m == nil {
		return nil
	}
	v, ok := m[k]
	if !ok || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	return &s
}

func boolField(m map[string]any, k string) bool {
	if m == nil {
		return false
	}
	v, ok := m[k].(bool)
	return ok && v
}

func floatField(m map[string]any, k string) float64 {
	if m == nil {
		return 0
	}
	switch v := m[k].(type) {
	case float64:
		return v
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func rawField(m map[string]any, k string) json.RawMessage {
	if m == nil {
		return nil
	}
	v, ok := m[k]
	if !ok || v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func isJSONNull(b json.RawMessage) bool {
	return len(b) == 0 || string(b) == "null"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func peopleFromMap(m map[string]TransferPerson) []TransferPerson {
	out := make([]TransferPerson, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	return out
}
