package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (st *importState) importWorkspace(ctx context.Context, q *db.Queries, dry bool) ([]ConfigImportItem, error) {
	ws := st.req.Bundle.Entities.Workspace
	if ws == nil || !st.applyWorkspaceSettings() {
		return []ConfigImportItem{}, nil
	}
	item := ConfigImportItem{Name: "workspace", Action: ActionUpdated, TargetID: uuidString(st.env.TargetID)}
	if dry {
		return []ConfigImportItem{item}, nil
	}
	params := db.PatchWorkspaceConfigImportParams{
		ID:                    st.env.TargetID,
		Settings:              []byte(ws.Settings),
		Context:               pgtype.Text{String: ws.Context, Valid: true},
		Repos:                 []byte(ws.Repos),
		AttributionFailClosed: pgtype.Bool{Bool: ws.AttributionFailClosed, Valid: true},
	}
	if st.req.Options.ApplyIssuePrefix && ws.IssuePrefix != "" {
		n, err := q.CountWorkspaceIssues(ctx, st.env.TargetID)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			params.IssuePrefix = pgtype.Text{String: ws.IssuePrefix, Valid: true}
		} else {
			st.report.Warnings = append(st.report.Warnings, ConfigWarning{Code: "issue_prefix_skipped_target_has_issues"})
		}
	}
	if _, err := q.PatchWorkspaceConfigImport(ctx, params); err != nil {
		return nil, err
	}
	return []ConfigImportItem{item}, nil
}

func (st *importState) importLabels(ctx context.Context, q *db.Queries, dry bool) ([]ConfigImportItem, error) {
	var items []ConfigImportItem
	for _, lab := range st.req.Bundle.Entities.Labels {
		existing, err := q.GetLabelByIdentity(ctx, db.GetLabelByIdentityParams{
			WorkspaceID: st.env.TargetID, ResourceType: lab.ResourceType, Lower: lab.Name,
		})
		exists := err == nil
		if err != nil && !isNoRows(err) {
			return items, err
		}
		action, _, cerr := st.resolveConflict(lab.Name, exists)
		if cerr != nil {
			items = append(items, ConfigImportItem{SourceID: lab.SourceID, Name: lab.Name, Action: ActionFailed, Reason: "exists"})
			return items, cerr
		}
		item := ConfigImportItem{SourceID: lab.SourceID, Name: lab.Name, Action: action}
		switch action {
		case ActionSkipped:
			item.TargetID = uuidString(existing.ID)
			st.put("label", lab.SourceID, item.TargetID)
		case ActionUpdated:
			item.TargetID = uuidString(existing.ID)
			st.put("label", lab.SourceID, item.TargetID)
			if !dry {
				if _, err := q.UpdateLabel(ctx, db.UpdateLabelParams{
					ID: existing.ID, WorkspaceID: st.env.TargetID,
					Name:        pgtype.Text{String: lab.Name, Valid: true},
					Description: pgtype.Text{String: lab.Description, Valid: true},
					Color:       pgtype.Text{String: lab.Color, Valid: true},
				}); err != nil {
					return items, err
				}
			}
		case ActionRenamed:
			name := lab.Name
			target := ""
			for i := 2; i < configImportRenameMax+2; i++ {
				cand := nextRenamed(lab.Name, i)
				_, err := q.GetLabelByIdentity(ctx, db.GetLabelByIdentityParams{
					WorkspaceID: st.env.TargetID, ResourceType: lab.ResourceType, Lower: cand,
				})
				if isNoRows(err) {
					name = cand
					break
				}
				if err != nil {
					return items, err
				}
			}
			item.Name = name
			if dry {
				target = st.newID()
			} else {
				row, err := q.CreateLabel(ctx, db.CreateLabelParams{
					WorkspaceID: st.env.TargetID, ResourceType: lab.ResourceType,
					Name: name, Description: lab.Description, Color: lab.Color,
				})
				if err != nil {
					return items, err
				}
				target = uuidString(row.ID)
			}
			item.TargetID = target
			st.put("label", lab.SourceID, target)
		default:
			var target string
			if dry {
				target = st.newID()
			} else {
				row, err := q.CreateLabel(ctx, db.CreateLabelParams{
					WorkspaceID: st.env.TargetID, ResourceType: lab.ResourceType,
					Name: lab.Name, Description: lab.Description, Color: lab.Color,
				})
				if err != nil {
					return items, err
				}
				target = uuidString(row.ID)
			}
			item.TargetID = target
			st.put("label", lab.SourceID, target)
		}
		if action == ActionSkipped {
			item.Reason = "exists"
		}
		items = append(items, item)
	}
	return items, nil
}

func (st *importState) importIssueStatuses(ctx context.Context, q *db.Queries, dry bool) ([]ConfigImportItem, error) {
	var items []ConfigImportItem
	for _, stt := range st.req.Bundle.Entities.IssueStatuses {
		existing, err := q.GetIssueStatusEntryByKey(ctx, db.GetIssueStatusEntryByKeyParams{
			WorkspaceID: st.env.TargetID, Key: stt.Key,
		})
		exists := err == nil
		if err != nil && !isNoRows(err) {
			return items, err
		}
		// Built-in rows are platform-seeded, not user data: creating a workspace
		// seeds the same 7 keys the source exported (see handler/workspace.go),
		// so an import into a freshly created workspace always finds a same-key
		// row on the very first batch. Under `fail` — the CLI's default — that
		// used to 409 before the user saw a single preview row, which is exactly
		// the runbook flow ("create an empty workspace, then import"). Both
		// sides carry the seeded name/description/color, so skipping writes what
		// overwrite would. A built-in is always its own category
		// (issue_status_system_is_canonical), so the mismatch check below cannot
		// fire for one and stays untouched for real data.
		builtin := exists && existing.IsSystem && stt.IsSystem
		if exists && existing.Category != stt.Category && !builtin {
			item := ConfigImportItem{SourceID: stt.SourceID, Name: stt.Key, Action: ActionFailed, Reason: "category_mismatch", TargetID: uuidString(existing.ID)}
			items = append(items, item)
			if st.req.OnConflict == ConflictFail {
				return items, &ImportError{Status: 409, Code: "config_import_conflict", Msg: "status category mismatch: " + stt.Key}
			}
			st.put("issue_status", stt.SourceID, uuidString(existing.ID))
			continue
		}
		action := ActionCreated
		if exists {
			if st.req.OnConflict == ConflictRename || (builtin && st.req.OnConflict == ConflictFail) {
				action = ActionSkipped
			} else {
				var cerr error
				action, _, cerr = st.resolveConflict(stt.Key, true)
				if cerr != nil {
					items = append(items, ConfigImportItem{SourceID: stt.SourceID, Name: stt.Key, Action: ActionFailed, Reason: "exists"})
					return items, cerr
				}
			}
		}
		item := ConfigImportItem{SourceID: stt.SourceID, Name: stt.Key, Action: action}
		if exists {
			item.TargetID = uuidString(existing.ID)
			st.put("issue_status", stt.SourceID, item.TargetID)
			if action == ActionUpdated && !dry {
				if _, err := q.UpdateIssueStatusEntryForImport(ctx, db.UpdateIssueStatusEntryForImportParams{
					Name:        pgtype.Text{String: stt.Name, Valid: true},
					Description: pgtype.Text{String: stt.Description, Valid: true},
					Color:       pgtype.Text{String: stt.Color, Valid: true},
					Position:    pgtype.Float8{Float64: stt.Position, Valid: true},
					ID:          existing.ID, WorkspaceID: st.env.TargetID,
				}); err != nil {
					return items, err
				}
			}
			if action == ActionSkipped {
				item.Reason = "exists"
			}
		} else {
			var target string
			if dry {
				target = st.newID()
			} else {
				row, err := q.CreateIssueStatusEntry(ctx, db.CreateIssueStatusEntryParams{
					WorkspaceID: st.env.TargetID, Key: stt.Key, Name: stt.Name,
					Description: stt.Description, Category: stt.Category, Color: stt.Color,
				})
				if err != nil {
					return items, err
				}
				if _, err := q.UpdateIssueStatusEntryForImport(ctx, db.UpdateIssueStatusEntryForImportParams{
					Position: pgtype.Float8{Float64: stt.Position, Valid: true},
					ID:       row.ID, WorkspaceID: st.env.TargetID,
				}); err != nil {
					return items, err
				}
				target = uuidString(row.ID)
			}
			item.TargetID = target
			st.put("issue_status", stt.SourceID, target)
		}
		items = append(items, item)
	}
	return items, nil
}

