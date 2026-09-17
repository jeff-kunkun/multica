package service

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type ConfigExportOptions struct {
	WorkspaceID     pgtype.UUID
	ExportedBy      string
	ServerVersion   string
	Include         []string
	IncludeArchived bool
}

func ExportWorkspaceConfig(ctx context.Context, q *db.Queries, opts ConfigExportOptions) (*ConfigBundle, error) {
	ws, err := q.GetWorkspace(ctx, opts.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("get workspace: %w", err)
	}

	want := map[string]bool{}
	if len(opts.Include) == 0 {
		for _, t := range configEntityTypes {
			want[t] = true
		}
	} else {
		for _, t := range opts.Include {
			if !ValidEntityType(t) {
				return nil, &ImportError{Status: 400, Code: "invalid_include", Msg: "unknown entity type: " + t}
			}
			want[t] = true
		}
	}

	bundle := &ConfigBundle{
		Format:        ConfigBundleFormat,
		SchemaVersion: ConfigBundleSchemaVersion,
		BundleID:      newBundleID(),
		ExportedAt:    time.Now().UTC(),
		Source: ConfigBundleSource{
			WorkspaceID:   uuidString(ws.ID),
			Slug:          ws.Slug,
			Name:          ws.Name,
			IssuePrefix:   ws.IssuePrefix,
			ServerVersion: opts.ServerVersion,
			ExportedBy:    opts.ExportedBy,
		},
		Options:            ConfigBundleOptions{IncludeArchived: opts.IncludeArchived},
		Integrations:       []ConfigIntegration{},
		PluginsToReinstall: []ConfigPlugin{},
		SecretsOmitted:     []SecretOmitted{},
		Stats:              map[string]int{},
	}

	if want["workspace"] {
		bundle.Entities.Workspace = &ConfigWorkspace{
			Settings:              rawOrEmpty(ws.Settings, "{}"),
			Context:               textOrEmpty(ws.Context),
			Repos:                 rawOrEmpty(ws.Repos, "[]"),
			IssuePrefix:           ws.IssuePrefix,
			AttributionFailClosed: ws.AttributionFailClosed,
		}
	}

	if want["labels"] {
		if err := exportLabels(ctx, q, opts.WorkspaceID, bundle); err != nil {
			return nil, err
		}
	}
	if want["issue_statuses"] {
		if err := exportIssueStatuses(ctx, q, opts, bundle); err != nil {
			return nil, err
		}
	}
	if want["issue_properties"] {
		if err := exportIssueProperties(ctx, q, opts, bundle); err != nil {
			return nil, err
		}
	}
	if want["skills"] {
		if err := exportSkills(ctx, q, opts.WorkspaceID, bundle); err != nil {
			return nil, err
		}
	}
	if want["mcp_servers"] {
		if err := exportMcpServers(ctx, q, opts.WorkspaceID, bundle); err != nil {
			return nil, err
		}
	}
	if want["agents"] {
		if err := exportAgents(ctx, q, opts, bundle); err != nil {
			return nil, err
		}
	}
	if want["system_agents"] {
		if err := exportSystemAgents(ctx, q, opts, bundle); err != nil {
			return nil, err
		}
	}
	if want["squads"] {
		if err := exportSquads(ctx, q, opts, bundle); err != nil {
			return nil, err
		}
	}
	if want["projects"] {
		if err := exportProjects(ctx, q, opts, bundle); err != nil {
			return nil, err
		}
	}
	if want["autopilots"] {
		if err := exportAutopilots(ctx, q, opts, bundle); err != nil {
			return nil, err
		}
	}
	if want["quick_actions"] {
		if err := exportQuickActions(ctx, q, opts, bundle); err != nil {
			return nil, err
		}
	}
	if want["issue_views"] {
		if err := exportIssueViews(ctx, q, opts.WorkspaceID, bundle); err != nil {
			return nil, err
		}
	}

	if err := exportIntegrations(ctx, q, opts.WorkspaceID, bundle); err != nil {
		return nil, err
	}

	sanitizeBundleSecrets(bundle)
	return bundle, nil
}

