package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type TransferImportEnv struct {
	Queries    *db.Queries
	TxStarter  TxStarter
	TargetID   pgtype.UUID
	TargetSlug string
	ImporterID pgtype.UUID
	PublicURL  string
	Storage    storage.Storage
}

func ImportTransferConfig(ctx context.Context, env TransferImportEnv, req TransferConfigRequest) (*TransferConfigReport, error) {
	if req.Manifest.Format != "" && req.Manifest.Format != TransferBundleFormat {
		return nil, &ImportError{Status: 400, Code: "transfer_bundle_invalid", Msg: "manifest format is not multica.workspace-transfer"}
	}
	if req.Manifest.Format != "" && req.Manifest.SchemaVersion != 0 && req.Manifest.SchemaVersion != TransferBundleSchemaVersion {
		return nil, &ImportError{Status: 400, Code: "transfer_bundle_version_unsupported", Msg: "unsupported transfer schema_version"}
	}
	if req.DryRun == nil {
		return nil, &ImportError{Status: 400, Code: "transfer_bundle_invalid", Msg: "dry_run is required"}
	}

	peopleMap, peopleRows := mapPeople(ctx, env, req)
	rewriteBundleMembers(&req.Config, peopleMap, req.Manifest.Source.ExportedBy, uuidString(env.ImporterID))
	if req.Config.Source.ExportedBy == "" {
		req.Config.Source.ExportedBy = uuidString(env.ImporterID)
	}

	cfgReq := ConfigImportRequest{
		Bundle:     req.Config,
		DryRun:     req.DryRun,
		OnConflict: req.OnConflict,
	}
	cfgReport, err := ImportWorkspaceConfig(ctx, ConfigImportEnv{
		Queries:    env.Queries,
		TxStarter:  env.TxStarter,
		TargetID:   env.TargetID,
		TargetSlug: env.TargetSlug,
		ImporterID: env.ImporterID,
		PublicURL:  env.PublicURL,
	}, cfgReq)
	if err != nil {
		return nil, err
	}

	profileItems, err := importRuntimeProfiles(ctx, env, req, *req.DryRun)
	if err != nil {
		return nil, err
	}
	pinnedItems, err := importPinnedAgents(ctx, env, req, *req.DryRun)
	if err != nil {
		return nil, err
	}

	report := &TransferConfigReport{
		ConfigReport:   cfgReport,
		PeopleMap:      peopleRows,
		Profiles:       profileItems,
		PinnedAgents:   pinnedItems,
		RuntimesToBind: buildRuntimesToBind(ctx, env, req, cfgReport, profileItems),
		ExportGaps:     req.Manifest.ExportGaps,
	}
	return report, nil
}

func mapPeople(ctx context.Context, env TransferImportEnv, req TransferConfigRequest) (map[string]string, []TransferPeopleMapRow) {
	out := map[string]string{}
	rows := []TransferPeopleMapRow{}
	exporter := req.Manifest.Source.ExportedBy
	importer := uuidString(env.ImporterID)
	if exporter != "" {
		out[exporter] = importer
	}
	for _, p := range req.People {
		row := TransferPeopleMapRow{SourceUserID: p.SourceUserID}
		if p.SourceUserID == exporter {
			row.Mapped = true
			row.TargetUserID = importer
			rows = append(rows, row)
			continue
		}
		email := strings.TrimSpace(p.Email)
		if email == "" {
			row.Reason = "member_unmapped"
			rows = append(rows, row)
			continue
		}
		m, err := env.Queries.GetWorkspaceMemberByEmail(ctx, db.GetWorkspaceMemberByEmailParams{
			WorkspaceID: env.TargetID,
			Email:       email,
		})
		if err != nil {
			row.Reason = "member_unmapped"
			rows = append(rows, row)
			continue
		}
		tid := uuidString(m.UserID)
		out[p.SourceUserID] = tid
		row.Mapped = true
		row.TargetUserID = tid
		rows = append(rows, row)
	}
	return out, rows
}

