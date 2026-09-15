package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type ConfigImportEnv struct {
	Queries    *db.Queries
	TxStarter  TxStarter
	TargetID   pgtype.UUID
	TargetSlug string
	ImporterID pgtype.UUID
	PublicURL  string
}

type importState struct {
	env       ConfigImportEnv
	req       ConfigImportRequest
	include   map[string]bool
	idMap     map[string]string
	members   map[string]bool
	report    *ConfigImportReport
	failFirst bool
	stopped   bool
	publicURL string
}

func ImportWorkspaceConfig(ctx context.Context, env ConfigImportEnv, req ConfigImportRequest) (*ConfigImportReport, error) {
	if req.Bundle.Format != ConfigBundleFormat {
		return nil, &ImportError{Status: 400, Code: "config_bundle_invalid", Msg: "bundle format is not multica.workspace-config"}
	}
	if req.Bundle.SchemaVersion != ConfigBundleSchemaVersion {
		return nil, &ImportError{Status: 400, Code: "config_bundle_version_unsupported", Msg: "unsupported bundle schema_version"}
	}
	if req.DryRun == nil {
		return nil, &ImportError{Status: 400, Code: "config_bundle_invalid", Msg: "dry_run is required"}
	}
	onConflict := req.OnConflict
	if onConflict == "" {
		onConflict = ConflictFail
	}
	if !ValidOnConflict(onConflict) {
		return nil, &ImportError{Status: 400, Code: "invalid_on_conflict", Msg: "on_conflict must be one of: fail, overwrite, rename, skip"}
	}
	if err := bundleContainsSecret(&req.Bundle); err != nil {
		return nil, &ImportError{Status: 400, Code: "config_bundle_contains_secret", Msg: "bundle contains a secret field: " + err.Error()}
	}
	if uuidString(env.TargetID) != "" && req.Bundle.Source.WorkspaceID == uuidString(env.TargetID) {
		return nil, &ImportError{Status: 400, Code: "config_import_same_workspace", Msg: "cannot import a bundle into its source workspace"}
	}
	for _, t := range req.Include {
		if !ValidEntityType(t) {
			return nil, &ImportError{Status: 400, Code: "invalid_include", Msg: "unknown entity type: " + t}
		}
	}

	present := map[string]bool{}
	if req.Bundle.Entities.Workspace != nil {
		present["workspace"] = true
	}
	if len(req.Bundle.Entities.Labels) > 0 {
		present["labels"] = true
	}
	if len(req.Bundle.Entities.IssueStatuses) > 0 {
		present["issue_statuses"] = true
	}
	if len(req.Bundle.Entities.IssueProperties) > 0 {
		present["issue_properties"] = true
	}
	if len(req.Bundle.Entities.Skills) > 0 {
		present["skills"] = true
	}
	if len(req.Bundle.Entities.McpServers) > 0 {
		present["mcp_servers"] = true
	}
	if len(req.Bundle.Entities.Agents) > 0 {
		present["agents"] = true
	}
	if len(req.Bundle.Entities.SystemAgents) > 0 {
		present["system_agents"] = true
	}
	if len(req.Bundle.Entities.Squads) > 0 {
		present["squads"] = true
	}
	if len(req.Bundle.Entities.Projects) > 0 {
		present["projects"] = true
	}
	if len(req.Bundle.Entities.Autopilots) > 0 {
		present["autopilots"] = true
	}
	if len(req.Bundle.Entities.QuickActions) > 0 {
		present["quick_actions"] = true
	}
	if len(req.Bundle.Entities.IssueViews) > 0 {
		present["issue_views"] = true
	}

	st := &importState{
		env:       env,
		req:       req,
		include:   includedSet(req.Include, present),
		idMap:     map[string]string{},
		members:   map[string]bool{},
		publicURL: strings.TrimRight(env.PublicURL, "/"),
		report: &ConfigImportReport{
			Applied:       !*req.DryRun,
			BundleID:      req.Bundle.BundleID,
			OnConflict:    onConflict,
			Batches:       []ConfigImportBatch{},
			UnmappedRefs:  []UnmappedRef{},
			SecretsToFill: []SecretToFill{},
			Warnings:      []ConfigWarning{},
		},
	}
	st.req.OnConflict = onConflict

	order := []string{
		"workspace", "labels", "issue_statuses", "issue_properties", "skills",
		"mcp_servers", "agents", "system_agents", "squads", "projects",
		"autopilots", "quick_actions", "issue_views",
	}

	dry := *req.DryRun
	if !dry {
		// Hold one lock transaction open for the whole apply so a concurrent
		// import cannot interleave between batch commits.
		lockTx, err := env.TxStarter.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("begin import lock tx: %w", err)
		}
		defer func() { _ = lockTx.Rollback(context.WithoutCancel(ctx)) }()
		locked, err := env.Queries.WithTx(lockTx).TryConfigImportLock(ctx, uuidString(env.TargetID))
		if err != nil {
			return nil, fmt.Errorf("acquire import lock: %w", err)
		}
		if !locked {
			return nil, &ImportError{Status: 409, Code: "config_import_in_progress", Msg: "another import is in progress"}
		}
	}
	var conflictItems []ConfigImportBatch
	for _, et := range order {
		if !st.include[et] {
			continue
		}
		if st.stopped {
			st.report.Batches = append(st.report.Batches, ConfigImportBatch{
				EntityType: et, BatchStatus: BatchNotAttempted, Items: []ConfigImportItem{},
			})
			continue
		}
		status := BatchCommitted
		if dry {
			status = BatchPreview
		}
		batch := ConfigImportBatch{EntityType: et, BatchStatus: status, Items: []ConfigImportItem{}}
		var err error
		if dry {
			batch.Items, err = st.previewBatch(ctx, et)
		} else {
			batch.Items, err = st.applyBatch(ctx, et)
		}
		if err != nil {
			var ie *ImportError
			if errors.As(err, &ie) && ie.Code == "config_import_conflict" {
				batch.BatchStatus = BatchRolledBack
				if dry {
					batch.BatchStatus = BatchPreview
					conflictItems = append(conflictItems, batch)
					st.report.Batches = append(st.report.Batches, batch)
					continue
				}
				st.stopped = true
				st.report.Batches = append(st.report.Batches, batch)
				st.tally()
				ie.Report = st.report
				return st.report, ie
			}
			if !dry {
				batch.BatchStatus = BatchRolledBack
				st.stopped = true
			}
			st.report.Batches = append(st.report.Batches, batch)
			st.tally()
			if ie, ok := err.(*ImportError); ok {
				ie.Report = st.report
				return st.report, ie
			}
			return st.report, &ImportError{
				Status: 422, Code: "config_import_partial_failure", Msg: err.Error(), Report: st.report,
			}
		}
		st.report.Batches = append(st.report.Batches, batch)
	}

	if dry && onConflict == ConflictFail && len(conflictItems) > 0 {
		st.tally()
		return st.report, &ImportError{
			Status: 409, Code: "config_import_conflict", Msg: "import conflicts", Report: st.report,
		}
	}

	st.buildSecretsToFill()
	st.tally()
	if st.stopped {
		return st.report, &ImportError{
			Status: 422, Code: "config_import_partial_failure", Msg: "import stopped after a batch failure", Report: st.report,
		}
	}
	return st.report, nil
}