func textOrEmpty(t pgtype.Text) string {
	if t.Valid {
		return t.String
	}
	return ""
}

func exportLabels(ctx context.Context, q *db.Queries, wsID pgtype.UUID, bundle *ConfigBundle) error {
	rows, err := q.ExportLabels(ctx, wsID)
	if err != nil {
		return fmt.Errorf("export labels: %w", err)
	}
	out := make([]ConfigLabel, 0, len(rows))
	for _, r := range rows {
		out = append(out, ConfigLabel{
			SourceID:     uuidString(r.ID),
			ResourceType: r.ResourceType,
			Name:         r.Name,
			Color:        r.Color,
			Description:  r.Description,
		})
	}
	bundle.Entities.Labels = out
	bundle.Stats["labels"] = len(out)
	return nil
}

func exportIssueStatuses(ctx context.Context, q *db.Queries, opts ConfigExportOptions, bundle *ConfigBundle) error {
	rows, err := q.ExportIssueStatuses(ctx, db.ExportIssueStatusesParams{
		WorkspaceID:     opts.WorkspaceID,
		IncludeArchived: opts.IncludeArchived,
	})
	if err != nil {
		return fmt.Errorf("export issue statuses: %w", err)
	}
	out := make([]ConfigIssueStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, ConfigIssueStatus{
			SourceID:    uuidString(r.ID),
			Key:         r.Key,
			Name:        r.Name,
			Description: r.Description,
			Category:    r.Category,
			Color:       r.Color,
			Position:    r.Position,
			IsSystem:    r.IsSystem,
			Archived:    r.Archived,
		})
	}
	bundle.Entities.IssueStatuses = out
	bundle.Stats["issue_statuses"] = len(out)
	return nil
}

func exportIssueProperties(ctx context.Context, q *db.Queries, opts ConfigExportOptions, bundle *ConfigBundle) error {
	rows, err := q.ExportIssueProperties(ctx, db.ExportIssuePropertiesParams{
		WorkspaceID:     opts.WorkspaceID,
		IncludeArchived: opts.IncludeArchived,
	})
	if err != nil {
		return fmt.Errorf("export issue properties: %w", err)
	}
	out := make([]ConfigProperty, 0, len(rows))
	for _, r := range rows {
		out = append(out, ConfigProperty{
			SourceID:    uuidString(r.ID),
			Name:        r.Name,
			Type:        r.Type,
			Description: r.Description,
			Icon:        r.Icon,
			Config:      rawOrEmpty(r.Config, "{}"),
			Position:    r.Position,
			Archived:    r.Archived,
		})
	}
	bundle.Entities.IssueProperties = out
	bundle.Stats["issue_properties"] = len(out)
	return nil
}