func rewriteBundleMembers(bundle *ConfigBundle, peopleMap map[string]string, exporter, importer string) {
	mapID := func(id string) string {
		if id == "" {
			return id
		}
		if id == exporter && importer != "" {
			return importer
		}
		if t, ok := peopleMap[id]; ok {
			return t
		}
		return id
	}
	for i := range bundle.Entities.Agents {
		for j := range bundle.Entities.Agents[i].InvocationTargets {
			t := &bundle.Entities.Agents[i].InvocationTargets[j]
			if t.TargetType == "member" && t.TargetID != nil {
				mapped := mapID(*t.TargetID)
				t.TargetID = &mapped
			}
		}
	}
	for i := range bundle.Entities.Squads {
		for j := range bundle.Entities.Squads[i].Members {
			m := &bundle.Entities.Squads[i].Members[j]
			if m.MemberType == "member" {
				m.MemberID = mapID(m.MemberID)
			}
		}
	}
	for i := range bundle.Entities.Autopilots {
		for j := range bundle.Entities.Autopilots[i].Subscribers {
			s := &bundle.Entities.Autopilots[i].Subscribers[j]
			if s.UserType == "member" {
				s.UserID = mapID(s.UserID)
			}
		}
		for j := range bundle.Entities.Autopilots[i].Collaborators {
			c := &bundle.Entities.Autopilots[i].Collaborators[j]
			if c.UserType == "member" {
				c.UserID = mapID(c.UserID)
			}
		}
	}
	for i := range bundle.Entities.Projects {
		if bundle.Entities.Projects[i].Lead != nil && bundle.Entities.Projects[i].Lead.Type == "member" {
			bundle.Entities.Projects[i].Lead.ID = mapID(bundle.Entities.Projects[i].Lead.ID)
		}
	}
	for i := range bundle.Entities.QuickActions {
		if bundle.Entities.QuickActions[i].Assignee != nil && bundle.Entities.QuickActions[i].Assignee.Type == "member" {
			bundle.Entities.QuickActions[i].Assignee.ID = mapID(bundle.Entities.QuickActions[i].Assignee.ID)
		}
	}
}

func importRuntimeProfiles(ctx context.Context, env TransferImportEnv, req TransferConfigRequest, dry bool) ([]ConfigImportItem, error) {
	onConflict := req.OnConflict
	if onConflict == "" {
		onConflict = ConflictFail
	}
	items := []ConfigImportItem{}
	for _, p := range req.RuntimeProfiles.Profiles {
		existing, err := env.Queries.GetRuntimeProfileByDisplayName(ctx, db.GetRuntimeProfileByDisplayNameParams{
			WorkspaceID: env.TargetID,
			DisplayName: p.DisplayName,
		})
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("lookup runtime profile: %w", err)
		}
		item := ConfigImportItem{SourceID: p.SourceID, Name: p.DisplayName}
		if exists {
			switch onConflict {
			case ConflictSkip:
				item.Action = ActionSkipped
				item.TargetID = uuidString(existing.ID)
				item.Reason = "already exists"
				items = append(items, item)
				continue
			case ConflictFail:
				return nil, &ImportError{Status: 409, Code: "config_import_conflict", Msg: "runtime profile already exists: " + p.DisplayName}
			case ConflictRename:
				p.DisplayName = p.DisplayName + " (imported)"
				item.Action = ActionRenamed
			case ConflictOverwrite:
				item.Action = ActionUpdated
				item.TargetID = uuidString(existing.ID)
				if dry {
					items = append(items, item)
					continue
				}
				args, _ := json.Marshal(p.FixedArgs)
				args = restoreSecretArgs(args, existing.FixedArgs)
				_, err := env.Queries.UpdateRuntimeProfile(ctx, db.UpdateRuntimeProfileParams{
					DisplayName: pgtype.Text{String: p.DisplayName, Valid: true},
					CommandName: pgtype.Text{String: p.CommandName, Valid: p.CommandName != ""},
					Description: pgtype.Text{String: p.Description, Valid: p.Description != ""},
					FixedArgs:   args,
					Enabled:     pgtype.Bool{Bool: p.Enabled, Valid: true},
					ID:          existing.ID,
					WorkspaceID: env.TargetID,
				})
				if err != nil {
					return nil, fmt.Errorf("update runtime profile: %w", err)
				}
				items = append(items, item)
				continue
			}
		} else {
			item.Action = ActionCreated
		}
		if dry {
			items = append(items, item)
			continue
		}
		args, _ := json.Marshal(p.FixedArgs)
		args = restoreSecretArgs(args, nil)
		vis := p.Visibility
		if vis == "" {
			vis = "workspace"
		}
		created, err := env.Queries.CreateRuntimeProfile(ctx, db.CreateRuntimeProfileParams{
			WorkspaceID:    env.TargetID,
			DisplayName:    p.DisplayName,
			ProtocolFamily: p.ProtocolFamily,
			CommandName:    p.CommandName,
			Description:    pgtype.Text{String: p.Description, Valid: p.Description != ""},
			FixedArgs:      args,
			Visibility:     vis,
			CreatedBy:      env.ImporterID,
			Enabled:        p.Enabled,
		})
		if err != nil {
			return nil, fmt.Errorf("create runtime profile: %w", err)
		}
		item.TargetID = uuidString(created.ID)
		items = append(items, item)
	}
	return items, nil
}