func (st *importState) previewBatch(ctx context.Context, et string) ([]ConfigImportItem, error) {
	return st.runBatch(ctx, st.env.Queries, et, true)
}

func (st *importState) applyBatch(ctx context.Context, et string) ([]ConfigImportItem, error) {
	tx, err := st.env.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := st.env.Queries.WithTx(tx)
	items, err := st.runBatch(ctx, q, et, false)
	if err != nil {
		return items, err
	}
	if err := tx.Commit(ctx); err != nil {
		return items, fmt.Errorf("commit: %w", err)
	}
	return items, nil
}

func (st *importState) runBatch(ctx context.Context, q *db.Queries, et string, dry bool) ([]ConfigImportItem, error) {
	switch et {
	case "workspace":
		return st.importWorkspace(ctx, q, dry)
	case "labels":
		return st.importLabels(ctx, q, dry)
	case "issue_statuses":
		return st.importIssueStatuses(ctx, q, dry)
	case "issue_properties":
		return st.importIssueProperties(ctx, q, dry)
	case "skills":
		return st.importSkills(ctx, q, dry)
	case "mcp_servers":
		return st.importMcpServers(ctx, q, dry)
	case "agents":
		return st.importAgents(ctx, q, dry)
	case "system_agents":
		return st.importSystemAgents(ctx, q, dry)
	case "squads":
		return st.importSquads(ctx, q, dry)
	case "projects":
		return st.importProjects(ctx, q, dry)
	case "autopilots":
		return st.importAutopilots(ctx, q, dry)
	case "quick_actions":
		return st.importQuickActions(ctx, q, dry)
	case "issue_views":
		return st.importIssueViews(ctx, q, dry)
	default:
		return nil, nil
	}
}