func exportSkills(ctx context.Context, q *db.Queries, wsID pgtype.UUID, bundle *ConfigBundle) error {
	rows, err := q.ExportSkills(ctx, wsID)
	if err != nil {
		return fmt.Errorf("export skills: %w", err)
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	filesBy := map[string][]ConfigSkillFile{}
	labelsBy := map[string][]string{}
	if len(ids) > 0 {
		files, err := q.ExportSkillFiles(ctx, ids)
		if err != nil {
			return fmt.Errorf("export skill files: %w", err)
		}
		for _, f := range files {
			sid := uuidString(f.SkillID)
			filesBy[sid] = append(filesBy[sid], ConfigSkillFile{Path: f.Path, Content: f.Content})
		}
		labs, err := q.ExportSkillLabelIDs(ctx, ids)
		if err != nil {
			return fmt.Errorf("export skill labels: %w", err)
		}
		for _, l := range labs {
			sid := uuidString(l.SkillID)
			labelsBy[sid] = append(labelsBy[sid], uuidString(l.LabelID))
		}
	}
	out := make([]ConfigSkill, 0, len(rows))
	for _, r := range rows {
		sid := uuidString(r.ID)
		files := filesBy[sid]
		if files == nil {
			files = []ConfigSkillFile{}
		}
		labs := labelsBy[sid]
		if labs == nil {
			labs = []string{}
		}
		out = append(out, ConfigSkill{
			SourceID:    sid,
			Name:        r.Name,
			Description: r.Description,
			Content:     r.Content,
			Config:      rawOrEmpty(r.Config, "{}"),
			LabelIDs:    labs,
			Files:       files,
		})
	}
	bundle.Entities.Skills = out
	bundle.Stats["skills"] = len(out)
	return nil
}

func exportMcpServers(ctx context.Context, q *db.Queries, wsID pgtype.UUID, bundle *ConfigBundle) error {
	rows, err := q.ExportMcpServers(ctx, wsID)
	if err != nil {
		return fmt.Errorf("export mcp servers: %w", err)
	}
	out := make([]ConfigMcpServer, 0, len(rows))
	for _, r := range rows {
		transport := mcpTransportFromFlags(r.ConfigType, r.HasCommand, r.HasUrl)
		out = append(out, ConfigMcpServer{
			SourceID:  uuidString(r.ID),
			Name:      r.Name,
			Transport: transport,
		})
		bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
			Entity:   "workspace_mcp_server",
			SourceID: uuidString(r.ID),
			Name:     r.Name,
			Field:    "config",
			Reason:   secretReasonWriteOnly,
			Hint:     map[string]any{"transport": transport},
		})
		bundle.Integrations = append(bundle.Integrations, ConfigIntegration{
			Kind:      "workspace_mcp_server",
			Name:      r.Name,
			Transport: transport,
		})
	}
	bundle.Entities.McpServers = out
	bundle.Stats["mcp_servers"] = len(out)
	return nil
}