func importPinnedAgents(ctx context.Context, env TransferImportEnv, req TransferConfigRequest, dry bool) ([]ConfigImportItem, error) {
	items := []ConfigImportItem{}
	for _, pin := range req.Preferences.PinnedAgents {
		item := ConfigImportItem{SourceID: pin.AgentID, Name: pin.AgentID, Action: ActionCreated}
		agent, err := env.Queries.GetUserAgentByName(ctx, db.GetUserAgentByNameParams{
			WorkspaceID: env.TargetID,
			Name:        pin.AgentID, // may be source id; try name via config report instead
		})
		if err != nil {
			// Prefer mapping by imported agent name from config entities.
			mapped := false
			for _, a := range req.Config.Entities.Agents {
				if a.SourceID == pin.AgentID {
					agent, err = env.Queries.GetUserAgentByName(ctx, db.GetUserAgentByNameParams{
						WorkspaceID: env.TargetID,
						Name:        a.Name,
					})
					if err == nil {
						mapped = true
					}
					break
				}
			}
			if !mapped {
				item.Action = ActionSkipped
				item.Reason = "agent_unmapped"
				items = append(items, item)
				continue
			}
		}
		item.TargetID = uuidString(agent.ID)
		if dry {
			items = append(items, item)
			continue
		}
		_, err = env.Queries.CreateChatPinnedAgent(ctx, db.CreateChatPinnedAgentParams{
			WorkspaceID: env.TargetID,
			UserID:      env.ImporterID,
			AgentID:     agent.ID,
			Position:    pin.Position,
		})
		if err != nil {
			return nil, fmt.Errorf("pin agent: %w", err)
		}
		items = append(items, item)
	}
	return items, nil
}

func buildRuntimesToBind(ctx context.Context, env TransferImportEnv, req TransferConfigRequest, cfg *ConfigImportReport, profiles []ConfigImportItem) []TransferRuntimeBind {
	autoBind := req.AutoBindRuntimes == nil || *req.AutoBindRuntimes
	dry := req.DryRun == nil || *req.DryRun
	return planTransferRuntimeBinds(ctx, env, req, cfg, profiles, dry, autoBind)
}

// transferRuntimeCandidate is one bindable runtime in the target workspace.
// RuntimeUUID is what the write needs; RuntimeID is the string the report and
// the client speak in.
type transferRuntimeCandidate struct {
	RuntimeUUID pgtype.UUID
	RuntimeID   string
	Name        string
	Provider    string
	RuntimeMode string
	ProfileName string
}