func (st *importState) importIssueProperties(ctx context.Context, q *db.Queries, dry bool) ([]ConfigImportItem, error) {
	var items []ConfigImportItem
	for _, p := range st.req.Bundle.Entities.IssueProperties {
		existing, err := q.GetIssuePropertyByName(ctx, db.GetIssuePropertyByNameParams{
			WorkspaceID: st.env.TargetID, Lower: p.Name,
		})
		exists := err == nil
		if err != nil && !isNoRows(err) {
			return items, err
		}
		if exists && existing.Type != p.Type {
			item := ConfigImportItem{SourceID: p.SourceID, Name: p.Name, Action: ActionFailed, Reason: "type_mismatch", TargetID: uuidString(existing.ID)}
			items = append(items, item)
			if st.req.OnConflict == ConflictFail {
				return items, &ImportError{Status: 409, Code: "config_import_conflict", Msg: "property type mismatch: " + p.Name}
			}
			st.put("property", p.SourceID, uuidString(existing.ID))
			continue
		}
		action, _, cerr := st.resolveConflict(p.Name, exists)
		if cerr != nil {
			items = append(items, ConfigImportItem{SourceID: p.SourceID, Name: p.Name, Action: ActionFailed, Reason: "exists"})
			return items, cerr
		}
		if exists && st.req.OnConflict == ConflictRename {
			action = ActionSkipped
		}
		item := ConfigImportItem{SourceID: p.SourceID, Name: p.Name, Action: action}
		cfg := []byte(p.Config)
		if len(cfg) == 0 {
			cfg = []byte("{}")
		}
		switch action {
		case ActionSkipped:
			item.TargetID = uuidString(existing.ID)
			item.Reason = "exists"
			st.put("property", p.SourceID, item.TargetID)
		case ActionUpdated:
			item.TargetID = uuidString(existing.ID)
			st.put("property", p.SourceID, item.TargetID)
			if !dry {
				if _, err := q.UpdateIssueProperty(ctx, db.UpdateIssuePropertyParams{
					ID: existing.ID, WorkspaceID: st.env.TargetID,
					Name:        pgtype.Text{String: p.Name, Valid: true},
					Description: pgtype.Text{String: p.Description, Valid: true},
					Icon:        pgtype.Text{String: p.Icon, Valid: true},
					Config:      cfg,
				}); err != nil {
					return items, err
				}
				if err := q.UpdateIssuePropertyPosition(ctx, db.UpdateIssuePropertyPositionParams{
					ID: existing.ID, Position: p.Position,
				}); err != nil {
					return items, err
				}
			}
		default:
			var target string
			if dry {
				target = st.newID()
			} else {
				row, err := q.CreateIssueProperty(ctx, db.CreateIssuePropertyParams{
					WorkspaceID: st.env.TargetID, Name: p.Name, Type: p.Type,
					Description: p.Description, Icon: p.Icon, Config: cfg,
				})
				if err != nil {
					return items, err
				}
				_ = q.UpdateIssuePropertyPosition(ctx, db.UpdateIssuePropertyPositionParams{ID: row.ID, Position: p.Position})
				target = uuidString(row.ID)
			}
			item.TargetID = target
			st.put("property", p.SourceID, target)
		}
		items = append(items, item)
	}
	return items, nil
}