func exportAgents(ctx context.Context, q *db.Queries, opts ConfigExportOptions, bundle *ConfigBundle) error {
	rows, err := q.ExportUserAgents(ctx, db.ExportUserAgentsParams{
		WorkspaceID:     opts.WorkspaceID,
		IncludeArchived: opts.IncludeArchived,
	})
	if err != nil {
		return fmt.Errorf("export agents: %w", err)
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	skillsBy := map[string][]ConfigAgentSkill{}
	labelsBy := map[string][]string{}
	mcpBy := map[string][]ConfigAgentMcp{}
	invBy := map[string][]ConfigInvocationTarget{}
	if len(ids) > 0 {
		sk, err := q.ExportAgentSkillBindings(ctx, ids)
		if err != nil {
			return fmt.Errorf("export agent skills: %w", err)
		}
		for _, s := range sk {
			aid := uuidString(s.AgentID)
			skillsBy[aid] = append(skillsBy[aid], ConfigAgentSkill{Skill: uuidString(s.SkillID), Enabled: s.Enabled})
		}
		labs, err := q.ExportAgentLabelIDs(ctx, ids)
		if err != nil {
			return fmt.Errorf("export agent labels: %w", err)
		}
		for _, l := range labs {
			aid := uuidString(l.AgentID)
			labelsBy[aid] = append(labelsBy[aid], uuidString(l.LabelID))
		}
		mcps, err := q.ExportAgentMcpBindings(ctx, ids)
		if err != nil {
			return fmt.Errorf("export agent mcp: %w", err)
		}
		for _, m := range mcps {
			aid := uuidString(m.AgentID)
			mcpBy[aid] = append(mcpBy[aid], ConfigAgentMcp{Server: uuidString(m.ServerID), Enabled: m.Enabled})
		}
		invs, err := q.ListAgentInvocationTargetsByAgentIDs(ctx, ids)
		if err != nil {
			return fmt.Errorf("export invocation targets: %w", err)
		}
		for _, inv := range invs {
			aid := uuidString(inv.AgentID)
			var tid *string
			if inv.TargetType != "workspace" {
				s := uuidString(inv.TargetID)
				tid = &s
			}
			invBy[aid] = append(invBy[aid], ConfigInvocationTarget{TargetType: inv.TargetType, TargetID: tid})
		}
	}
	out := make([]ConfigAgent, 0, len(rows))
	for _, r := range rows {
		aid := uuidString(r.ID)
		rc, hadToken := stripGatewayToken(r.RuntimeConfig)
		customArgs, maskedArgs := redactSecretArgs(r.CustomArgs)
		if maskedArgs > 0 {
			bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
				Entity: "agent", SourceID: aid, Name: r.Name, Field: "custom_args",
				Reason: secretReasonMaterial, Hint: map[string]any{"value_count": maskedArgs},
			})
		}
		if r.CustomEnvKeyCount > 0 {
			bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
				Entity: "agent", SourceID: aid, Name: r.Name, Field: "custom_env",
				Reason: secretReasonMaterial, Hint: map[string]any{"key_count": r.CustomEnvKeyCount},
			})
		}
		if r.HasMcpConfig.Valid && r.HasMcpConfig.Bool {
			bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
				Entity: "agent", SourceID: aid, Name: r.Name, Field: "mcp_config",
				Reason: secretReasonMaterial,
			})
		}
		if hadToken || (r.HasGatewayToken.Valid && r.HasGatewayToken.Bool) {
			bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
				Entity: "agent", SourceID: aid, Name: r.Name, Field: "runtime_config.gateway.token",
				Reason: secretReasonMaterial,
			})
		}
		sk := skillsBy[aid]
		if sk == nil {
			sk = []ConfigAgentSkill{}
		}
		labs := labelsBy[aid]
		if labs == nil {
			labs = []string{}
		}
		mc := mcpBy[aid]
		if mc == nil {
			mc = []ConfigAgentMcp{}
		}
		inv := invBy[aid]
		if inv == nil {
			inv = []ConfigInvocationTarget{}
		}
		allow := r.ComposioToolkitAllowlist
		if allow == nil {
			allow = []string{}
		}
		out = append(out, ConfigAgent{
			SourceID:                 aid,
			Name:                     r.Name,
			Description:              r.Description,
			Instructions:             r.Instructions,
			AvatarURL:                textPtr(r.AvatarUrl),
			RuntimeMode:              r.RuntimeMode,
			RuntimeConfig:            rc,
			CustomArgs:               rawOrEmpty(customArgs, "[]"),
			CustomEnv:                jsonNull(),
			McpConfig:                jsonNull(),
			Model:                    textPtr(r.Model),
			ThinkingLevel:            textPtr(r.ThinkingLevel),
			ServiceTier:              textPtr(r.ServiceTier),
			Visibility:               r.Visibility,
			PermissionMode:           r.PermissionMode,
			MaxConcurrentTasks:       r.MaxConcurrentTasks,
			ConversationStarters:     rawOrEmpty(r.ConversationStarters, "[]"),
			DisabledRuntimeSkills:    rawOrEmpty(r.DisabledRuntimeSkills, "[]"),
			ComposioToolkitAllowlist: allow,
			Skills:                   sk,
			LabelIDs:                 labs,
			McpServers:               mc,
			InvocationTargets:        inv,
			Archived:                 r.Archived,
		})
	}
	bundle.Entities.Agents = out
	bundle.Stats["agents"] = len(out)
	return nil
}

func exportSystemAgents(ctx context.Context, q *db.Queries, opts ConfigExportOptions, bundle *ConfigBundle) error {
	rows, err := q.ExportSystemAgents(ctx, db.ExportSystemAgentsParams{
		WorkspaceID:     opts.WorkspaceID,
		IncludeArchived: opts.IncludeArchived,
	})
	if err != nil {
		return fmt.Errorf("export system agents: %w", err)
	}
	out := make([]ConfigSystemAgent, 0, len(rows))
	for _, r := range rows {
		out = append(out, ConfigSystemAgent{
			SourceID:              uuidString(r.ID),
			SystemKey:             textOrEmpty(r.SystemKey),
			Instructions:          r.Instructions,
			Model:                 textPtr(r.Model),
			ThinkingLevel:         textPtr(r.ThinkingLevel),
			ServiceTier:           textPtr(r.ServiceTier),
			ConversationStarters:  rawOrEmpty(r.ConversationStarters, "[]"),
			DisabledRuntimeSkills: rawOrEmpty(r.DisabledRuntimeSkills, "[]"),
		})
	}
	bundle.Entities.SystemAgents = out
	bundle.Stats["system_agents"] = len(out)
	return nil
}