// listTransferRuntimeCandidates reads the runtimes the importer may bind to.
// The query already applies the visibility rule (own runtime, or one a
// teammate shared), so no candidate here needs a second permission check.
func listTransferRuntimeCandidates(ctx context.Context, env TransferImportEnv) []transferRuntimeCandidate {
	rows, err := env.Queries.ListVisibleRuntimesForTransfer(ctx, db.ListVisibleRuntimesForTransferParams{
		WorkspaceID: env.TargetID,
		OwnerID:     env.ImporterID,
	})
	if err != nil {
		return []transferRuntimeCandidate{}
	}
	out := make([]transferRuntimeCandidate, 0, len(rows))
	for _, rt := range rows {
		name := rt.Name
		if rt.CustomName.Valid && rt.CustomName.String != "" {
			name = rt.CustomName.String
		}
		out = append(out, transferRuntimeCandidate{
			RuntimeUUID: rt.ID,
			RuntimeID:   uuidString(rt.ID),
			Name:        name,
			Provider:    rt.Provider,
			RuntimeMode: rt.RuntimeMode,
			ProfileName: rt.ProfileDisplayName,
		})
	}
	return out
}

// matchTransferRuntimeCandidates applies the bind rule: a source runtime may
// only be replaced by one that runs the same provider, in the same
// runtime_mode, through the same custom profile. A hint with no profile never
// matches a custom-profile runtime and vice versa — the profile is what decides
// which command the daemon executes, so a provider-only match would silently
// swap the user's CLI.
func matchTransferRuntimeCandidates(candidates []transferRuntimeCandidate, hint TransferRuntimeHint, profileName string) []transferRuntimeCandidate {
	out := []transferRuntimeCandidate{}
	if hint.Provider == "" {
		return out
	}
	for _, c := range candidates {
		if c.Provider != hint.Provider {
			continue
		}
		if hint.RuntimeMode != "" && c.RuntimeMode != hint.RuntimeMode {
			continue
		}
		if c.ProfileName != profileName {
			continue
		}
		out = append(out, c)
	}
	return out
}

