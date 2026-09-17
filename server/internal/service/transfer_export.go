package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type TransferSourceClient interface {
	GetJSON(ctx context.Context, path string, out any) error
	// GetOptionalJSON reads an endpoint that powers an optional subsystem — the
	// source may simply not have it (plugins behind FF_PLUGINS_V1 is the one
	// today). A 503 there is permanent, not transient, so the client must
	// answer at once instead of retrying: the caller records an export gap and
	// exports the rest of the group (DENE-406).
	GetOptionalJSON(ctx context.Context, path string, out any) error
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

// TransferListTruncation reports a list read that may have dropped rows. It is
// not a read failure — the decoded rows are still exported — but every caller
// records it as an export gap, because one request answered with a cap or a
// cursor is exactly the silent truncation this exporter must not ship.
type TransferListTruncation struct {
	Reason string
	Limit  int
}

func (t *TransferListTruncation) Error() string {
	if t.Limit > 0 {
		return fmt.Sprintf("%s (limit %d)", t.Reason, t.Limit)
	}
	return t.Reason
}

// transferReadGap builds the manifest gap for a list/config read. A
// *TransferListTruncation is reported with its own reason and limit; an HTTP
// error keeps the read_api_error / read_api_missing vocabulary.
func transferReadGap(group string, err error) TransferExportGap {
	var trunc *TransferListTruncation
	if errors.As(err, &trunc) {
		return TransferExportGap{Group: group, Reason: trunc.Reason, Limit: trunc.Limit}
	}
	g := TransferExportGap{Group: group, Reason: gapReasonReadAPIError}
	switch st := transferStatus(err); {
	case st == 404:
		g.Reason = gapReasonReadAPIMissing
		g.Status = 404
	case st > 0:
		g.Status = st
	}
	return g
}

// noteListTruncation records a capped or paginated list read as an export gap.
func noteListTruncation(gap func(string, error), group string, trunc *TransferListTruncation) {
	if trunc != nil {
		gap(group, trunc)
	}
}

// appendTransferGap is noteListTruncation for callers that own a gap slice
// instead of the exportConfigGroups recorder.
func appendTransferGap(gaps *[]TransferExportGap, group string, trunc *TransferListTruncation) {
	if trunc != nil {
		*gaps = append(*gaps, transferReadGap(group, trunc))
	}
}

// dedupeTransferAttachments collapses rows that describe the same attachment.
//
// A comment-held attachment keeps its issue_id, so the issue's attachment list
// serves it a second time next to the comment read that already carried it. One
// file must not become two `attachments/index.jsonl` rows: the duplicate costs a
// second body download and makes `stats.attachments` — the number the migration
// card shows — one higher than the files actually in the bundle (DENE-406).
//
// The row that names its comment wins, because that is the column the import
// reconnects the attachment to its comment through, and the issue-list copy
// carries it as NULL.
func dedupeTransferAttachments(rows []TransferAttachmentRow) []TransferAttachmentRow {
	if len(rows) < 2 {
		return rows
	}
	seen := make(map[string]int, len(rows))
	out := make([]TransferAttachmentRow, 0, len(rows))
	for _, row := range rows {
		if row.SourceID == "" {
			// Without an id there is nothing to dedupe on; dropping the row
			// would lose it.
			out = append(out, row)
			continue
		}
		if first, ok := seen[row.SourceID]; ok {
			if out[first].CommentID == nil && row.CommentID != nil {
				out[first] = row
			}
			continue
		}
		seen[row.SourceID] = len(out)
		out = append(out, row)
	}
	return out
}

type TransferExportOpts struct {
	Include         []string
	ExcludeArchived bool
	People          bool
	Estimate        bool
	ClientVersion   string
	BaseURLHost     string
	WorkspaceRef    string
	// PartialDir is the <out>.partial checkpoint directory. Existing
	// completed session shards are reused; new ones are written as they
	// finish. Empty disables checkpointing (estimate mode).
	PartialDir string
	OutPath    string
	// Progress, when set, receives incremental export progress. It is called
	// synchronously on the exporting goroutine, so a slow callback slows the
	// export down; callers that write to a pipe should keep it cheap.
	Progress func(TransferExportProgress)
}

// TransferExportProgress is one incremental export sample. It exists so a
// multi-minute export is visible while it runs instead of looking hung: the
// CLI turns each sample into a `{"event":"progress",...}` line on stderr
// (DENE-318).
type TransferExportProgress struct {
	// Stage names the group being walked: TransferStageConfig,
	// TransferStageConversations or TransferStageIssues. An export that ships
	// several groups spends minutes in each, so a reader that only counted
	// sessions saw a frozen "26 / 26" for the whole task walk (DENE-240).
	Stage string
	// SessionIndex is the 1-based position of the session being exported, and
	// SessionsTotal how many the export will walk. Together they read as
	// "3 / 26 sessions" while SessionTitle names the session in flight.
	SessionIndex  int
	SessionsTotal int
	SessionTitle  string
	// AttachmentsDownloaded counts attachment bodies fetched so far. The total
	// is deliberately absent: attachments are discovered per message, so no
	// honest denominator exists before the walk finishes.
	AttachmentsDownloaded int
	// IssuesDone / IssuesTotal are the task walk's counterpart to the session
	// pair. The total is known up front: the issue list is fetched whole before
	// any issue is read.
	IssuesDone  int
	IssuesTotal int
}

// The stages an export walks, in the order ExportFromSource runs them.
const (
	TransferStageConfig        = "config"
	TransferStageConversations = "conversations"
	TransferStageIssues        = "issues"
)

type TransferExportFiles struct {
	Manifest      TransferManifest
	Config        ConfigBundle
	People        []TransferPerson
	Runtimes      TransferRuntimesFile
	Preferences   TransferPreferences
	SessionShards [][]TransferSessionRow
	MessageShards [][]TransferMessageRow
	// V3 issue group. IssueShards[i] and CommentShards[i] are one shard pair:
	// every comment in CommentShards[i] belongs to an issue in IssueShards[i].
	IssueShards   [][]TransferIssueRow
	CommentShards [][]TransferCommentRow
	// Relations is one bundle-wide file (labels and reactions); the import
	// groups it by the shard each referenced issue/comment travelled in.
	Relations   []TransferRelationRow
	Attachments []TransferAttachmentRow
	Blobs       map[string][]byte
	Secrets     []SecretOmitted
	Estimate    *TransferEstimate
}

type TransferEstimate struct {
	Sessions         int   `json:"sessions"`
	Messages         int   `json:"messages"`
	Issues           int   `json:"issues"`
	IssueComments    int   `json:"issue_comments"`
	Attachments      int   `json:"attachments"`
	AttachmentBodies int   `json:"attachment_bodies"`
	EstimatedBytes   int64 `json:"estimated_bytes"`
}

// mergeTransferEstimate adds the issue group's estimate to the conversation
// group's, so `--estimate --include config,conversations,attachments,issues`
// reports one number for the whole bundle and no field silently disappears
// just because both groups ran.
func mergeTransferEstimate(dst, src *TransferEstimate) *TransferEstimate {
	if src == nil {
		return dst
	}
	if dst == nil {
		return src
	}
	dst.Issues += src.Issues
	dst.IssueComments += src.IssueComments
	dst.Attachments += src.Attachments
	dst.AttachmentBodies += src.AttachmentBodies
	dst.EstimatedBytes += src.EstimatedBytes
	return dst
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

	if inc["config"] && opts.Progress != nil {
		opts.Progress(TransferExportProgress{Stage: TransferStageConfig})
	}
	if inc["config"] {
		cfgGaps := exportConfigGroups(ctx, src, ws.ID, &bundle, peopleByID)
		gaps = append(gaps, cfgGaps...)
		sanitizeBundleSecrets(&bundle)
	}

	runtimes := TransferRuntimesFile{}
	prefs := TransferPreferences{}
	if inc["config"] {
		runtimes, _ = exportRuntimeProfiles(ctx, src, ws.ID, &bundle, &gaps)
		prefs = exportPinnedAgents(ctx, src, &gaps)
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
		Agents:          map[string]TransferAgentRef{},
		SystemAgents:    map[string]TransferAgentRef{},
		Projects:        map[string]TransferProjRef{},
		Members:         map[string]TransferMemberRef{},
		Squads:          map[string]TransferSquadRef{},
		IssueStatuses:   map[string]string{},
		IssueProperties: map[string]TransferPropertyRef{},
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
	// The V3 maps are the import's whole basis for mention rewriting, status
	// downgrade and property-key remapping, so they are filled from the config
	// group that actually ships in this bundle. `--no-people` keeps member
	// emails out of the bundle, and this map follows it rather than becoming a
	// second, unflagged copy of the same addresses.
	if opts.People {
		for _, p := range people {
			refs.Members[p.SourceUserID] = TransferMemberRef{Email: p.Email}
		}
	}
	for _, s := range bundle.Entities.Squads {
		refs.Squads[s.SourceID] = TransferSquadRef{Name: s.Name}
	}
	for _, st := range bundle.Entities.IssueStatuses {
		refs.IssueStatuses[st.Key] = st.Category
	}
	for _, p := range bundle.Entities.IssueProperties {
		refs.IssueProperties[p.SourceID] = TransferPropertyRef{Name: p.Name}
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

	if inc[TransferIncludeIssues] {
		issues := exportIssues(ctx, src, opts, &refs)
		gaps = append(gaps, issues.Gaps...)
		out.IssueShards, out.CommentShards = shardTransferIssues(issues.IssueRows, issues.CommentsByIssue)
		out.Relations = issues.Relations
		out.Attachments = append(out.Attachments, issues.Attachments...)
		for sha, body := range issues.Blobs {
			out.Blobs[sha] = body
		}
		bundle.SecretsOmitted = append(bundle.SecretsOmitted, issues.Secrets...)
		if opts.Estimate {
			out.Estimate = mergeTransferEstimate(out.Estimate, issues.Estimate)
		}
	}
	out.Secrets = bundle.SecretsOmitted

	// Both groups are gathered, so an attachment read twice can be collapsed
	// before it reaches `attachments/index.jsonl` or the stats below (DENE-406).
	out.Attachments = dedupeTransferAttachments(out.Attachments)

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
	issueCount, commentCount := 0, 0
	for _, shard := range out.IssueShards {
		issueCount += len(shard)
	}
	for _, shard := range out.CommentShards {
		commentCount += len(shard)
	}
	stats["issues"] = issueCount
	stats["issue_comments"] = commentCount
	stats["issue_relations"] = len(out.Relations)
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
		SchemaVersion: TransferBundleSchemaVersionForContent(includeList),
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
	// A transfer that silently succeeds with failed core reads is an empty
	// shell.  Preserve 404 as the documented compatibility downgrade, but fail
	// the export for every other read failure before the caller can write a
	// zip.  Documented degradations that still export their whole group (an
	// auxiliary endpoint being unavailable, a list read the server capped or
	// paginated, an envelope this exporter cannot read) are warnings, not
	// failures — the contract keeps the group.
	if len(gaps) > 0 {
		failed := make([]string, 0, len(gaps))
		for _, g := range gaps {
			if warningGapReason(g.Reason) {
				continue
			}
			if g.Status == 404 && g.Reason == gapReasonReadAPIMissing {
				continue
			}
			if g.Status != 404 {
				if g.Status > 0 {
					failed = append(failed, fmt.Sprintf("%s (%s, status %d)", g.Group, g.Reason, g.Status))
				} else {
					failed = append(failed, fmt.Sprintf("%s (%s)", g.Group, g.Reason))
				}
			}
		}
		if len(failed) > 0 {
			return nil, fmt.Errorf("export failed for groups: %s", strings.Join(failed, ", "))
		}
	}
	return out, nil
}

// Gap reasons that are documented degradations rather than read failures:
// the group's own read succeeded, so its data is in the bundle and only the
// reported caveat remains.  `read_api_error` / `read_api_missing` are read
// failures and stay fatal (except 404, the compatibility downgrade).
//
// The list_* reasons come from getList: every one of them means the rows in
// hand are exported but are not provably the whole list, so they warn instead
// of aborting a migration the user can still complete.
const (
	gapReasonReadAPIError     = "read_api_error"
	gapReasonReadAPIMissing   = "read_api_missing"
	gapReasonPluginUnfiltered = "plugin_skills_unfiltered"
	gapReasonIssueViewsCapped = "issue_views_scope_capped"
	// gapReasonListCapReached: the response filled the endpoint's server-side
	// per-request cap, so the tail never came back.
	gapReasonListCapReached = "list_cap_reached"
	// gapReasonListHasMore: the envelope advertised another page (has_more /
	// next_cursor) and only the first one was read.
	gapReasonListHasMore = "list_has_more"
	// gapReasonListShapeUnknown: the response was an object with no list key
	// this exporter knows how to read, so it yielded zero rows — the whole
	// group went missing. Saying so beats exporting nothing silently.
	gapReasonListShapeUnknown = "list_shape_unknown"
	// gapReasonCommentWindowTruncated: one issue has more comments at a single
	// microsecond than a `since` page can carry, so the cursor cannot advance
	// past them. The rows read so far still ship, but the tail is known to be
	// missing (§11.5 keeps this token for exactly this case).
	gapReasonCommentWindowTruncated = "comment_window_truncated"
	// gapReasonCommentWindowSampled: `--estimate` read one comments page per
	// issue on purpose. It is a sample, not a truncation, and never blocks a
	// bundle because estimate mode writes none.
	gapReasonCommentWindowSampled = "comment_window_sampled"
	// gapReasonAutopilotFieldsUnreadable: the autopilot detail body carried
	// none of the fields this exporter maps, so the row was dropped instead of
	// exported empty for the import to skip as assignee_unmapped. The rest of
	// the group still ships, so this is a warning, not a read failure.
	gapReasonAutopilotFieldsUnreadable = "autopilot_fields_unreadable"
)

func warningGapReason(reason string) bool {
	switch reason {
	case gapReasonPluginUnfiltered, gapReasonIssueViewsCapped,
		gapReasonListCapReached, gapReasonListHasMore, gapReasonListShapeUnknown,
		gapReasonCommentWindowTruncated, gapReasonCommentWindowSampled,
		gapReasonAutopilotFieldsUnreadable:
		return true
	}
	return false
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
		gaps = append(gaps, transferReadGap(group, err))
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
	if trunc, err := getList(ctx, src, "/api/labels", &labels); err != nil {
		gap("labels", err)
	} else {
		noteListTruncation(gap, "labels", trunc)
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
	if trunc, err := getList(ctx, src, "/api/issue-statuses", &statuses); err != nil {
		gap("issue_statuses", err)
	} else {
		noteListTruncation(gap, "issue_statuses", trunc)
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
	if trunc, err := getList(ctx, src, "/api/properties", &props); err != nil {
		gap("issue_properties", err)
	} else {
		noteListTruncation(gap, "issue_properties", trunc)
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
		g := TransferExportGap{Group: "skills", Reason: gapReasonPluginUnfiltered}
		if st := transferStatus(pluginErr); st > 0 {
			g.Status = st
		}
		*gaps = append(*gaps, g)
		pluginSkills = nil
	}
	var skills []map[string]any
	if trunc, err := getList(ctx, src, "/api/skills", &skills); err != nil {
		gap("skills", err)
		return
	} else {
		noteListTruncation(gap, "skills", trunc)
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
		if trunc, err := getList(ctx, src, "/api/skills/"+url.PathEscape(id)+"/files", &files); err == nil {
			noteListTruncation(gap, "skills", trunc)
			for _, f := range files {
				sk.Files = append(sk.Files, ConfigSkillFile{Path: strField(f, "path"), Content: strField(f, "content")})
			}
		}
		var labs []map[string]any
		if trunc, err := getList(ctx, src, "/api/skills/"+url.PathEscape(id)+"/labels", &labs); err == nil {
			noteListTruncation(gap, "skills", trunc)
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
// record plugin_skills_unfiltered. The read is optional, so it is not retried:
// a disabled plugin subsystem answers 503 forever, and waiting on it only
// parks the export for minutes before recording the same gap (DENE-406).
func pluginContributedSkillNames(ctx context.Context, src TransferSourceClient, wsID string) (map[string]bool, error) {
	var plugins []map[string]any
	trunc, err := getOptionalList(ctx, src, "/api/workspaces/"+url.PathEscape(wsID)+"/plugins", &plugins)
	if err != nil {
		return nil, err
	}
	if trunc != nil {
		// A capped page cannot say which skills came from plugins, so the
		// caller exports all of them and records plugin_skills_unfiltered.
		return nil, trunc
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
	if trunc, err := getList(ctx, src, "/api/agents", &agents); err != nil {
		gap("agents", err)
		return
	} else {
		noteListTruncation(gap, "agents", trunc)
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
		if trunc, err := getList(ctx, src, "/api/agents/"+url.PathEscape(id)+"/labels", &labs); err == nil {
			noteListTruncation(gap, "agents", trunc)
			for _, l := range labs {
				a.LabelIDs = append(a.LabelIDs, strField(l, "id"))
			}
		}
		var mcps []map[string]any
		if trunc, err := getList(ctx, src, "/api/agents/"+url.PathEscape(id)+"/mcp-servers", &mcps); err == nil {
			noteListTruncation(gap, "agents", trunc)
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
	if trunc, err := getList(ctx, src, "/api/squads", &rows); err != nil {
		gap("squads", err)
		return
	} else {
		noteListTruncation(gap, "squads", trunc)
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
		if trunc, err := getList(ctx, src, "/api/squads/"+url.PathEscape(id)+"/members", &members); err == nil {
			noteListTruncation(gap, "squads", trunc)
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
	if trunc, err := getList(ctx, src, "/api/projects", &rows); err != nil {
		gap("projects", err)
		return
	} else {
		noteListTruncation(gap, "projects", trunc)
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
		if trunc, err := getList(ctx, src, "/api/projects/"+url.PathEscape(id)+"/resources", &resources); err == nil {
			noteListTruncation(gap, "projects", trunc)
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

func sourceExportAutopilots(ctx context.Context, src TransferSourceClient, bundle *ConfigBundle, gaps *[]TransferExportGap, gap func(string, error)) {
	var rows []map[string]any
	if trunc, err := getList(ctx, src, "/api/autopilots", &rows); err != nil {
		gap("autopilots", err)
		return
	} else {
		noteListTruncation(gap, "autopilots", trunc)
	}
	for _, raw := range rows {
		id := strField(raw, "id")
		// GET /api/autopilots/{id} wraps the autopilot body:
		// {"autopilot": {...}, "triggers": [...], "collaborators": [...]}.
		// Reading that envelope as the autopilot itself exported a row with
		// every field empty, which the import then skipped as
		// assignee_unmapped (DENE-403). Unwrap the body; triggers and
		// collaborators stay top-level siblings. The list row is the fallback
		// when the detail read fails or the body key is absent — it carries the
		// same autopilot fields.
		body := raw
		var detail map[string]any
		if err := src.GetJSON(ctx, "/api/autopilots/"+url.PathEscape(id), &detail); err == nil && len(detail) > 0 {
			if nested, ok := detail["autopilot"].(map[string]any); ok && len(nested) > 0 {
				body = nested
			}
		}
		ap := ConfigAutopilot{
			SourceID:           id,
			Title:              strField(body, "title"),
			Description:        strField(body, "description"),
			ExecutionMode:      strField(body, "execution_mode"),
			IssueTitleTemplate: strPtrField(body, "issue_title_template"),
			Status:             strField(body, "status"),
		}
		if at := strField(body, "assignee_type"); at != "" {
			ap.Assignee = &ConfigPolymorphicRef{Type: at, ID: strField(body, "assignee_id")}
		}
		if pid := strField(body, "project_id"); pid != "" {
			ap.ProjectID = &pid
		}
		if trigs, ok := detail["triggers"].([]any); ok {
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
		if subs, ok := body["subscribers"].([]any); ok {
			for _, s := range subs {
				m, _ := s.(map[string]any)
				ap.Subscribers = append(ap.Subscribers, ConfigAutopilotPerson{UserType: strField(m, "user_type"), UserID: strField(m, "user_id")})
			}
		}
		if cols, ok := detail["collaborators"].([]any); ok {
			for _, s := range cols {
				m, _ := s.(map[string]any)
				ap.Collaborators = append(ap.Collaborators, ConfigAutopilotPerson{UserType: strField(m, "user_type"), UserID: strField(m, "user_id")})
			}
		}
		if autopilotBodyUnreadable(ap) {
			// A body this exporter cannot read reaches the target as
			// assignee_unmapped and is skipped there. Counting it here would
			// report an automation that never arrives, so drop the empty row
			// and name the degradation instead.
			*gaps = append(*gaps, TransferExportGap{Group: "autopilots", Reason: gapReasonAutopilotFieldsUnreadable})
			continue
		}
		bundle.Entities.Autopilots = append(bundle.Entities.Autopilots, ap)
	}
	bundle.Stats["autopilots"] = len(bundle.Entities.Autopilots)
}

// autopilotBodyUnreadable reports a response body that yielded none of the
// fields this exporter maps. Every autopilot row has a title (NOT NULL in the
// schema) and an assignee, so an all-empty body means the source answered in a
// shape this exporter does not read — not that the autopilot is empty.
func autopilotBodyUnreadable(ap ConfigAutopilot) bool {
	return ap.Title == "" && ap.Description == "" && ap.ExecutionMode == "" &&
		ap.Status == "" && ap.IssueTitleTemplate == nil && ap.Assignee == nil &&
		ap.ProjectID == nil && len(ap.Triggers) == 0 && len(ap.Subscribers) == 0 &&
		len(ap.Collaborators) == 0
}

func sourceExportQuickActions(ctx context.Context, src TransferSourceClient, bundle *ConfigBundle, _ *[]TransferExportGap, gap func(string, error)) {
	var rows []map[string]any
	if trunc, err := getList(ctx, src, "/api/quick-actions", &rows); err != nil {
		gap("quick_actions", err)
		return
	} else {
		noteListTruncation(gap, "quick_actions", trunc)
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
	// The endpoint requires scope_type. Fetch workspace views once, then each
	// project scope explicitly; a bare /api/issue-views request is a 400.
	paths := []string{"/api/issue-views?scope_type=workspace"}
	for _, project := range bundle.Entities.Projects {
		if project.SourceID != "" {
			paths = append(paths, "/api/issue-views?scope_type=project&scope_id="+url.QueryEscape(project.SourceID))
		}
	}
	for _, path := range paths {
		var rows []map[string]any
		trunc, err := getList(ctx, src, path, &rows)
		if err != nil {
			gap("issue_views", err)
			continue
		}
		noteListTruncation(gap, "issue_views", trunc)
		for _, raw := range rows {
			// Parity with the V1 export (ExportIssueViews): workspace- and
			// project-shared views travel; private and my-scope views stay
			// behind. Skipping visibility='project' silently dropped every
			// project board from the bundle.
			visibility := strField(raw, "visibility")
			if visibility != "workspace" && visibility != "project" {
				continue
			}
			bundle.Entities.IssueViews = append(bundle.Entities.IssueViews, ConfigIssueView{
				SourceID:          strField(raw, "id"),
				Name:              strField(raw, "name"),
				ScopeType:         strField(raw, "scope_type"),
				ScopeID:           strPtrField(raw, "scope_id"),
				ScopeVariant:      strPtrField(raw, "scope_variant"),
				Visibility:        visibility,
				DefinitionVersion: int32(floatField(raw, "definition_version")),
				Query:             rawField(raw, "query"),
				Display:           rawField(raw, "display"),
			})
		}
	}
	bundle.Stats["issue_views"] = len(bundle.Entities.IssueViews)
}

func sourceExportIntegrations(ctx context.Context, src TransferSourceClient, wsID string, bundle *ConfigBundle, _ *[]TransferExportGap, gap func(string, error)) {
	base := "/api/workspaces/" + url.PathEscape(wsID)
	var mcp []map[string]any
	if trunc, err := getList(ctx, src, base+"/mcp-servers", &mcp); err != nil {
		gap("mcp_servers", err)
	} else {
		noteListTruncation(gap, "mcp_servers", trunc)
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
	if trunc, err := getList(ctx, src, base+"/github/installations", &gh); err == nil {
		noteListTruncation(gap, "github_installations", trunc)
		for _, raw := range gh {
			bundle.Integrations = append(bundle.Integrations, ConfigIntegration{
				Kind: "github", AccountLogin: strField(raw, "account_login"), AccountType: strField(raw, "account_type"),
			})
		}
	}
	var vcs []map[string]any
	if trunc, err := getList(ctx, src, base+"/vcs/connections", &vcs); err == nil {
		noteListTruncation(gap, "vcs_connections", trunc)
		for _, raw := range vcs {
			bundle.Integrations = append(bundle.Integrations, ConfigIntegration{
				Kind: "vcs", Provider: strField(raw, "provider"), InstanceURL: strField(raw, "instance_url"), AccountLogin: strField(raw, "account_login"),
			})
		}
	}
	var plugins []map[string]any
	// Optional subsystem, read a second time for PluginsToReinstall: with
	// FF_PLUGINS_V1 off this 503s, and retrying would stall the export for
	// minutes on top of the skills read that already recorded the gap.
	if trunc, err := getOptionalList(ctx, src, base+"/plugins", &plugins); err == nil {
		noteListTruncation(gap, "plugins", trunc)
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

func exportRuntimeProfiles(ctx context.Context, src TransferSourceClient, wsID string, bundle *ConfigBundle, gaps *[]TransferExportGap) (TransferRuntimesFile, error) {
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
	runtimeByID := map[string]TransferRuntimeHint{}
	profileNameBySourceID := map[string]string{}
	for _, p := range out.Profiles {
		profileNameBySourceID[p.SourceID] = p.DisplayName
	}
	if trunc, err := getList(ctx, src, "/api/runtimes", &runtimes); err == nil {
		appendTransferGap(gaps, "runtimes", trunc)
		for _, raw := range runtimes {
			name := strField(raw, "custom_name")
			if name == "" {
				name = strField(raw, "name")
			}
			hint := TransferRuntimeHint{
				SourceRuntimeID: strField(raw, "id"),
				Provider:        strField(raw, "provider"),
				RuntimeMode:     strField(raw, "runtime_mode"),
				ProfileSourceID: strField(raw, "profile_id"),
				DisplayName:     name,
			}
			out.RuntimesHint = append(out.RuntimesHint, hint)
			if hint.SourceRuntimeID != "" {
				runtimeByID[hint.SourceRuntimeID] = hint
			}
		}
	}
	// Per-agent half of the same hint (DENE-364). `/api/agents` is the only
	// place the source instance exposes which runtime each agent runs on, so
	// the transfer reads it here to turn that id into the provider / mode /
	// profile triple the target matches on.
	var agents []map[string]any
	if trunc, err := getList(ctx, src, "/api/agents", &agents); err == nil {
		appendTransferGap(gaps, "agent_runtimes", trunc)
		for _, raw := range agents {
			if strField(raw, "system_key") != "" {
				continue
			}
			agentID := strField(raw, "id")
			if agentID == "" {
				continue
			}
			hint := TransferAgentRuntimeHint{
				SourceAgentID:   agentID,
				SourceRuntimeID: strField(raw, "runtime_id"),
				RuntimeMode:     strField(raw, "runtime_mode"),
			}
			if rt, ok := runtimeByID[hint.SourceRuntimeID]; ok {
				hint.Provider = rt.Provider
				hint.RuntimeMode = rt.RuntimeMode
				hint.ProfileSourceID = rt.ProfileSourceID
				hint.ProfileName = profileNameBySourceID[rt.ProfileSourceID]
				hint.RuntimeName = rt.DisplayName
			}
			out.AgentHints = append(out.AgentHints, hint)
		}
	}
	return out, nil
}

func exportPinnedAgents(ctx context.Context, src TransferSourceClient, gaps *[]TransferExportGap) TransferPreferences {
	var rows []map[string]any
	if trunc, err := getList(ctx, src, "/api/chat/pinned-agents", &rows); err == nil {
		appendTransferGap(gaps, "pinned_agents", trunc)
	}
	out := TransferPreferences{}
	for _, raw := range rows {
		out.PinnedAgents = append(out.PinnedAgents, TransferPinnedAgent{
			AgentID:  strField(raw, "agent_id"),
			Position: floatField(raw, "position"),
		})
	}
	return out
}

// exportProgressTracker accumulates the counters behind
// TransferExportOpts.Progress. An export walks sessions and attachments on one
// goroutine, so the tracker needs no locking, and a nil tracker is a no-op so
// callers can thread it through unconditionally.
type exportProgressTracker struct {
	emit          func(TransferExportProgress)
	sessionsTotal int
	sessionIndex  int
	sessionTitle  string
	downloaded    int
}

func newExportProgressTracker(emit func(TransferExportProgress)) *exportProgressTracker {
	if emit == nil {
		return nil
	}
	return &exportProgressTracker{emit: emit}
}

func (t *exportProgressTracker) sessions(total int) {
	if t == nil {
		return
	}
	t.sessionsTotal = total
	t.emit(t.sample())
}

func (t *exportProgressTracker) session(index int, title string) {
	if t == nil {
		return
	}
	t.sessionIndex = index
	t.sessionTitle = title
	t.emit(t.sample())
}

// finish reports the walk as complete — every session visited, no session left
// in flight — so the last sample a listener sees is not "24 / 26".
func (t *exportProgressTracker) finish() {
	if t == nil {
		return
	}
	t.sessionIndex = t.sessionsTotal
	t.sessionTitle = ""
	t.emit(t.sample())
}

func (t *exportProgressTracker) attachment(downloaded bool) {
	if t == nil {
		return
	}
	if downloaded {
		t.downloaded++
	}
	t.emit(t.sample())
}

func (t *exportProgressTracker) sample() TransferExportProgress {
	return TransferExportProgress{
		Stage:                 TransferStageConversations,
		SessionIndex:          t.sessionIndex,
		SessionsTotal:         t.sessionsTotal,
		SessionTitle:          t.sessionTitle,
		AttachmentsDownloaded: t.downloaded,
	}
}

func exportConversations(ctx context.Context, src TransferSourceClient, opts TransferExportOpts) (
	[][]TransferSessionRow, [][]TransferMessageRow, []TransferAttachmentRow, map[string][]byte, []SecretOmitted, *TransferEstimate, []TransferExportGap,
) {
	gaps := []TransferExportGap{}
	progress := newExportProgressTracker(opts.Progress)
	var sessions []map[string]any
	path := "/api/chat/sessions"
	if !opts.ExcludeArchived {
		path += "?status=all"
	}
	trunc, err := getList(ctx, src, path, &sessions)
	if err != nil {
		gaps = append(gaps, TransferExportGap{Group: "conversations", Reason: gapReasonReadAPIError, Status: transferStatus(err)})
		return nil, nil, nil, nil, nil, nil, gaps
	}
	appendTransferGap(&gaps, "conversations", trunc)
	progress.sessions(len(sessions))

	est := &TransferEstimate{Sessions: len(sessions)}
	sessRows := []TransferSessionRow{}
	msgBySession := map[string][]TransferMessageRow{}
	atts := []TransferAttachmentRow{}
	blobs := map[string][]byte{}
	secrets := []SecretOmitted{}
	sampleBytes := int64(0)
	sampleMsgs := 0

	resume := loadTransferPartial(opts.PartialDir, opts.OutPath, opts.WorkspaceRef)
	resumeByID := map[string]TransferSessionRow{}
	resumeMsgs := map[string][]TransferMessageRow{}
	if resume != nil && !opts.Estimate {
		var resumeAtts []TransferAttachmentRow
		var resumeBlobs map[string][]byte
		var resumeSess []TransferSessionRow
		resumeSess, resumeMsgs, resumeAtts, resumeBlobs = loadPartialConversations(opts.PartialDir, resume.CompletedSessionIDs)
		for _, s := range resumeSess {
			resumeByID[s.SourceID] = s
		}
		atts = append(atts, resumeAtts...)
		for k, v := range resumeBlobs {
			blobs[k] = v
		}
	}

	for i, raw := range sessions {
		id := strField(raw, "id")
		// Report position before any work: a resumed session is still one of
		// the sessions being walked, and the counter must advance either way.
		progress.session(i+1, strField(raw, "title"))
		if saved, ok := resumeByID[id]; ok {
			sessRows = append(sessRows, saved)
			msgBySession[id] = resumeMsgs[id]
			continue
		}
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

		msgs, msgSecrets, msgAtts, msgBlobs := fetchAllMessages(ctx, src, id, opts, progress)
		msgBySession[id] = msgs
		secrets = append(secrets, msgSecrets...)
		atts = append(atts, msgAtts...)
		for k, v := range msgBlobs {
			blobs[k] = v
		}
		if opts.PartialDir != "" {
			if resume == nil {
				resume = newTransferPartialState(opts)
			}
			_ = persistCompletedSession(opts.PartialDir, resume, row, msgs, msgAtts, msgBlobs)
		}
	}
	progress.finish()

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

func fetchAllMessages(ctx context.Context, src TransferSourceClient, sessionID string, opts TransferExportOpts, progress *exportProgressTracker) ([]TransferMessageRow, []SecretOmitted, []TransferAttachmentRow, map[string][]byte) {
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
					progress.attachment(blob != nil)
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
	origFilename := strField(raw, "filename")
	ct := strField(raw, "content_type")
	size := int64(floatField(raw, "size_bytes"))
	filename := origFilename
	var secrets []SecretOmitted
	if scannedName := ScanTransferContent(origFilename); scannedName.Changed {
		filename = scannedName.Text
		secrets = append(secrets, secretOmittedFromHits("attachment", id, "filename", scannedName.Hits))
	}
	row := TransferAttachmentRow{
		SourceID:      id,
		ChatSessionID: &sessionID,
		ChatMessageID: &messageID,
		Filename:      filename,
		ContentType:   ct,
		SizeBytes:     size,
		CreatedAt:     strField(raw, "created_at"),
	}
	exportBody, reason := shouldExportAttachmentBody(ct, origFilename, size)
	if !exportBody {
		row.BodyOmittedReason = &reason
		return row, nil, secrets
	}
	body, err := src.GetBytes(ctx, "/api/attachments/"+url.PathEscape(id)+"/download")
	if err != nil {
		r := "attachment_body_not_exported"
		row.BodyOmittedReason = &r
		return row, nil, secrets
	}
	if isTextAttachment(ct, origFilename) {
		scanned := ScanTransferContent(string(body))
		if scanned.Changed {
			r := "content_pattern"
			row.BodyOmittedReason = &r
			return row, nil, append(secrets, secretOmittedFromHits("attachment", id, "body", scanned.Hits))
		}
	}
	sum := sha256.Sum256(body)
	row.SHA256 = hex.EncodeToString(sum[:])
	row.Body = "attachments/blobs/" + row.SHA256
	return row, body, secrets
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

// transferListCap is the server-side per-request result cap of a list endpoint
// plus the gap reason that cap produces. Keeping the reason per path is what
// lets the issue-view cap keep its own contract token
// (issue_views_scope_capped) while every other capped endpoint gets
// list_cap_reached.
type transferListCap struct {
	Limit  int
	Reason string
}

// transferListCaps is the per-request cap of the list endpoints the export
// reads through getList, keyed by path with the query string stripped. A
// response that fills its cap has no room for the next row, so the tail is
// unaccounted for and the read is reported as an export gap. Endpoints absent
// from this table have no LIMIT on their result set in server/pkg/db/queries,
// so a full response is the whole list — add an entry here when a new capped
// endpoint joins the export.
var transferListCaps = map[string]transferListCap{
	// ListIssueViewsForUser ends in `LIMIT 200` (issue_view.sql): one saved-view
	// scope can hold more rows than the endpoint will ever return in one call.
	"/api/issue-views": {Limit: issueViewsScopeCap, Reason: gapReasonIssueViewsCapped},
}

// issueViewsScopeCap mirrors the hard per-scope LIMIT of the read endpoint
// (server/pkg/db/queries/issue_view.sql: "Hard response cap ... LIMIT 200").
// The endpoint exposes no cursor, so a full page cannot be walked past.
const issueViewsScopeCap = 200

// listCapTruncation reports a read whose row count reached the endpoint's cap.
func listCapTruncation(path string, rows int) *TransferListTruncation {
	base, _, _ := strings.Cut(path, "?")
	cap, ok := transferListCaps[base]
	if !ok || rows < cap.Limit {
		return nil
	}
	return &TransferListTruncation{Reason: cap.Reason, Limit: cap.Limit}
}

// listEnvelopeTruncation reports the pagination signals an envelope can carry.
// getList reads exactly one response, so an envelope that says "another page
// exists" can never be treated as the complete list.
func listEnvelopeTruncation(obj map[string]json.RawMessage) *TransferListTruncation {
	if v, ok := obj["has_more"]; ok {
		var hasMore bool
		if json.Unmarshal(v, &hasMore) == nil && hasMore {
			return &TransferListTruncation{Reason: gapReasonListHasMore}
		}
	}
	if v, ok := obj["next_cursor"]; ok && !isJSONNull(v) {
		var cursor any
		if json.Unmarshal(v, &cursor) == nil {
			switch c := cursor.(type) {
			case string:
				if c != "" {
					return &TransferListTruncation{Reason: gapReasonListHasMore}
				}
			case map[string]any:
				if len(c) > 0 {
					return &TransferListTruncation{Reason: gapReasonListHasMore}
				}
			}
		}
	}
	return nil
}

// getList decodes one list response. The second result is non-nil when that
// response may not be the whole list: the endpoint's per-request cap was
// reached, or the envelope advertised another page. Callers record it as an
// export gap and keep the decoded rows — reading one page and moving on is the
// silent truncation the manifest has to account for.
func getList(ctx context.Context, src TransferSourceClient, path string, dest *[]map[string]any) (*TransferListTruncation, error) {
	return decodeTransferList(ctx, src.GetJSON, path, dest)
}

// getOptionalList is getList for an endpoint that powers an optional subsystem
// (DENE-406). The read itself is not worth waiting minutes for: the caller
// records an export gap and exports the rest of the group either way.
func getOptionalList(ctx context.Context, src TransferSourceClient, path string, dest *[]map[string]any) (*TransferListTruncation, error) {
	return decodeTransferList(ctx, src.GetOptionalJSON, path, dest)
}

func decodeTransferList(ctx context.Context, read func(context.Context, string, any) error, path string, dest *[]map[string]any) (*TransferListTruncation, error) {
	var raw json.RawMessage
	if err := read(ctx, path, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		*dest = nil
		return nil, nil
	}
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, dest); err != nil {
			return nil, err
		}
		return listCapTruncation(path, len(*dest)), nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	for _, key := range []string{"items", "data", "labels", "statuses", "agents", "skills", "squads", "projects", "autopilots", "quick_actions", "issue_views", "issue_statuses", "properties", "members", "mcp_servers", "runtime_profiles", "runtimes", "plugins", "installations", "connections", "messages", "resources", "files"} {
		if v, ok := obj[key]; ok && len(v) > 0 && v[0] == '[' {
			if err := json.Unmarshal(v, dest); err != nil {
				return nil, err
			}
			if trunc := listEnvelopeTruncation(obj); trunc != nil {
				return trunc, nil
			}
			return listCapTruncation(path, len(*dest)), nil
		}
	}
	// No recognized list key: the endpoint renamed its envelope, or this
	// exporter never learned it. Reporting zero rows as if they were the whole
	// group is the failure mode that hid the issue-status catalog behind
	// {"statuses": ...}; name it instead.
	*dest = nil
	return &TransferListTruncation{Reason: gapReasonListShapeUnknown}, nil
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

const transferPartialFormat = "multica.workspace-transfer.partial"

type transferPartialState struct {
	Format               string   `json:"format"`
	OutPath              string   `json:"out_path"`
	WorkspaceRef         string   `json:"workspace_ref"`
	CompletedSessionIDs  []string `json:"completed_session_ids"`
	DownloadedBlobSHA256 []string `json:"downloaded_blob_sha256,omitempty"`
	// V3: the issues finished so far plus the comment watermark each one was
	// read up to. An issue missing from the set is re-walked from scratch,
	// because a single issue's comments are cheap to re-read (§8.5).
	CompletedIssueIDs     []string                          `json:"completed_issue_ids,omitempty"`
	IssueCommentWatermark map[string]TransferIssueWatermark `json:"issue_comment_watermark,omitempty"`
}

// TransferIssueWatermark is how far one issue's comment walk reached: the last
// created_at it saw and the comment ids it collected, so a resumed walk can
// tell re-read tie rows from new ones.
type TransferIssueWatermark struct {
	LastCreatedAt string   `json:"last_created_at,omitempty"`
	CommentIDs    []string `json:"comment_ids,omitempty"`
}

func newTransferPartialState(opts TransferExportOpts) *transferPartialState {
	abs, _ := filepath.Abs(opts.OutPath)
	return &transferPartialState{
		Format:       transferPartialFormat,
		OutPath:      abs,
		WorkspaceRef: opts.WorkspaceRef,
	}
}

func loadTransferPartial(dir, outPath, workspaceRef string) *transferPartialState {
	if dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil
	}
	var st transferPartialState
	if json.Unmarshal(data, &st) != nil {
		return nil
	}
	if st.Format != transferPartialFormat {
		return nil
	}
	absOut, _ := filepath.Abs(outPath)
	if st.OutPath != "" && absOut != "" && filepath.Clean(st.OutPath) != filepath.Clean(absOut) {
		return nil
	}
	if st.WorkspaceRef != "" && workspaceRef != "" && st.WorkspaceRef != workspaceRef {
		return nil
	}
	return &st
}

func loadPartialConversations(dir string, completed []string) ([]TransferSessionRow, map[string][]TransferMessageRow, []TransferAttachmentRow, map[string][]byte) {
	want := map[string]bool{}
	for _, id := range completed {
		if id != "" {
			want[id] = true
		}
	}
	sessRows := []TransferSessionRow{}
	msgBySession := map[string][]TransferMessageRow{}
	matches, _ := filepath.Glob(filepath.Join(dir, "conversations", "sessions-*.jsonl"))
	sort.Strings(matches)
	for _, sp := range matches {
		rows, err := decodeTransferJSONL[TransferSessionRow](sp)
		if err != nil {
			continue
		}
		msgPath := filepath.Join(filepath.Dir(sp), strings.Replace(filepath.Base(sp), "sessions-", "messages-", 1))
		msgs, _ := decodeTransferJSONL[TransferMessageRow](msgPath)
		bySess := map[string][]TransferMessageRow{}
		for _, m := range msgs {
			bySess[m.ChatSessionID] = append(bySess[m.ChatSessionID], m)
		}
		for _, s := range rows {
			if !want[s.SourceID] {
				continue
			}
			sessRows = append(sessRows, s)
			msgBySession[s.SourceID] = bySess[s.SourceID]
		}
	}
	var atts []TransferAttachmentRow
	if all, err := decodeTransferJSONL[TransferAttachmentRow](filepath.Join(dir, "attachments", "index.jsonl")); err == nil {
		for _, a := range all {
			if a.ChatSessionID != nil && want[*a.ChatSessionID] {
				atts = append(atts, a)
			}
		}
	}
	blobs := map[string][]byte{}
	entries, _ := os.ReadDir(filepath.Join(dir, "attachments", "blobs"))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "attachments", "blobs", e.Name()))
		if err == nil {
			blobs[e.Name()] = b
		}
	}
	return sessRows, msgBySession, atts, blobs
}

func persistCompletedSession(dir string, state *transferPartialState, sess TransferSessionRow, msgs []TransferMessageRow, atts []TransferAttachmentRow, blobs map[string][]byte) error {
	if dir == "" || state == nil {
		return nil
	}
	conv := filepath.Join(dir, "conversations")
	if err := os.MkdirAll(conv, 0o700); err != nil {
		return err
	}
	n := nextPartialShardIndex(dir)
	if err := os.WriteFile(filepath.Join(conv, fmt.Sprintf("sessions-%04d.jsonl", n)), encodeTransferJSONL([]TransferSessionRow{sess}), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(conv, fmt.Sprintf("messages-%04d.jsonl", n)), encodeTransferJSONL(msgs), 0o600); err != nil {
		return err
	}
	if err := persistPartialAttachments(dir, atts, blobs); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range state.CompletedSessionIDs {
		seen[id] = true
	}
	if !seen[sess.SourceID] {
		state.CompletedSessionIDs = append(state.CompletedSessionIDs, sess.SourceID)
	}
	for sha := range blobs {
		if sha == "" {
			continue
		}
		found := false
		for _, existing := range state.DownloadedBlobSHA256 {
			if existing == sha {
				found = true
				break
			}
		}
		if !found {
			state.DownloadedBlobSHA256 = append(state.DownloadedBlobSHA256, sha)
		}
	}
	return writeTransferPartialState(dir, state)
}

func persistPartialAttachments(dir string, atts []TransferAttachmentRow, blobs map[string][]byte) error {
	if len(atts) == 0 && len(blobs) == 0 {
		return nil
	}
	blobDir := filepath.Join(dir, "attachments", "blobs")
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		return err
	}
	if len(atts) > 0 {
		f, err := os.OpenFile(filepath.Join(dir, "attachments", "index.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.Write(encodeTransferJSONL(atts)); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	for sha, blob := range blobs {
		if sha == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(blobDir, sha), blob, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func writeTransferPartialState(dir string, state *transferPartialState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "state.json.tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "state.json"))
}

func nextPartialShardIndex(dir string) int {
	matches, _ := filepath.Glob(filepath.Join(dir, "conversations", "sessions-*.jsonl"))
	max := 0
	for _, m := range matches {
		var n int
		if _, err := fmt.Sscanf(filepath.Base(m), "sessions-%d.jsonl", &n); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}

func encodeTransferJSONL[T any](rows []T) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, row := range rows {
		_ = enc.Encode(row)
	}
	return buf.Bytes()
}

func decodeTransferJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var rows []T
	for {
		var row T
		if err := dec.Decode(&row); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}