func exportSquads(ctx context.Context, q *db.Queries, opts ConfigExportOptions, bundle *ConfigBundle) error {
	rows, err := q.ExportSquads(ctx, db.ExportSquadsParams{
		WorkspaceID:     opts.WorkspaceID,
		IncludeArchived: opts.IncludeArchived,
	})
	if err != nil {
		return fmt.Errorf("export squads: %w", err)
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	membersBy := map[string][]ConfigSquadMember{}
	if len(ids) > 0 {
		ms, err := q.ExportSquadMembers(ctx, ids)
		if err != nil {
			return fmt.Errorf("export squad members: %w", err)
		}
		for _, m := range ms {
			if m.Role == "leader" {
				continue
			}
			sid := uuidString(m.SquadID)
			membersBy[sid] = append(membersBy[sid], ConfigSquadMember{
				MemberType: m.MemberType,
				MemberID:   uuidString(m.MemberID),
				Role:       m.Role,
			})
		}
	}
	out := make([]ConfigSquad, 0, len(rows))
	for _, r := range rows {
		sid := uuidString(r.ID)
		mem := membersBy[sid]
		if mem == nil {
			mem = []ConfigSquadMember{}
		}
		out = append(out, ConfigSquad{
			SourceID:     sid,
			Name:         r.Name,
			Description:  r.Description,
			Instructions: r.Instructions,
			AvatarURL:    textPtr(r.AvatarUrl),
			LeaderID:     uuidString(r.LeaderID),
			Members:      mem,
			Archived:     r.Archived,
		})
	}
	bundle.Entities.Squads = out
	bundle.Stats["squads"] = len(out)
	return nil
}

func exportProjects(ctx context.Context, q *db.Queries, opts ConfigExportOptions, bundle *ConfigBundle) error {
	rows, err := q.ExportProjects(ctx, db.ExportProjectsParams{
		WorkspaceID:     opts.WorkspaceID,
		IncludeArchived: opts.IncludeArchived,
	})
	if err != nil {
		return fmt.Errorf("export projects: %w", err)
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	resBy := map[string][]ConfigProjectResource{}
	if len(ids) > 0 {
		rs, err := q.ExportProjectResources(ctx, ids)
		if err != nil {
			return fmt.Errorf("export project resources: %w", err)
		}
		for _, r := range rs {
			pid := uuidString(r.ProjectID)
			resBy[pid] = append(resBy[pid], ConfigProjectResource{
				ResourceType: r.ResourceType,
				ResourceRef:  rawOrEmpty(r.ResourceRef, "{}"),
				Label:        textPtr(r.Label),
				Position:     r.Position,
			})
		}
	}
	out := make([]ConfigProject, 0, len(rows))
	for _, r := range rows {
		pid := uuidString(r.ID)
		res := resBy[pid]
		if res == nil {
			res = []ConfigProjectResource{}
		}
		var lead *ConfigPolymorphicRef
		if r.LeadType.Valid && r.LeadID.Valid {
			lead = &ConfigPolymorphicRef{Type: r.LeadType.String, ID: uuidString(r.LeadID)}
		}
		out = append(out, ConfigProject{
			SourceID:    pid,
			Title:       r.Title,
			Description: textOrEmpty(r.Description),
			Icon:        textPtr(r.Icon),
			Status:      r.Status,
			Priority:    r.Priority,
			Lead:        lead,
			StartDate:   datePtr(r.StartDate),
			DueDate:     datePtr(r.DueDate),
			Resources:   res,
		})
	}
	bundle.Entities.Projects = out
	bundle.Stats["projects"] = len(out)
	return nil
}

func datePtr(d pgtype.Date) *string {
	if !d.Valid {
		return nil
	}
	s := d.Time.Format("2006-01-02")
	return &s
}

func exportAutopilots(ctx context.Context, q *db.Queries, opts ConfigExportOptions, bundle *ConfigBundle) error {
	rows, err := q.ExportAutopilots(ctx, db.ExportAutopilotsParams{
		WorkspaceID:     opts.WorkspaceID,
		IncludeArchived: opts.IncludeArchived,
	})
	if err != nil {
		return fmt.Errorf("export autopilots: %w", err)
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	trBy := map[string][]ConfigAutopilotTrigger{}
	subBy := map[string][]ConfigAutopilotPerson{}
	colBy := map[string][]ConfigAutopilotPerson{}
	if len(ids) > 0 {
		trs, err := q.ExportAutopilotTriggers(ctx, ids)
		if err != nil {
			return fmt.Errorf("export autopilot triggers: %w", err)
		}
		for _, t := range trs {
			aid := uuidString(t.AutopilotID)
			trig := ConfigAutopilotTrigger{
				Kind:           t.Kind,
				Enabled:        t.Enabled,
				CronExpression: textPtr(t.CronExpression),
				Timezone:       textPtr(t.Timezone),
				Label:          textPtr(t.Label),
				Provider:       t.Provider,
				EventFilters:   rawOrNull(t.EventFilters),
				WebhookToken:   jsonNull(),
				SigningSecret:  jsonNull(),
			}
			trBy[aid] = append(trBy[aid], trig)
			if t.HasWebhookToken.Valid && t.HasWebhookToken.Bool {
				name := triggerOmittedName(rows, t)
				bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
					Entity: "autopilot_trigger", SourceID: aid, Name: name,
					Field: "webhook_token", Reason: secretReasonMaterial,
				})
			}
			if t.HasSigningSecret.Valid && t.HasSigningSecret.Bool {
				name := triggerOmittedName(rows, t)
				bundle.SecretsOmitted = append(bundle.SecretsOmitted, SecretOmitted{
					Entity: "autopilot_trigger", SourceID: aid, Name: name,
					Field: "signing_secret", Reason: secretReasonMaterial,
				})
			}
		}
		subs, err := q.ExportAutopilotSubscribers(ctx, ids)
		if err != nil {
			return fmt.Errorf("export autopilot subscribers: %w", err)
		}
		for _, s := range subs {
			aid := uuidString(s.AutopilotID)
			subBy[aid] = append(subBy[aid], ConfigAutopilotPerson{UserType: s.UserType, UserID: uuidString(s.UserID)})
		}
		cols, err := q.ExportAutopilotCollaborators(ctx, ids)
		if err != nil {
			return fmt.Errorf("export autopilot collaborators: %w", err)
		}
		for _, c := range cols {
			aid := uuidString(c.AutopilotID)
			colBy[aid] = append(colBy[aid], ConfigAutopilotPerson{UserType: c.UserType, UserID: uuidString(c.UserID)})
		}
	}
	out := make([]ConfigAutopilot, 0, len(rows))
	for _, r := range rows {
		aid := uuidString(r.ID)
		tr := trBy[aid]
		if tr == nil {
			tr = []ConfigAutopilotTrigger{}
		}
		sub := subBy[aid]
		if sub == nil {
			sub = []ConfigAutopilotPerson{}
		}
		col := colBy[aid]
		if col == nil {
			col = []ConfigAutopilotPerson{}
		}
		var projectID *string
		if r.ProjectID.Valid {
			s := uuidString(r.ProjectID)
			projectID = &s
		}
		out = append(out, ConfigAutopilot{
			SourceID:           aid,
			Title:              r.Title,
			Description:        textOrEmpty(r.Description),
			Assignee:           &ConfigPolymorphicRef{Type: r.AssigneeType, ID: uuidString(r.AssigneeID)},
			ProjectID:          projectID,
			ExecutionMode:      r.ExecutionMode,
			IssueTitleTemplate: textPtr(r.IssueTitleTemplate),
			Status:             r.Status,
			Triggers:           tr,
			Subscribers:        sub,
			Collaborators:      col,
		})
	}
	bundle.Entities.Autopilots = out
	bundle.Stats["autopilots"] = len(out)
	return nil
}

func triggerOmittedName(rows []db.ExportAutopilotsRow, t db.ExportAutopilotTriggersRow) string {
	title := uuidString(t.AutopilotID)
	for _, r := range rows {
		if r.ID == t.AutopilotID {
			title = r.Title
			break
		}
	}
	if t.Label.Valid && t.Label.String != "" {
		return title + " / " + t.Label.String
	}
	return title
}

func exportQuickActions(ctx context.Context, q *db.Queries, opts ConfigExportOptions, bundle *ConfigBundle) error {
	rows, err := q.ExportQuickActions(ctx, db.ExportQuickActionsParams{
		WorkspaceID:     opts.WorkspaceID,
		IncludeArchived: opts.IncludeArchived,
	})
	if err != nil {
		return fmt.Errorf("export quick actions: %w", err)
	}
	out := make([]ConfigQuickAction, 0, len(rows))
	for _, r := range rows {
		if r.Visibility != "public" && uuidString(r.CreatedByID) != opts.ExportedBy {
			continue
		}
		out = append(out, ConfigQuickAction{
			SourceID:    uuidString(r.ID),
			Name:        r.Name,
			Description: r.Description,
			Assignee:    &ConfigPolymorphicRef{Type: r.AssigneeType, ID: uuidString(r.AssigneeID)},
			Prompt:      r.Prompt,
			Visibility:  r.Visibility,
			Status:      r.Status,
		})
	}
	bundle.Entities.QuickActions = out
	bundle.Stats["quick_actions"] = len(out)
	return nil
}

func exportIssueViews(ctx context.Context, q *db.Queries, wsID pgtype.UUID, bundle *ConfigBundle) error {
	rows, err := q.ExportIssueViews(ctx, wsID)
	if err != nil {
		return fmt.Errorf("export issue views: %w", err)
	}
	out := make([]ConfigIssueView, 0, len(rows))
	for _, r := range rows {
		var scopeID *string
		if r.ScopeID.Valid {
			s := uuidString(r.ScopeID)
			scopeID = &s
		}
		out = append(out, ConfigIssueView{
			SourceID:          uuidString(r.ID),
			Name:              r.Name,
			ScopeType:         r.ScopeType,
			ScopeID:           scopeID,
			ScopeVariant:      textPtr(r.ScopeVariant),
			Visibility:        r.Visibility,
			DefinitionVersion: r.DefinitionVersion,
			Query:             rawOrEmpty(r.Query, "{}"),
			Display:           rawOrEmpty(r.Display, "{}"),
		})
	}
	bundle.Entities.IssueViews = out
	bundle.Stats["issue_views"] = len(out)
	return nil
}

func exportIntegrations(ctx context.Context, q *db.Queries, wsID pgtype.UUID, bundle *ConfigBundle) error {
	ghs, err := q.ExportGitHubInstallations(ctx, wsID)
	if err != nil {
		return fmt.Errorf("export github installations: %w", err)
	}
	for _, g := range ghs {
		bundle.Integrations = append(bundle.Integrations, ConfigIntegration{
			Kind: "github_installation", AccountLogin: g.AccountLogin, AccountType: g.AccountType,
		})
	}
	vcs, err := q.ExportVCSConnections(ctx, wsID)
	if err != nil {
		return fmt.Errorf("export vcs connections: %w", err)
	}
	for _, v := range vcs {
		bundle.Integrations = append(bundle.Integrations, ConfigIntegration{
			Kind: "vcs_connection", Provider: v.Provider, InstanceURL: v.InstanceUrl, AccountLogin: v.AccountLogin,
		})
	}
	chs, err := q.ExportChannelInstallations(ctx, wsID)
	if err != nil {
		return fmt.Errorf("export channel installations: %w", err)
	}
	for _, c := range chs {
		bundle.Integrations = append(bundle.Integrations, ConfigIntegration{
			Kind: "channel_installation", ChannelType: c.ChannelType, Agent: uuidString(c.AgentID),
		})
	}
	pls, err := q.ExportPluginInstallations(ctx, wsID)
	if err != nil {
		return fmt.Errorf("export plugins: %w", err)
	}
	for _, p := range pls {
		bundle.PluginsToReinstall = append(bundle.PluginsToReinstall, ConfigPlugin{
			PluginKey:     p.PluginKey,
			Version:       p.Version,
			Enabled:       p.Enabled,
			GrantedScopes: rawOrEmpty(p.GrantedScopes, "[]"),
			Config:        rawOrEmpty(p.Config, "{}"),
		})
	}
	return nil
}