// planTransferRuntimeBinds turns the imported agents into per-agent outcomes:
// exactly one candidate binds automatically, several are left for the Desktop
// card to pick, none is reported with a reason instead of being skipped in
// silence (DENE-364). dry keeps the outcomes identical but writes nothing, so
// the preview says what apply would do.
func planTransferRuntimeBinds(ctx context.Context, env TransferImportEnv, req TransferConfigRequest, cfg *ConfigImportReport, profiles []ConfigImportItem, dry, autoBind bool) []TransferRuntimeBind {
	out := []TransferRuntimeBind{}
	if cfg == nil {
		return out
	}
	candidates := listTransferRuntimeCandidates(ctx, env)
	profileNameBySource := map[string]string{}
	for _, p := range profiles {
		if p.SourceID != "" {
			profileNameBySource[p.SourceID] = p.Name
		}
	}
	hintByRuntime := map[string]TransferRuntimeHint{}
	hintByName := map[string]TransferRuntimeHint{}
	for _, h := range req.RuntimeProfiles.RuntimesHint {
		if h.SourceRuntimeID != "" {
			hintByRuntime[h.SourceRuntimeID] = h
		}
		if h.DisplayName != "" {
			hintByName[h.DisplayName] = h
		}
	}
	agentBySource := map[string]ConfigAgent{}
	for _, a := range req.Config.Entities.Agents {
		agentBySource[a.SourceID] = a
	}

	for _, batch := range cfg.Batches {
		if batch.EntityType != "agents" {
			continue
		}
		for _, item := range batch.Items {
			if item.Action == ActionFailed {
				continue
			}
			bind := TransferRuntimeBind{AgentTargetID: item.TargetID, AgentName: item.Name}
			source := agentBySource[item.SourceID]
			hint, hasHint := hintByRuntime[source.RuntimeSourceID]
			if !hasHint && source.Name != "" {
				// Bundles exported before runtime_source_id existed carry no
				// agent→runtime link. Their only join is the runtime's display
				// name, so keep that fallback rather than reporting nothing for
				// a bundle the user already has on disk.
				hint, hasHint = hintByName[source.Name]
			}
			if hasHint {
				bind.Provider = hint.Provider
				bind.RuntimeMode = hint.RuntimeMode
				bind.ProfileName = profileNameBySource[hint.ProfileSourceID]
			}
			matches := matchTransferRuntimeCandidates(candidates, hint, bind.ProfileName)
			bind.CandidateIDs = make([]string, 0, len(matches))
			bind.Candidates = make([]TransferRuntimeCandidate, 0, len(matches))
			for _, m := range matches {
				bind.CandidateIDs = append(bind.CandidateIDs, m.RuntimeID)
				bind.Candidates = append(bind.Candidates, TransferRuntimeCandidate{
					RuntimeID:   m.RuntimeID,
					Name:        m.Name,
					Provider:    m.Provider,
					RuntimeMode: m.RuntimeMode,
					ProfileName: m.ProfileName,
				})
			}

			if item.TargetID == "" {
				bind.Action = TransferBindAgentMissing
				bind.ReasonCode = TransferBindReasonAgentMissing
				bind.Reason = "the agent was not created in the target workspace"
				out = append(out, bind)
				continue
			}
			// A dry run resolves new agents to synthetic ids that have no row
			// yet, so a failed load only means "not created yet" there. An
			// existing agent does load in both modes, which is what keeps the
			// preview's already_bound rows honest.
			existing, err := loadTransferAgent(ctx, env, item.TargetID)
			hasExisting := err == nil
			if !dry && !hasExisting {
				bind.Action = TransferBindAgentMissing
				bind.ReasonCode = TransferBindReasonAgentMissing
				bind.Reason = "the agent is not in the target workspace"
				out = append(out, bind)
				continue
			}
			if hasExisting && existing.RuntimeID.Valid {
				// Already bound — by an earlier import or by the user.
				// Auto-bind must not move an agent off a runtime a human
				// chose, whatever the candidate list says.
				bind.Action = TransferBindAlreadyBound
				bind.BoundRuntimeID = uuidString(existing.RuntimeID)
				bind.BoundRuntimeName = transferRuntimeNameByID(candidates, bind.BoundRuntimeID)
				out = append(out, bind)
				continue
			}

			switch {
			case !hasHint:
				bind.Action = TransferBindNoCandidate
				bind.ReasonCode = TransferBindReasonSourceUnknown
				bind.Reason = "the bundle does not record which runtime this agent used; re-export with a current CLI"
			case len(matches) == 0:
				bind.Action = TransferBindNoCandidate
				bind.ReasonCode = TransferBindReasonNoRuntime
				bind.Reason = transferNoRuntimeReason(hint, bind.ProfileName)
			case len(matches) > 1 || !autoBind:
				bind.Action = TransferBindCandidates
			default:
				bind.Action = TransferBindBound
				bind.AutoBind = true
				bind.BoundRuntimeID = matches[0].RuntimeID
				bind.BoundRuntimeName = matches[0].Name
				if !dry && hasExisting {
					if err := bindTransferRuntime(ctx, env, existing, matches[0]); err != nil {
						bind.Action = TransferBindFailed
						bind.AutoBind = false
						bind.BoundRuntimeID = ""
						bind.BoundRuntimeName = ""
						bind.ReasonCode = TransferBindReasonBindFailed
						bind.Reason = err.Error()
					}
				}
			}
			out = append(out, bind)
		}
	}
	return out
}

// transferRuntimeNameByID names a runtime the report already saw, so an
// "already bound" row reads as the same machine label the candidates use.
func transferRuntimeNameByID(candidates []transferRuntimeCandidate, runtimeID string) string {
	for _, c := range candidates {
		if c.RuntimeID == runtimeID {
			return c.Name
		}
	}
	return ""
}

// bindTransferRuntime is the single writer behind both the automatic and the
// manual transfer bind. It performs the same narrow rebind the live UpdateAgent
// path does for a runtime change — including the model reset that keeps a
// runtime-native model id off a runtime that cannot serve it. Callers own the
// authorization decision (workspace membership plus runtime visibility), so
// this function only writes.
func bindTransferRuntime(ctx context.Context, env TransferImportEnv, existing db.Agent, rt transferRuntimeCandidate) error {
	params := db.UpdateAgentParams{
		ID:          existing.ID,
		RuntimeID:   rt.RuntimeUUID,
		RuntimeMode: pgtype.Text{String: rt.RuntimeMode, Valid: rt.RuntimeMode != ""},
	}
	if existing.Model.Valid && agentpkg.ModelKnownIncompatibleWithProvider(rt.Provider, existing.Model.String) {
		params.Model = pgtype.Text{String: "", Valid: true}
	}
	if _, err := env.Queries.UpdateAgent(ctx, params); err != nil {
		return fmt.Errorf("bind agent runtime: %w", err)
	}
	return nil
}