func (st *importState) put(kind, sourceID, targetID string) {
	if sourceID == "" || targetID == "" {
		return
	}
	st.idMap[kind+":"+sourceID] = targetID
}

func (st *importState) get(kind, sourceID string) (string, bool) {
	if sourceID == "" {
		return "", false
	}
	v, ok := st.idMap[kind+":"+sourceID]
	return v, ok
}

func (st *importState) isMember(ctx context.Context, q *db.Queries, userID string) bool {
	if userID == "" {
		return false
	}
	if v, ok := st.members[userID]; ok {
		return v
	}
	uid, err := parseUUID(userID)
	if err != nil {
		st.members[userID] = false
		return false
	}
	_, err = q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: uid, WorkspaceID: st.env.TargetID,
	})
	ok := err == nil
	st.members[userID] = ok
	return ok
}

func (st *importState) dropMember(entity, sourceID, field, refID string) {
	st.report.UnmappedRefs = append(st.report.UnmappedRefs, UnmappedRef{
		Entity: entity, SourceID: sourceID, Field: field,
		RefType: "member", RefID: refID, Resolution: "dropped",
	})
}

func (st *importState) dropRef(entity, sourceID, field, refType, refID, resolution string) {
	st.report.UnmappedRefs = append(st.report.UnmappedRefs, UnmappedRef{
		Entity: entity, SourceID: sourceID, Field: field,
		RefType: refType, RefID: refID, Resolution: resolution,
	})
}

func (st *importState) newID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

func (st *importState) applyWorkspaceSettings() bool {
	if st.req.Options.ApplyWorkspaceSettings == nil {
		return true
	}
	return *st.req.Options.ApplyWorkspaceSettings
}

func (st *importState) tally() {
	s := ConfigImportStats{}
	for _, b := range st.report.Batches {
		for _, it := range b.Items {
			switch it.Action {
			case ActionCreated:
				s.Created++
			case ActionUpdated:
				s.Updated++
			case ActionRenamed:
				s.Renamed++
			case ActionSkipped:
				s.Skipped++
			case ActionFailed:
				s.Failed++
			}
		}
	}
	st.report.Stats = s
}

func (st *importState) buildSecretsToFill() {
	slug := st.env.TargetSlug
	for _, s := range st.req.Bundle.SecretsOmitted {
		item := SecretToFill{Entity: s.Entity, Name: s.Name, Field: s.Field}
		switch s.Entity {
		case "agent":
			if tid, ok := st.get("agent", s.SourceID); ok {
				item.TargetID = tid
				item.Path = fmt.Sprintf("/%s/agents/%s/settings", slug, tid)
			}
		case "workspace_mcp_server":
			item.Name = s.Name
			item.Path = fmt.Sprintf("/%s/settings/mcp", slug)
		case "autopilot_trigger":
			if tid, ok := st.get("autopilot", s.SourceID); ok {
				item.TargetID = tid
				item.Path = fmt.Sprintf("/%s/autopilots/%s", slug, tid)
			}
		}
		st.report.SecretsToFill = append(st.report.SecretsToFill, item)
	}
	for _, integ := range st.req.Bundle.Integrations {
		if integ.Kind == "workspace_mcp_server" {
			already := false
			for _, s := range st.report.SecretsToFill {
				if s.Entity == "workspace_mcp_server" && s.Name == integ.Name {
					already = true
					break
				}
			}
			if !already {
				st.report.SecretsToFill = append(st.report.SecretsToFill, SecretToFill{
					Entity: "workspace_mcp_server", Name: integ.Name, Field: "config",
					Path: fmt.Sprintf("/%s/settings/mcp", slug),
				})
			}
		}
	}
}

func (st *importState) resolveConflict(name string, exists bool) (action string, newName string, err error) {
	if !exists {
		return ActionCreated, name, nil
	}
	switch st.req.OnConflict {
	case ConflictSkip:
		return ActionSkipped, name, nil
	case ConflictOverwrite:
		return ActionUpdated, name, nil
	case ConflictRename:
		return ActionRenamed, name, nil
	default:
		return ActionFailed, name, &ImportError{Status: 409, Code: "config_import_conflict", Msg: "entity already exists: " + name}
	}
}

func nextRenamed(base string, n int) string {
	return fmt.Sprintf("%s-%d", base, n)
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