func (st *importState) importSkills(ctx context.Context, q *db.Queries, dry bool) ([]ConfigImportItem, error) {
	var items []ConfigImportItem
	for _, sk := range st.req.Bundle.Entities.Skills {
		existing, err := q.GetSkillByWorkspaceAndName(ctx, db.GetSkillByWorkspaceAndNameParams{
			WorkspaceID: st.env.TargetID, Name: sk.Name,
		})
		exists := err == nil
		if err != nil && !isNoRows(err) {
			return items, err
		}
		action, name, cerr := st.resolveConflict(sk.Name, exists)
		if cerr != nil {
			items = append(items, ConfigImportItem{SourceID: sk.SourceID, Name: sk.Name, Action: ActionFailed, Reason: "exists"})
			return items, cerr
		}
		item := ConfigImportItem{SourceID: sk.SourceID, Name: sk.Name, Action: action}
		cfg := []byte(sk.Config)
		if len(cfg) == 0 {
			cfg = []byte("{}")
		}
		writeSkill := func(targetName string, id pgtype.UUID, isNew bool) (string, error) {
			if dry {
				if isNew {
					return st.newID(), nil
				}
				return uuidString(id), nil
			}
			var row db.Skill
			var err error
			if isNew {
				row, err = q.CreateSkill(ctx, db.CreateSkillParams{
					WorkspaceID: st.env.TargetID, Name: targetName, Description: sk.Description,
					Content: sk.Content, Config: cfg, CreatedBy: st.env.ImporterID,
				})
			} else {
				row, err = q.UpdateSkill(ctx, db.UpdateSkillParams{
					ID: id, Name: pgtype.Text{String: targetName, Valid: true},
					Description: pgtype.Text{String: sk.Description, Valid: true},
					Content:     pgtype.Text{String: sk.Content, Valid: true}, Config: cfg,
				})
				if err == nil {
					_ = q.DeleteSkillFilesBySkill(ctx, id)
					_ = q.DeleteSkillLabelAssignmentsBySkill(ctx, id)
				}
			}
			if err != nil {
				return "", err
			}
			for _, f := range sk.Files {
				if _, err := q.UpsertSkillFile(ctx, db.UpsertSkillFileParams{SkillID: row.ID, Path: f.Path, Content: f.Content}); err != nil {
					return "", err
				}
			}
			for _, lid := range sk.LabelIDs {
				if tid, ok := st.get("label", lid); ok {
					tu, _ := parseUUID(tid)
					_ = q.AttachLabelToSkill(ctx, db.AttachLabelToSkillParams{SkillID: row.ID, LabelID: tu, WorkspaceID: st.env.TargetID})
				} else {
					st.dropRef("skill", sk.SourceID, "label_ids", "label", lid, "dropped")
				}
			}
			return uuidString(row.ID), nil
		}
		switch action {
		case ActionSkipped:
			item.TargetID = uuidString(existing.ID)
			item.Reason = "exists"
			st.put("skill", sk.SourceID, item.TargetID)
		case ActionUpdated:
			tid, err := writeSkill(sk.Name, existing.ID, false)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("skill", sk.SourceID, tid)
		case ActionRenamed:
			final := sk.Name
			for i := 2; i < configImportRenameMax+2; i++ {
				cand := nextRenamed(sk.Name, i)
				_, err := q.GetSkillByWorkspaceAndName(ctx, db.GetSkillByWorkspaceAndNameParams{WorkspaceID: st.env.TargetID, Name: cand})
				if isNoRows(err) {
					final = cand
					break
				}
				if err != nil && !isNoRows(err) {
					return items, err
				}
			}
			item.Name = final
			tid, err := writeSkill(final, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("skill", sk.SourceID, tid)
		default:
			tid, err := writeSkill(name, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("skill", sk.SourceID, tid)
		}
		items = append(items, item)
	}
	return items, nil
}

func (st *importState) importMcpServers(ctx context.Context, q *db.Queries, _ bool) ([]ConfigImportItem, error) {
	var items []ConfigImportItem
	for _, s := range st.req.Bundle.Entities.McpServers {
		row, err := q.GetWorkspaceMcpServerByName(ctx, db.GetWorkspaceMcpServerByNameParams{
			WorkspaceID: st.env.TargetID, Name: s.Name,
		})
		item := ConfigImportItem{SourceID: s.SourceID, Name: s.Name, Action: ActionSkipped, Reason: "lookup_only"}
		if err == nil {
			item.TargetID = uuidString(row.ID)
			st.put("mcp_server", s.SourceID, item.TargetID)
		} else if !isNoRows(err) {
			return items, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (st *importState) importAgents(ctx context.Context, q *db.Queries, dry bool) ([]ConfigImportItem, error) {
	var items []ConfigImportItem
	for _, ag := range st.req.Bundle.Entities.Agents {
		existing, err := q.GetUserAgentByName(ctx, db.GetUserAgentByNameParams{
			WorkspaceID: st.env.TargetID, Name: ag.Name,
		})
		exists := err == nil
		if err != nil && !isNoRows(err) {
			return items, err
		}
		action, _, cerr := st.resolveConflict(ag.Name, exists)
		if cerr != nil {
			items = append(items, ConfigImportItem{SourceID: ag.SourceID, Name: ag.Name, Action: ActionFailed, Reason: "exists"})
			return items, cerr
		}
		item := ConfigImportItem{SourceID: ag.SourceID, Name: ag.Name, Action: action}
		writeAgent := func(name string, id pgtype.UUID, isNew bool) (string, error) {
			if dry {
				if isNew {
					return st.newID(), nil
				}
				return uuidString(id), nil
			}
			rc := []byte(ag.RuntimeConfig)
			if len(rc) == 0 {
				rc = []byte("{}")
			}
			stripped, _ := stripGatewayToken(rc)
			args := []byte(ag.CustomArgs)
			if len(args) == 0 {
				args = []byte("[]")
			}
			if isNew {
				args = restoreSecretArgs(args, nil)
			} else {
				args = restoreSecretArgs(args, existing.CustomArgs)
			}
			starters := []byte(ag.ConversationStarters)
			if len(starters) == 0 {
				starters = []byte("[]")
			}
			var row db.Agent
			var err error
			if isNew {
				avatar := pgtype.Text{}
				if ag.AvatarURL != nil {
					avatar = pgtype.Text{String: *ag.AvatarURL, Valid: true}
				}
				model := pgtype.Text{}
				if ag.Model != nil {
					model = pgtype.Text{String: *ag.Model, Valid: true}
				}
				think := pgtype.Text{}
				if ag.ThinkingLevel != nil {
					think = pgtype.Text{String: *ag.ThinkingLevel, Valid: true}
				}
				tier := pgtype.Text{}
				if ag.ServiceTier != nil {
					tier = pgtype.Text{String: *ag.ServiceTier, Valid: true}
				}
				row, err = q.CreateAgent(ctx, db.CreateAgentParams{
					WorkspaceID: st.env.TargetID, Name: name, Description: ag.Description,
					AvatarUrl: avatar, RuntimeMode: ag.RuntimeMode, RuntimeConfig: stripped,
					Visibility: ag.Visibility, MaxConcurrentTasks: ag.MaxConcurrentTasks,
					OwnerID: st.env.ImporterID, Instructions: ag.Instructions,
					CustomEnv: []byte("{}"), CustomArgs: args, Model: model,
					ThinkingLevel: think, ServiceTier: tier, ConversationStarters: starters,
					ComposioToolkitAllowlist: ag.ComposioToolkitAllowlist, PermissionMode: ag.PermissionMode,
				})
			} else {
				row, err = q.UpdateAgent(ctx, db.UpdateAgentParams{
					ID:                       id,
					Name:                     pgtype.Text{String: name, Valid: true},
					Description:              pgtype.Text{String: ag.Description, Valid: true},
					AvatarUrl:                optionalText(ag.AvatarURL),
					Model:                    optionalText(ag.Model),
					ThinkingLevel:            optionalText(ag.ThinkingLevel),
					ServiceTier:              optionalText(ag.ServiceTier),
					RuntimeConfig:            keepTargetGatewayToken(stripped, existing.RuntimeConfig),
					RuntimeMode:              pgtype.Text{String: ag.RuntimeMode, Valid: true},
					Visibility:               pgtype.Text{String: ag.Visibility, Valid: true},
					PermissionMode:           pgtype.Text{String: ag.PermissionMode, Valid: true},
					MaxConcurrentTasks:       pgtype.Int4{Int32: ag.MaxConcurrentTasks, Valid: true},
					Instructions:             pgtype.Text{String: ag.Instructions, Valid: true},
					CustomArgs:               args,
					ConversationStarters:     starters,
					ComposioToolkitAllowlist: ag.ComposioToolkitAllowlist,
				})
				if err == nil {
					err = errors.Join(
						q.RemoveAllAgentSkills(ctx, id),
						q.DeleteAgentLabelAssignmentsByAgent(ctx, id),
						q.DeleteAgentMcpServersByAgent(ctx, id),
						q.DeleteAgentInvocationTargets(ctx, id),
					)
				}
			}
			if err != nil {
				return "", err
			}
			if len(ag.DisabledRuntimeSkills) > 0 && string(ag.DisabledRuntimeSkills) != "null" {
				_, _ = q.UpdateAgentDisabledRuntimeSkills(ctx, db.UpdateAgentDisabledRuntimeSkillsParams{
					ID: row.ID, DisabledRuntimeSkills: []byte(ag.DisabledRuntimeSkills),
				})
			}
			for _, s := range ag.Skills {
				if tid, ok := st.get("skill", s.Skill); ok {
					tu, _ := parseUUID(tid)
					_ = q.AddAgentSkill(ctx, db.AddAgentSkillParams{AgentID: row.ID, SkillID: tu})
					if !s.Enabled {
						_, _ = q.SetAgentSkillEnabled(ctx, db.SetAgentSkillEnabledParams{AgentID: row.ID, SkillID: tu, Enabled: false})
					}
				} else {
					st.dropRef("agent", ag.SourceID, "skills", "skill", s.Skill, "dropped")
				}
			}
			for _, lid := range ag.LabelIDs {
				if tid, ok := st.get("label", lid); ok {
					tu, _ := parseUUID(tid)
					_ = q.AttachLabelToAgent(ctx, db.AttachLabelToAgentParams{AgentID: row.ID, LabelID: tu, WorkspaceID: st.env.TargetID})
				} else {
					st.dropRef("agent", ag.SourceID, "label_ids", "label", lid, "dropped")
				}
			}
			for _, m := range ag.McpServers {
				if tid, ok := st.get("mcp_server", m.Server); ok {
					tu, _ := parseUUID(tid)
					_ = q.AddAgentMcpServer(ctx, db.AddAgentMcpServerParams{AgentID: row.ID, ServerID: tu})
					if !m.Enabled {
						_, _ = q.SetAgentMcpServerEnabled(ctx, db.SetAgentMcpServerEnabledParams{AgentID: row.ID, ServerID: tu, Enabled: false})
					}
				} else {
					st.dropRef("agent", ag.SourceID, "mcp_servers", "workspace_mcp_server", m.Server, "dropped")
				}
			}
			keptMember := false
			for _, inv := range ag.InvocationTargets {
				switch inv.TargetType {
				case "workspace":
					_ = q.CreateAgentInvocationTarget(ctx, db.CreateAgentInvocationTargetParams{
						AgentID: row.ID, TargetType: "workspace", TargetID: st.env.TargetID, CreatedBy: st.env.ImporterID,
					})
				case "member":
					if inv.TargetID != nil && st.isMember(ctx, q, *inv.TargetID) {
						uid, _ := parseUUID(*inv.TargetID)
						_ = q.CreateAgentInvocationTarget(ctx, db.CreateAgentInvocationTargetParams{
							AgentID: row.ID, TargetType: "member", TargetID: uid, CreatedBy: st.env.ImporterID,
						})
						keptMember = true
					} else if inv.TargetID != nil {
						st.dropMember("agent", ag.SourceID, "invocation_targets", *inv.TargetID)
					}
				case "team":
					if inv.TargetID != nil {
						uid, err := parseUUID(*inv.TargetID)
						if err == nil {
							_ = q.CreateAgentInvocationTarget(ctx, db.CreateAgentInvocationTargetParams{
								AgentID: row.ID, TargetType: "team", TargetID: uid, CreatedBy: st.env.ImporterID,
							})
						}
					}
				}
			}
			_ = keptMember
			return uuidString(row.ID), nil
		}
		switch action {
		case ActionSkipped:
			item.TargetID = uuidString(existing.ID)
			item.Reason = "exists"
			st.put("agent", ag.SourceID, item.TargetID)
		case ActionUpdated:
			tid, err := writeAgent(ag.Name, existing.ID, false)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("agent", ag.SourceID, tid)
		case ActionRenamed:
			final := ag.Name
			for i := 2; i < configImportRenameMax+2; i++ {
				cand := nextRenamed(ag.Name, i)
				_, err := q.GetUserAgentByName(ctx, db.GetUserAgentByNameParams{WorkspaceID: st.env.TargetID, Name: cand})
				if isNoRows(err) {
					final = cand
					break
				}
			}
			item.Name = final
			tid, err := writeAgent(final, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("agent", ag.SourceID, tid)
		default:
			tid, err := writeAgent(ag.Name, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("agent", ag.SourceID, tid)
		}
		items = append(items, item)
	}
	return items, nil
}

func (st *importState) importSystemAgents(ctx context.Context, q *db.Queries, dry bool) ([]ConfigImportItem, error) {
	var items []ConfigImportItem
	for _, sa := range st.req.Bundle.Entities.SystemAgents {
		existing, err := q.GetAgentBySystemKey(ctx, db.GetAgentBySystemKeyParams{
			WorkspaceID: st.env.TargetID, SystemKey: pgtype.Text{String: sa.SystemKey, Valid: true},
		})
		item := ConfigImportItem{Name: sa.SystemKey, Action: ActionUpdated}
		if err != nil {
			item.Action = ActionSkipped
			item.Reason = "missing_system_agent"
			items = append(items, item)
			continue
		}
		item.TargetID = uuidString(existing.ID)
		if !dry {
			starters := []byte(sa.ConversationStarters)
			if len(starters) == 0 {
				starters = []byte("[]")
			}
			model := pgtype.Text{}
			if sa.Model != nil {
				model = pgtype.Text{String: *sa.Model, Valid: true}
			}
			think := pgtype.Text{}
			if sa.ThinkingLevel != nil {
				think = pgtype.Text{String: *sa.ThinkingLevel, Valid: true}
			}
			tier := pgtype.Text{}
			if sa.ServiceTier != nil {
				tier = pgtype.Text{String: *sa.ServiceTier, Valid: true}
			}
			if _, err := q.UpdateAgent(ctx, db.UpdateAgentParams{
				ID:           existing.ID,
				Instructions: pgtype.Text{String: sa.Instructions, Valid: true},
				Model:        model, ThinkingLevel: think, ServiceTier: tier,
				ConversationStarters: starters,
			}); err != nil {
				return items, err
			}
			if len(sa.DisabledRuntimeSkills) > 0 {
				_, _ = q.UpdateAgentDisabledRuntimeSkills(ctx, db.UpdateAgentDisabledRuntimeSkillsParams{
					ID: existing.ID, DisabledRuntimeSkills: []byte(sa.DisabledRuntimeSkills),
				})
			}
		}
		items = append(items, item)
	}
	return items, nil
}

func (st *importState) importSquads(ctx context.Context, q *db.Queries, dry bool) ([]ConfigImportItem, error) {
	var items []ConfigImportItem
	for _, sq := range st.req.Bundle.Entities.Squads {
		leaderTID, ok := st.get("agent", sq.LeaderID)
		if !ok {
			st.dropRef("squad", sq.SourceID, "leader_id", "agent", sq.LeaderID, "skipped")
			items = append(items, ConfigImportItem{SourceID: sq.SourceID, Name: sq.Name, Action: ActionSkipped, Reason: "leader_unmapped"})
			continue
		}
		existing, err := q.GetEarliestSquadByName(ctx, db.GetEarliestSquadByNameParams{
			WorkspaceID: st.env.TargetID, Name: sq.Name,
		})
		exists := err == nil
		if err != nil && !isNoRows(err) {
			return items, err
		}
		action, _, cerr := st.resolveConflict(sq.Name, exists)
		if cerr != nil {
			items = append(items, ConfigImportItem{SourceID: sq.SourceID, Name: sq.Name, Action: ActionFailed, Reason: "exists"})
			return items, cerr
		}
		item := ConfigImportItem{SourceID: sq.SourceID, Name: sq.Name, Action: action}
		writeSquad := func(name string, id pgtype.UUID, isNew bool) (string, error) {
			if dry {
				if isNew {
					return st.newID(), nil
				}
				return uuidString(id), nil
			}
			leader, _ := parseUUID(leaderTID)
			avatar := pgtype.Text{}
			if sq.AvatarURL != nil {
				avatar = pgtype.Text{String: *sq.AvatarURL, Valid: true}
			}
			var row db.Squad
			var err error
			if isNew {
				row, err = q.CreateSquad(ctx, db.CreateSquadParams{
					WorkspaceID: st.env.TargetID, Name: name, Description: sq.Description,
					LeaderID: leader, CreatorID: st.env.ImporterID, AvatarUrl: avatar,
				})
				if err != nil {
					return "", err
				}
				_, _ = q.AddSquadMember(ctx, db.AddSquadMemberParams{
					SquadID: row.ID, MemberType: "agent", MemberID: leader, Role: "leader",
				})
				if sq.Instructions != "" {
					_, _ = q.UpdateSquad(ctx, db.UpdateSquadParams{
						ID: row.ID, Instructions: pgtype.Text{String: sq.Instructions, Valid: true},
					})
				}
			} else {
				row, err = q.UpdateSquad(ctx, db.UpdateSquadParams{
					ID: id, Name: pgtype.Text{String: name, Valid: true},
					Description: pgtype.Text{String: sq.Description, Valid: true},
					LeaderID:    leader, AvatarUrl: avatar,
					Instructions: pgtype.Text{String: sq.Instructions, Valid: true},
				})
				if err != nil {
					return "", err
				}
				if err := q.DeleteSquadMembersBySquad(ctx, id); err != nil {
					return "", err
				}
				_, _ = q.AddSquadMember(ctx, db.AddSquadMemberParams{
					SquadID: row.ID, MemberType: "agent", MemberID: leader, Role: "leader",
				})
			}
			for i, m := range sq.Members {
				if m.Role == "leader" {
					continue
				}
				// A leader reassigned to an existing member keeps its old role in
				// squad_member; inserting it again would violate the unique key
				// and abort the whole batch transaction.
				if m.MemberType == "agent" {
					if tid, ok := st.get("agent", m.MemberID); ok && tid == leaderTID {
						continue
					}
				}
				var mid pgtype.UUID
				switch m.MemberType {
				case "agent":
					tid, ok := st.get("agent", m.MemberID)
					if !ok {
						st.dropRef("squad", sq.SourceID, fmt.Sprintf("members[%d].member_id", i), "agent", m.MemberID, "dropped")
						continue
					}
					mid, _ = parseUUID(tid)
				case "member":
					if !st.isMember(ctx, q, m.MemberID) {
						st.dropMember("squad", sq.SourceID, fmt.Sprintf("members[%d].member_id", i), m.MemberID)
						continue
					}
					mid, _ = parseUUID(m.MemberID)
				default:
					continue
				}
				_, _ = q.AddSquadMember(ctx, db.AddSquadMemberParams{
					SquadID: row.ID, MemberType: m.MemberType, MemberID: mid, Role: m.Role,
				})
			}
			return uuidString(row.ID), nil
		}
		switch action {
		case ActionSkipped:
			item.TargetID = uuidString(existing.ID)
			item.Reason = "exists"
			st.put("squad", sq.SourceID, item.TargetID)
		case ActionUpdated:
			tid, err := writeSquad(sq.Name, existing.ID, false)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("squad", sq.SourceID, tid)
		case ActionRenamed:
			final := nextRenamed(sq.Name, 2)
			item.Name = final
			tid, err := writeSquad(final, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("squad", sq.SourceID, tid)
		default:
			tid, err := writeSquad(sq.Name, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("squad", sq.SourceID, tid)
		}
		items = append(items, item)
	}
	return items, nil
}

func (st *importState) importProjects(ctx context.Context, q *db.Queries, dry bool) ([]ConfigImportItem, error) {
	var items []ConfigImportItem
	for _, p := range st.req.Bundle.Entities.Projects {
		existing, err := q.GetEarliestProjectByTitle(ctx, db.GetEarliestProjectByTitleParams{
			WorkspaceID: st.env.TargetID, Title: p.Title,
		})
		exists := err == nil
		if err != nil && !isNoRows(err) {
			return items, err
		}
		action, _, cerr := st.resolveConflict(p.Title, exists)
		if cerr != nil {
			items = append(items, ConfigImportItem{SourceID: p.SourceID, Name: p.Title, Action: ActionFailed, Reason: "exists"})
			return items, cerr
		}
		item := ConfigImportItem{SourceID: p.SourceID, Name: p.Title, Action: action}
		writeProject := func(title string, id pgtype.UUID, isNew bool) (string, error) {
			if dry {
				if isNew {
					return st.newID(), nil
				}
				return uuidString(id), nil
			}
			leadType := pgtype.Text{}
			leadID := pgtype.UUID{}
			if p.Lead != nil {
				switch p.Lead.Type {
				case "agent":
					if tid, ok := st.get("agent", p.Lead.ID); ok {
						leadType = pgtype.Text{String: "agent", Valid: true}
						leadID, _ = parseUUID(tid)
					} else {
						st.dropRef("project", p.SourceID, "lead", "agent", p.Lead.ID, "cleared")
					}
				case "member":
					if st.isMember(ctx, q, p.Lead.ID) {
						leadType = pgtype.Text{String: "member", Valid: true}
						leadID, _ = parseUUID(p.Lead.ID)
					} else {
						st.dropMember("project", p.SourceID, "lead", p.Lead.ID)
					}
				}
			}
			icon := pgtype.Text{}
			if p.Icon != nil {
				icon = pgtype.Text{String: *p.Icon, Valid: true}
			}
			start := parseDate(p.StartDate)
			due := parseDate(p.DueDate)
			status := p.Status
			if status == "" {
				status = "planned"
			}
			priority := p.Priority
			if priority == "" {
				priority = "none"
			}
			var row db.Project
			var err error
			if isNew {
				row, err = q.CreateProject(ctx, db.CreateProjectParams{
					WorkspaceID: st.env.TargetID, Title: title,
					Description: pgtype.Text{String: p.Description, Valid: true},
					Icon:        icon, Status: status, LeadType: leadType, LeadID: leadID,
					Priority: priority, StartDate: start, DueDate: due,
				})
			} else {
				row, err = q.UpdateProject(ctx, db.UpdateProjectParams{
					ID: id, Title: pgtype.Text{String: title, Valid: true},
					Description: pgtype.Text{String: p.Description, Valid: true},
					Icon:        icon, Status: pgtype.Text{String: status, Valid: true},
					Priority: pgtype.Text{String: priority, Valid: true},
					LeadType: leadType, LeadID: leadID, StartDate: start, DueDate: due,
				})
				if err == nil {
					_ = q.DeleteProjectResourcesByProject(ctx, id)
				}
			}
			if err != nil {
				return "", err
			}
			for i, res := range p.Resources {
				ref := []byte(res.ResourceRef)
				if res.ResourceType == "local_directory" {
					var obj map[string]any
					if json.Unmarshal(ref, &obj) == nil {
						if did, ok := obj["daemon_id"].(string); ok && did != "" {
							okExists, err := q.DaemonIDExistsInWorkspace(ctx, db.DaemonIDExistsInWorkspaceParams{
								WorkspaceID: st.env.TargetID, DaemonID: pgtype.Text{String: did, Valid: true},
							})
							if err != nil || !okExists {
								st.dropRef("project", p.SourceID, fmt.Sprintf("resources[%d]", i), "daemon", did, "dropped")
								continue
							}
						}
					}
				}
				label := pgtype.Text{}
				if res.Label != nil {
					label = pgtype.Text{String: *res.Label, Valid: true}
				}
				_, _ = q.CreateProjectResource(ctx, db.CreateProjectResourceParams{
					ProjectID: row.ID, WorkspaceID: st.env.TargetID, ResourceType: res.ResourceType,
					ResourceRef: ref, Label: label, Position: res.Position, CreatedBy: st.env.ImporterID,
				})
			}
			return uuidString(row.ID), nil
		}
		switch action {
		case ActionSkipped:
			item.TargetID = uuidString(existing.ID)
			item.Reason = "exists"
			st.put("project", p.SourceID, item.TargetID)
		case ActionUpdated:
			tid, err := writeProject(p.Title, existing.ID, false)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("project", p.SourceID, tid)
		case ActionRenamed:
			final := nextRenamed(p.Title, 2)
			item.Name = final
			tid, err := writeProject(final, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("project", p.SourceID, tid)
		default:
			tid, err := writeProject(p.Title, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("project", p.SourceID, tid)
		}
		items = append(items, item)
	}
	return items, nil
}

func parseDate(s *string) pgtype.Date {
	if s == nil || *s == "" {
		return pgtype.Date{}
	}
	t, err := time.Parse("2006-01-02", *s)
	if err != nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: t, Valid: true}
}

func (st *importState) importAutopilots(ctx context.Context, q *db.Queries, dry bool) ([]ConfigImportItem, error) {
	var items []ConfigImportItem
	paused := 0
	for _, ap := range st.req.Bundle.Entities.Autopilots {
		if ap.Assignee == nil {
			items = append(items, ConfigImportItem{SourceID: ap.SourceID, Name: ap.Title, Action: ActionSkipped, Reason: "assignee_unmapped"})
			continue
		}
		kind := "agent"
		if ap.Assignee.Type == "squad" {
			kind = "squad"
		}
		assigneeTID, ok := st.get(kind, ap.Assignee.ID)
		if !ok {
			st.dropRef("autopilot", ap.SourceID, "assignee", kind, ap.Assignee.ID, "skipped")
			items = append(items, ConfigImportItem{SourceID: ap.SourceID, Name: ap.Title, Action: ActionSkipped, Reason: "assignee_unmapped"})
			continue
		}
		existing, err := q.GetEarliestAutopilotByTitle(ctx, db.GetEarliestAutopilotByTitleParams{
			WorkspaceID: st.env.TargetID, Title: ap.Title,
		})
		exists := err == nil
		if err != nil && !isNoRows(err) {
			return items, err
		}
		action, _, cerr := st.resolveConflict(ap.Title, exists)
		if cerr != nil {
			items = append(items, ConfigImportItem{SourceID: ap.SourceID, Name: ap.Title, Action: ActionFailed, Reason: "exists"})
			return items, cerr
		}
		item := ConfigImportItem{SourceID: ap.SourceID, Name: ap.Title, Action: action}
		status := "paused"
		if st.req.Options.ActivateAutopilots && ap.Status != "" && ap.Status != "archived" {
			status = ap.Status
		} else {
			paused++
		}
		writeAP := func(title string, id pgtype.UUID, isNew bool) (string, error) {
			if dry {
				if isNew {
					return st.newID(), nil
				}
				return uuidString(id), nil
			}
			assignee, _ := parseUUID(assigneeTID)
			projectID := pgtype.UUID{}
			if ap.ProjectID != nil {
				if tid, ok := st.get("project", *ap.ProjectID); ok {
					projectID, _ = parseUUID(tid)
				} else {
					st.dropRef("autopilot", ap.SourceID, "project_id", "project", *ap.ProjectID, "cleared")
				}
			}
			tpl := pgtype.Text{}
			if ap.IssueTitleTemplate != nil {
				tpl = pgtype.Text{String: *ap.IssueTitleTemplate, Valid: true}
			}
			var row db.Autopilot
			var err error
			if isNew {
				row, err = q.CreateAutopilot(ctx, db.CreateAutopilotParams{
					WorkspaceID: st.env.TargetID, Title: title, AssigneeType: ap.Assignee.Type,
					AssigneeID: assignee, Status: status, ExecutionMode: ap.ExecutionMode,
					CreatedByType: "member", CreatedByID: st.env.ImporterID,
					Description:        pgtype.Text{String: ap.Description, Valid: true},
					IssueTitleTemplate: tpl, ProjectID: projectID,
				})
			} else {
				row, err = q.UpdateAutopilot(ctx, db.UpdateAutopilotParams{
					ID: id, Title: pgtype.Text{String: title, Valid: true},
					Description:  pgtype.Text{String: ap.Description, Valid: true},
					AssigneeType: pgtype.Text{String: ap.Assignee.Type, Valid: true},
					AssigneeID:   assignee, Status: pgtype.Text{String: status, Valid: true},
					ExecutionMode:      pgtype.Text{String: ap.ExecutionMode, Valid: true},
					IssueTitleTemplate: tpl, ProjectID: projectID,
				})
				if err == nil && action == ActionUpdated {
					existingTrigs, err := q.ListAutopilotTriggerIDs(ctx, id)
					if err != nil {
						return "", err
					}
					keepWebhook := map[string][]keptWebhook{}
					for _, t := range existingTrigs {
						if t.Kind == "webhook" {
							lab := textOrEmpty(t.Label)
							keepWebhook[lab] = append(keepWebhook[lab], keptWebhook{token: t.WebhookToken, signingSecret: t.SigningSecret})
						}
					}
					if err := errors.Join(
						q.DeleteAutopilotTriggersByAutopilot(ctx, id),
						q.DeleteAutopilotSubscribersForAutopilot(ctx, id),
						q.DeleteAutopilotCollaboratorsForAutopilot(ctx, id),
					); err != nil {
						return "", err
					}
					if err := st.writeAutopilotRel(ctx, q, row, ap, keepWebhook); err != nil {
						return "", err
					}
					_ = RecordAutopilotRuleVersion(ctx, q, row, "member", st.env.ImporterID)
					return uuidString(row.ID), nil
				}
			}
			if err != nil {
				return "", err
			}
			if err := st.writeAutopilotRel(ctx, q, row, ap, nil); err != nil {
				return "", err
			}
			_ = RecordAutopilotRuleVersion(ctx, q, row, "member", st.env.ImporterID)
			return uuidString(row.ID), nil
		}
		switch action {
		case ActionSkipped:
			item.TargetID = uuidString(existing.ID)
			item.Reason = "exists"
			st.put("autopilot", ap.SourceID, item.TargetID)
		case ActionUpdated:
			tid, err := writeAP(ap.Title, existing.ID, false)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("autopilot", ap.SourceID, tid)
		case ActionRenamed:
			final := nextRenamed(ap.Title, 2)
			item.Name = final
			tid, err := writeAP(final, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("autopilot", ap.SourceID, tid)
		default:
			tid, err := writeAP(ap.Title, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = tid
			st.put("autopilot", ap.SourceID, tid)
		}
		items = append(items, item)
	}
	if paused > 0 && !st.req.Options.ActivateAutopilots {
		st.report.Warnings = append(st.report.Warnings, ConfigWarning{Code: "autopilots_imported_paused", Count: paused})
	}
	return items, nil
}

// keptWebhook carries a target webhook trigger's secrets across the
// delete-and-recreate that overwrite performs.
type keptWebhook struct {
	token         pgtype.Text
	signingSecret pgtype.Text
}

func (st *importState) writeAutopilotRel(ctx context.Context, q *db.Queries, row db.Autopilot, ap ConfigAutopilot, keepWebhook map[string][]keptWebhook) error {
	for _, t := range ap.Triggers {
		token := pgtype.Text{}
		signingSecret := pgtype.Text{}
		if t.Kind == "webhook" {
			lab := ""
			if t.Label != nil {
				lab = *t.Label
			}
			// Each kept secret is consumed once: webhook_token is unique, so two
			// triggers sharing a label must not reuse the same target token.
			if kept := keepWebhook[lab]; len(kept) > 0 {
				keepWebhook[lab] = kept[1:]
				if kept[0].token.Valid {
					token = kept[0].token
					signingSecret = kept[0].signingSecret
				}
			}
			if !token.Valid {
				tok, err := generateImportWebhookToken()
				if err != nil {
					return err
				}
				token = pgtype.Text{String: tok, Valid: true}
				if st.publicURL != "" {
					st.report.SecretsToFill = append(st.report.SecretsToFill, SecretToFill{
						Entity: "autopilot_trigger", TargetID: uuidString(row.ID),
						Name: ap.Title, Field: "signing_secret",
						Path:       fmt.Sprintf("/%s/autopilots/%s", st.env.TargetSlug, uuidString(row.ID)),
						WebhookURL: st.publicURL + "/api/webhooks/autopilots/" + tok,
					})
				}
			}
		}
		cron := pgtype.Text{}
		if t.CronExpression != nil {
			cron = pgtype.Text{String: *t.CronExpression, Valid: true}
		}
		tz := pgtype.Text{}
		if t.Timezone != nil {
			tz = pgtype.Text{String: *t.Timezone, Valid: true}
		}
		label := pgtype.Text{}
		if t.Label != nil {
			label = pgtype.Text{String: *t.Label, Valid: true}
		}
		provider := pgtype.Text{String: t.Provider, Valid: t.Provider != ""}
		var filters []byte
		if len(t.EventFilters) > 0 && string(t.EventFilters) != "null" {
			filters = []byte(t.EventFilters)
		}
		trig, err := q.CreateAutopilotTrigger(ctx, db.CreateAutopilotTriggerParams{
			AutopilotID: row.ID, Kind: t.Kind, Enabled: t.Enabled,
			CronExpression: cron, Timezone: tz, WebhookToken: token, Label: label,
			Provider: provider, EventFilters: filters,
			PublishedByType: pgtype.Text{String: "member", Valid: true},
			PublishedByID:   st.env.ImporterID,
			CreatedByType:   pgtype.Text{String: "member", Valid: true},
			CreatedByID:     st.env.ImporterID,
		})
		if err != nil {
			return err
		}
		if signingSecret.Valid && signingSecret.String != "" {
			if _, err := q.SetAutopilotTriggerSigningSecret(ctx, db.SetAutopilotTriggerSigningSecretParams{
				ID: trig.ID, SigningSecret: signingSecret,
			}); err != nil {
				return err
			}
		}
	}
	for i, s := range ap.Subscribers {
		if s.UserType == "member" && !st.isMember(ctx, q, s.UserID) {
			st.dropMember("autopilot", ap.SourceID, fmt.Sprintf("subscribers[%d]", i), s.UserID)
			continue
		}
		uid, err := parseUUID(s.UserID)
		if err != nil {
			continue
		}
		_ = q.AddAutopilotSubscriber(ctx, db.AddAutopilotSubscriberParams{
			AutopilotID: row.ID, UserType: s.UserType, UserID: uid,
		})
	}
	for i, c := range ap.Collaborators {
		if c.UserType == "member" && !st.isMember(ctx, q, c.UserID) {
			st.dropMember("autopilot", ap.SourceID, fmt.Sprintf("collaborators[%d]", i), c.UserID)
			continue
		}
		uid, err := parseUUID(c.UserID)
		if err != nil {
			continue
		}
		_, _ = q.AddAutopilotCollaborator(ctx, db.AddAutopilotCollaboratorParams{
			AutopilotID: row.ID, UserType: c.UserType, UserID: uid, GrantedBy: st.env.ImporterID,
		})
	}
	return nil
}

func generateImportWebhookToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "awt_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func (st *importState) importQuickActions(ctx context.Context, q *db.Queries, dry bool) ([]ConfigImportItem, error) {
	var items []ConfigImportItem
	for _, qa := range st.req.Bundle.Entities.QuickActions {
		if qa.Assignee == nil {
			items = append(items, ConfigImportItem{SourceID: qa.SourceID, Name: qa.Name, Action: ActionSkipped, Reason: "assignee_unmapped"})
			continue
		}
		kind := "agent"
		if qa.Assignee.Type == "squad" {
			kind = "squad"
		}
		tid, ok := st.get(kind, qa.Assignee.ID)
		if !ok {
			st.dropRef("quick_action", qa.SourceID, "assignee", kind, qa.Assignee.ID, "skipped")
			items = append(items, ConfigImportItem{SourceID: qa.SourceID, Name: qa.Name, Action: ActionSkipped, Reason: "assignee_unmapped"})
			continue
		}
		existing, err := q.GetEarliestQuickActionByName(ctx, db.GetEarliestQuickActionByNameParams{
			WorkspaceID: st.env.TargetID, Name: qa.Name,
		})
		exists := err == nil
		if err != nil && !isNoRows(err) {
			return items, err
		}
		action, _, cerr := st.resolveConflict(qa.Name, exists)
		if cerr != nil {
			items = append(items, ConfigImportItem{SourceID: qa.SourceID, Name: qa.Name, Action: ActionFailed, Reason: "exists"})
			return items, cerr
		}
		item := ConfigImportItem{SourceID: qa.SourceID, Name: qa.Name, Action: action}
		writeQA := func(name string, id pgtype.UUID, isNew bool) (string, error) {
			if dry {
				if isNew {
					return st.newID(), nil
				}
				return uuidString(id), nil
			}
			assignee, _ := parseUUID(tid)
			vis := qa.Visibility
			if vis == "" {
				vis = "public"
			}
			if isNew {
				row, err := q.CreateQuickAction(ctx, db.CreateQuickActionParams{
					WorkspaceID: st.env.TargetID, Name: name, Description: qa.Description,
					AssigneeType: qa.Assignee.Type, AssigneeID: assignee, Prompt: qa.Prompt,
					Visibility: vis, CreatedByType: "member", CreatedByID: st.env.ImporterID,
				})
				if err != nil {
					return "", err
				}
				if qa.Status != "" && qa.Status != "active" {
					_, _ = q.UpdateQuickAction(ctx, db.UpdateQuickActionParams{
						ID: row.ID, WorkspaceID: st.env.TargetID, Status: pgtype.Text{String: qa.Status, Valid: true},
					})
				}
				return uuidString(row.ID), nil
			}
			_, err := q.UpdateQuickAction(ctx, db.UpdateQuickActionParams{
				ID: id, WorkspaceID: st.env.TargetID,
				Name:         pgtype.Text{String: name, Valid: true},
				Description:  pgtype.Text{String: qa.Description, Valid: true},
				AssigneeType: pgtype.Text{String: qa.Assignee.Type, Valid: true},
				AssigneeID:   assignee, Prompt: pgtype.Text{String: qa.Prompt, Valid: true},
				Visibility: pgtype.Text{String: vis, Valid: true},
				Status:     pgtype.Text{String: qa.Status, Valid: qa.Status != ""},
			})
			if err != nil {
				return "", err
			}
			return uuidString(id), nil
		}
		switch action {
		case ActionSkipped:
			item.TargetID = uuidString(existing.ID)
			item.Reason = "exists"
			st.put("quick_action", qa.SourceID, item.TargetID)
		case ActionUpdated:
			id, err := writeQA(qa.Name, existing.ID, false)
			if err != nil {
				return items, err
			}
			item.TargetID = id
			st.put("quick_action", qa.SourceID, id)
		case ActionRenamed:
			final := nextRenamed(qa.Name, 2)
			item.Name = final
			id, err := writeQA(final, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = id
			st.put("quick_action", qa.SourceID, id)
		default:
			id, err := writeQA(qa.Name, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = id
			st.put("quick_action", qa.SourceID, id)
		}
		items = append(items, item)
	}
	return items, nil
}

func (st *importState) importIssueViews(ctx context.Context, q *db.Queries, dry bool) ([]ConfigImportItem, error) {
	var items []ConfigImportItem
	for _, v := range st.req.Bundle.Entities.IssueViews {
		if v.ScopeType == "project" && (v.ScopeID == nil || *v.ScopeID == "") {
			items = append(items, ConfigImportItem{SourceID: v.SourceID, Name: v.Name, Action: ActionSkipped, Reason: "scope_unmapped"})
			continue
		}
		var mappedScope pgtype.UUID
		if v.ScopeID != nil && *v.ScopeID != "" {
			if v.ScopeType == "project" {
				tid, ok := st.get("project", *v.ScopeID)
				if !ok {
					st.dropRef("issue_view", v.SourceID, "scope_id", "project", *v.ScopeID, "skipped")
					items = append(items, ConfigImportItem{SourceID: v.SourceID, Name: v.Name, Action: ActionSkipped, Reason: "scope_unmapped"})
					continue
				}
				mappedScope, _ = parseUUID(tid)
			} else {
				mappedScope, _ = parseUUID(*v.ScopeID)
			}
		}
		existing, err := q.GetWorkspaceIssueViewByIdentity(ctx, db.GetWorkspaceIssueViewByIdentityParams{
			WorkspaceID: st.env.TargetID, ScopeType: v.ScopeType, ScopeID: mappedScope, Name: v.Name,
		})
		exists := err == nil
		if err != nil && !isNoRows(err) {
			return items, err
		}
		action, _, cerr := st.resolveConflict(v.Name, exists)
		if cerr != nil {
			items = append(items, ConfigImportItem{SourceID: v.SourceID, Name: v.Name, Action: ActionFailed, Reason: "exists"})
			return items, cerr
		}
		item := ConfigImportItem{SourceID: v.SourceID, Name: v.Name, Action: action}
		query := remapViewQuery(ctx, q, v.Query, v.SourceID, st)
		display := []byte(v.Display)
		if len(display) == 0 {
			display = []byte("{}")
		}
		writeView := func(name string, id pgtype.UUID, isNew bool) (string, error) {
			if dry {
				if isNew {
					return st.newID(), nil
				}
				return uuidString(id), nil
			}
			variant := pgtype.Text{}
			if v.ScopeVariant != nil {
				variant = pgtype.Text{String: *v.ScopeVariant, Valid: true}
			}
			defVer := v.DefinitionVersion
			if defVer == 0 {
				defVer = 1
			}
			visibility := v.Visibility
			if visibility == "" {
				visibility = "private"
			}
			if isNew {
				row, err := q.CreateIssueView(ctx, db.CreateIssueViewParams{
					WorkspaceID: st.env.TargetID, OwnerID: st.env.ImporterID, Name: name,
					ScopeType: v.ScopeType, ScopeID: mappedScope, ScopeVariant: variant,
					Visibility: visibility, DefinitionVersion: defVer, Query: query, Display: display,
				})
				if err != nil {
					return "", err
				}
				return uuidString(row.ID), nil
			}
			_, err := q.UpdateIssueView(ctx, db.UpdateIssueViewParams{
				ID: id, WorkspaceID: st.env.TargetID, Name: name, Visibility: visibility,
				ScopeVariant: variant, Query: query, Display: display, Revision: existing.Revision,
			})
			if err != nil {
				return "", err
			}
			return uuidString(id), nil
		}
		switch action {
		case ActionSkipped:
			item.TargetID = uuidString(existing.ID)
			item.Reason = "exists"
			st.put("issue_view", v.SourceID, item.TargetID)
		case ActionUpdated:
			id, err := writeView(v.Name, existing.ID, false)
			if err != nil {
				return items, err
			}
			item.TargetID = id
			st.put("issue_view", v.SourceID, id)
		case ActionRenamed:
			final := nextRenamed(v.Name, 2)
			item.Name = final
			id, err := writeView(final, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = id
			st.put("issue_view", v.SourceID, id)
		default:
			id, err := writeView(v.Name, pgtype.UUID{}, true)
			if err != nil {
				return items, err
			}
			item.TargetID = id
			st.put("issue_view", v.SourceID, id)
		}
		items = append(items, item)
	}
	return items, nil
}

func remapViewQuery(ctx context.Context, q *db.Queries, raw json.RawMessage, sourceID string, st *importState) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return []byte(raw)
	}
	remapStringSlice := func(key, kind string) {
		arr, ok := obj[key].([]any)
		if !ok {
			return
		}
		out := make([]any, 0, len(arr))
		for _, v := range arr {
			s, ok := v.(string)
			if !ok {
				out = append(out, v)
				continue
			}
			if tid, ok := st.get(kind, s); ok {
				out = append(out, tid)
			}
		}
		obj[key] = out
	}
	remapStringSlice("labelFilters", "label")
	remapStringSlice("projectFilters", "project")
	remapActorSlice := func(key string) {
		arr, ok := obj[key].([]any)
		if !ok {
			return
		}
		out := make([]any, 0, len(arr))
		for i, v := range arr {
			m, ok := v.(map[string]any)
			if !ok {
				out = append(out, v)
				continue
			}
			typ, _ := m["type"].(string)
			id, _ := m["id"].(string)
			field := fmt.Sprintf("query.%s[%d]", key, i)
			switch typ {
			case "agent", "squad":
				if tid, ok := st.get(typ, id); ok {
					m["id"] = tid
					out = append(out, m)
				} else {
					st.dropRef("issue_view", sourceID, field, typ, id, "dropped")
				}
			case "member":
				if st.isMember(ctx, q, id) {
					out = append(out, m)
				} else {
					st.dropMember("issue_view", sourceID, field, id)
				}
			default:
				// Unknown actor kinds cannot be remapped; dropping them avoids
				// writing a source-workspace ID into the target view.
				st.dropRef("issue_view", sourceID, field, typ, id, "dropped")
			}
		}
		obj[key] = out
	}
	remapActorSlice("assigneeFilters")
	remapActorSlice("creatorFilters")
	if pf, ok := obj["propertyFilters"].(map[string]any); ok {
		out := map[string]any{}
		for k, v := range pf {
			if tid, ok := st.get("property", k); ok {
				out[tid] = v
			}
		}
		obj["propertyFilters"] = out
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return []byte(raw)
	}
	return b
}