// loadTransferAgent re-reads an imported agent so the bind rule can see a
// binding an earlier import or the user already made.
func loadTransferAgent(ctx context.Context, env TransferImportEnv, agentTargetID string) (db.Agent, error) {
	agentUUID, err := util.ParseUUID(agentTargetID)
	if err != nil {
		return db.Agent{}, err
	}
	return env.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: env.TargetID,
	})
}

// BindTransferAgentRuntime binds one already-authorized agent→runtime pair for
// the manual transfer endpoint. The caller has loaded both rows in the target
// workspace and applied canUseRuntimeForAgent; this function only writes.
func BindTransferAgentRuntime(ctx context.Context, env TransferImportEnv, existing db.Agent, runtime db.AgentRuntime) error {
	return bindTransferRuntime(ctx, env, existing, transferRuntimeCandidate{
		RuntimeUUID: runtime.ID,
		RuntimeID:   uuidString(runtime.ID),
		Provider:    runtime.Provider,
		RuntimeMode: runtime.RuntimeMode,
	})
}

// transferNoRuntimeReason is the English fallback for a zero-candidate row. The
// client localizes reason_code and names the provider/mode/profile from the
// row's own fields; this string is what a client that does not know the code
// still shows.
func transferNoRuntimeReason(hint TransferRuntimeHint, profileName string) string {
	detail := []string{}
	if hint.Provider != "" {
		detail = append(detail, "provider="+hint.Provider)
	}
	if hint.RuntimeMode != "" {
		detail = append(detail, "runtime_mode="+hint.RuntimeMode)
	}
	if profileName != "" {
		detail = append(detail, "profile="+profileName)
	}
	if len(detail) == 0 {
		return "no bindable runtime is visible in this workspace"
	}
	return "no runtime matching " + strings.Join(detail, ", ") + " is visible in this workspace; connect this machine's daemon to the target instance first"
}

func ImportTransferConversations(ctx context.Context, env TransferImportEnv, req TransferConversationsRequest) (*TransferConversationsReport, error) {
	if req.DryRun == nil {
		return nil, &ImportError{Status: 400, Code: "transfer_bundle_invalid", Msg: "dry_run is required"}
	}
	dry := *req.DryRun
	report := &TransferConversationsReport{Applied: !dry}
	wsID := uuidString(env.TargetID)

	skipSessions := map[string]bool{}
	agentTarget := map[string]pgtype.UUID{}
	projectTarget := map[string]pgtype.UUID{}

	for _, sess := range req.Sessions {
		agentUUID, ok, unmapped := resolveTransferAgent(ctx, env, req.Refs, sess.AgentID)
		if !ok {
			skipSessions[sess.SourceID] = true
			report.SessionsSkipped++
			if unmapped {
				report.AgentUnmapped = append(report.AgentUnmapped, UnmappedRef{
					Entity: "chat_session", SourceID: sess.SourceID, Field: "agent_id",
					RefType: "agent", RefID: sess.AgentID, Resolution: "skipped_session",
				})
			}
			continue
		}
		agentTarget[sess.SourceID] = agentUUID
		if sess.ProjectID != nil && *sess.ProjectID != "" {
			pid, mapped := resolveTransferProject(ctx, env, req.Refs, *sess.ProjectID)
			if mapped {
				projectTarget[sess.SourceID] = pid
			} else {
				report.ProjectUnmapped = append(report.ProjectUnmapped, UnmappedRef{
					Entity: "chat_session", SourceID: sess.SourceID, Field: "project_id",
					RefType: "project", RefID: *sess.ProjectID, Resolution: "nulled",
				})
			}
		}
		if sess.ChannelType != nil && *sess.ChannelType != "" {
			report.ChannelDetached++
		}
		if dry {
			report.SessionsCreated++
			continue
		}
		targetID := TransferChatSessionID(wsID, sess.SourceID)
		createdAt := parseTransferTime(sess.CreatedAt)
		updatedAt := parseTransferTime(sess.UpdatedAt)
		if !updatedAt.Valid {
			updatedAt = createdAt
		}
		status := sess.Status
		if status == "" {
			status = "active"
		}
		var pinned pgtype.Timestamptz
		if sess.PinnedAt != nil && *sess.PinnedAt != "" {
			pinned = parseTransferTime(*sess.PinnedAt)
		}
		var explicit pgtype.Timestamptz
		if sess.ExplicitlyCreatedAt != nil && *sess.ExplicitlyCreatedAt != "" {
			explicit = parseTransferTime(*sess.ExplicitlyCreatedAt)
		}
		var project pgtype.UUID
		if p, ok := projectTarget[sess.SourceID]; ok {
			project = p
		}
		n, err := env.Queries.TransferInsertChatSession(ctx, db.TransferInsertChatSessionParams{
			ID:                  pgUUID(targetID),
			WorkspaceID:         env.TargetID,
			AgentID:             agentUUID,
			CreatorID:           env.ImporterID,
			Title:               sess.Title,
			Status:              status,
			PinnedAt:            pinned,
			IsAgentIntro:        sess.IsAgentIntro,
			ExplicitlyCreatedAt: explicit,
			ProjectID:           project,
			CreatedAt:           createdAt,
			UpdatedAt:           updatedAt,
		})
		if err != nil {
			return nil, fmt.Errorf("insert chat session: %w", err)
		}
		if n == 0 {
			report.SessionsSkipped++
		} else {
			report.SessionsCreated++
		}
	}

	for _, msg := range req.Messages {
		if skipSessions[msg.ChatSessionID] {
			report.MessagesSkipped++
			continue
		}
		if _, ok := agentTarget[msg.ChatSessionID]; !ok && !dry {
			// Session may already exist from a prior import of this shard.
			targetSess := TransferChatSessionID(wsID, msg.ChatSessionID)
			if _, err := env.Queries.GetTransferChatSession(ctx, db.GetTransferChatSessionParams{
				ID: pgUUID(targetSess), WorkspaceID: env.TargetID,
			}); err != nil {
				report.MessagesSkipped++
				continue
			}
		}
		if dry {
			report.MessagesCreated++
			continue
		}
		targetMsg := TransferChatMessageID(wsID, msg.SourceID)
		targetSess := TransferChatSessionID(wsID, msg.ChatSessionID)
		kind := msg.MessageKind
		if kind == "" {
			kind = "message"
		}
		var failure pgtype.Text
		if msg.FailureReason != nil {
			failure = pgtype.Text{String: *msg.FailureReason, Valid: true}
		}
		var elapsed pgtype.Int8
		if msg.ElapsedMs != nil {
			elapsed = pgtype.Int8{Int64: *msg.ElapsedMs, Valid: true}
		}
		n, err := env.Queries.TransferInsertChatMessage(ctx, db.TransferInsertChatMessageParams{
			ID:            pgUUID(targetMsg),
			ChatSessionID: pgUUID(targetSess),
			Role:          msg.Role,
			Content:       msg.Content,
			MessageKind:   pgtype.Text{String: kind, Valid: true},
			FailureReason: failure,
			ElapsedMs:     elapsed,
			CreatedAt:     parseTransferTime(msg.CreatedAt),
		})
		if err != nil {
			return nil, fmt.Errorf("insert chat message: %w", err)
		}
		if n == 0 {
			report.MessagesSkipped++
		} else {
			report.MessagesCreated++
		}
	}
	report.Finalized = req.Finalize && !dry
	return report, nil
}

func resolveTransferAgent(ctx context.Context, env TransferImportEnv, refs TransferRefs, sourceAgentID string) (pgtype.UUID, bool, bool) {
	if ref, ok := refs.SystemAgents[sourceAgentID]; ok && ref.SystemKey != "" {
		a, err := env.Queries.GetAgentBySystemKey(ctx, db.GetAgentBySystemKeyParams{
			WorkspaceID: env.TargetID,
			SystemKey:   pgtype.Text{String: ref.SystemKey, Valid: true},
		})
		if err == nil {
			return a.ID, true, false
		}
		return pgtype.UUID{}, false, true
	}
	name := sourceAgentID
	if ref, ok := refs.Agents[sourceAgentID]; ok && ref.Name != "" {
		name = ref.Name
	}
	a, err := env.Queries.GetUserAgentByName(ctx, db.GetUserAgentByNameParams{
		WorkspaceID: env.TargetID,
		Name:        name,
	})
	if err != nil {
		return pgtype.UUID{}, false, true
	}
	return a.ID, true, false
}

func resolveTransferProject(ctx context.Context, env TransferImportEnv, refs TransferRefs, sourceProjectID string) (pgtype.UUID, bool) {
	title := sourceProjectID
	if ref, ok := refs.Projects[sourceProjectID]; ok && ref.Title != "" {
		title = ref.Title
	}
	p, err := env.Queries.GetEarliestProjectByTitle(ctx, db.GetEarliestProjectByTitleParams{
		WorkspaceID: env.TargetID,
		Title:       title,
	})
	if err != nil {
		return pgtype.UUID{}, false
	}
	return p.ID, true
}

func ImportTransferAttachment(ctx context.Context, env TransferImportEnv, meta TransferAttachmentMeta, body []byte) (*TransferAttachmentReport, error) {
	wsID := uuidString(env.TargetID)
	targetID := TransferAttachmentID(wsID, meta.SourceID)
	report := &TransferAttachmentReport{Applied: true, TargetID: targetID.String()}

	existing, err := env.Queries.GetAttachment(ctx, db.GetAttachmentParams{
		ID:          pgUUID(targetID),
		WorkspaceID: env.TargetID,
	})
	if err == nil && uuidString(existing.ID) != "" {
		report.Skipped = true
		report.Created = false
		report.BodyStored = existing.Url != "" && meta.BodyOmittedReason == nil
		return report, nil
	}

	url := ""
	bodyStored := false
	if len(body) > 0 && meta.BodyOmittedReason == nil {
		if env.Storage == nil {
			return nil, &ImportError{Status: 503, Code: "transfer_storage_unavailable", Msg: "file upload not configured"}
		}
		key := "workspaces/" + wsID + "/" + targetID.String() + pathExt(meta.Filename)
		u, err := env.Storage.Upload(ctx, key, body, meta.ContentType, meta.Filename)
		if err != nil {
			return nil, fmt.Errorf("store attachment: %w", err)
		}
		url = u
		bodyStored = true
	}

	var sess pgtype.UUID
	if meta.ChatSessionID != nil && *meta.ChatSessionID != "" {
		sess = pgUUID(TransferChatSessionID(wsID, *meta.ChatSessionID))
	}
	var msg pgtype.UUID
	if meta.ChatMessageID != nil && *meta.ChatMessageID != "" {
		msg = pgUUID(TransferChatMessageID(wsID, *meta.ChatMessageID))
	}
	n, err := env.Queries.TransferInsertAttachment(ctx, db.TransferInsertAttachmentParams{
		ID:            pgUUID(targetID),
		WorkspaceID:   env.TargetID,
		ChatSessionID: sess,
		ChatMessageID: msg,
		UploaderType:  "member",
		UploaderID:    env.ImporterID,
		Filename:      meta.Filename,
		Url:           url,
		ContentType:   meta.ContentType,
		SizeBytes:     meta.SizeBytes,
		CreatedAt:     parseTransferTime(meta.CreatedAt),
	})
	if err != nil {
		return nil, fmt.Errorf("insert attachment: %w", err)
	}
	if n == 0 {
		report.Skipped = true
		return report, nil
	}
	report.Created = true
	report.BodyStored = bodyStored
	if meta.BodyOmittedReason != nil {
		report.Reason = *meta.BodyOmittedReason
	}
	return report, nil
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func parseTransferTime(s string) pgtype.Timestamptz {
	s = strings.TrimSpace(s)
	if s == "" {
		return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return pgtype.Timestamptz{Time: t, Valid: true}
		}
	}
	return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
}

func pathExt(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return ""
	}
	return name[i:]
}
