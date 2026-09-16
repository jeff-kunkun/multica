package service

import (
	"encoding/json"
	"time"
)

const (
	TransferBundleFormat = "multica.workspace-transfer"
	// Schema versions are taken from the bundle's content, never from the CLI
	// version (V3 contract §9.1/§9.2): a bundle without the `issues` group is
	// still version 1 so an un-upgraded target can read it, and only a bundle
	// that actually carries issues is version 2.
	TransferBundleSchemaVersionV1 = 1
	TransferBundleSchemaVersionV2 = 2
	// TransferBundleSchemaVersion is the newest version this code understands.
	TransferBundleSchemaVersion   = TransferBundleSchemaVersionV2
	TransferConversationsMaxBytes = 20 << 20
	TransferAttachmentMaxBytes    = 25 << 20
	TransferMessageShardMaxBytes  = 16 << 20
	TransferMessageShardMaxRows   = 5000
	TransferAttachmentBodyMax     = 25 << 20
)

// TransferIncludeIssues is the V3 `--include` group that turns a V2 bundle
// into a V3 one.
const TransferIncludeIssues = "issues"

// TransferBundleSchemaVersionForContent picks the outer schema_version from
// what the bundle carries rather than from the CLI's own version: a bundle
// without the issues group stays readable by a target that has not upgraded
// past V2 (contract §9.2, last row).
func TransferBundleSchemaVersionForContent(include []string) int {
	for _, group := range include {
		if group == TransferIncludeIssues {
			return TransferBundleSchemaVersionV2
		}
	}
	return TransferBundleSchemaVersionV1
}

type TransferManifest struct {
	Format        string                `json:"format"`
	SchemaVersion int                   `json:"schema_version"`
	BundleID      string                `json:"bundle_id"`
	ExportedAt    time.Time             `json:"exported_at"`
	Exporter      TransferExporterInfo  `json:"exporter"`
	Source        TransferSourceInfo    `json:"source"`
	Options       TransferExportOptions `json:"options"`
	Files         []TransferFileMeta    `json:"files"`
	Refs          TransferRefs          `json:"refs"`
	ExportGaps    []TransferExportGap   `json:"export_gaps"`
	Stats         map[string]int        `json:"stats"`
}

type TransferExporterInfo struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
}

type TransferSourceInfo struct {
	BaseURLHost   string `json:"base_url_host"`
	ServerVersion string `json:"server_version"`
	WorkspaceID   string `json:"workspace_id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	ExportedBy    string `json:"exported_by"`
}

type TransferExportOptions struct {
	Include              []string `json:"include"`
	ExcludeArchivedChats bool     `json:"exclude_archived_chats"`
	People               bool     `json:"people"`
}

type TransferFileMeta struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
	Rows   int    `json:"rows,omitempty"`
}

type TransferRefs struct {
	Agents       map[string]TransferAgentRef `json:"agents,omitempty"`
	SystemAgents map[string]TransferAgentRef `json:"system_agents,omitempty"`
	Projects     map[string]TransferProjRef  `json:"projects,omitempty"`
	// V3 refs. Members / Squads / Issues / IssueStatuses drive mention
	// rewriting and status downgrade; IssueProperties exists because issue
	// property values are keyed by the source workspace's property definition
	// uuid, which the config import replaces with a fresh one on the target.
	Members         map[string]TransferMemberRef   `json:"members,omitempty"`
	Squads          map[string]TransferSquadRef    `json:"squads,omitempty"`
	Issues          map[string]TransferIssueRef    `json:"issues,omitempty"`
	IssueStatuses   map[string]string              `json:"issue_statuses,omitempty"`
	IssueProperties map[string]TransferPropertyRef `json:"issue_properties,omitempty"`
}

type TransferMemberRef struct {
	Email string `json:"email,omitempty"`
}

type TransferSquadRef struct {
	Name string `json:"name,omitempty"`
}

type TransferIssueRef struct {
	Number     int32  `json:"number,omitempty"`
	Identifier string `json:"identifier,omitempty"`
}

type TransferPropertyRef struct {
	Name string `json:"name,omitempty"`
}

type TransferAgentRef struct {
	Name      string `json:"name,omitempty"`
	SystemKey string `json:"system_key,omitempty"`
}

type TransferProjRef struct {
	Title string `json:"title"`
}

type TransferExportGap struct {
	Group  string `json:"group"`
	Reason string `json:"reason"`
	Status int    `json:"status,omitempty"`
	// Limit is the server-side per-request cap or page size that ended the
	// read, set only for list_cap_reached / list_has_more gaps.
	Limit int `json:"limit,omitempty"`
}

type TransferPerson struct {
	SourceUserID string `json:"source_user_id"`
	Email        string `json:"email"`
	Role         string `json:"role"`
}

type TransferRuntimeProfile struct {
	SourceID       string   `json:"source_id"`
	DisplayName    string   `json:"display_name"`
	ProtocolFamily string   `json:"protocol_family"`
	CommandName    string   `json:"command_name"`
	Description    string   `json:"description,omitempty"`
	FixedArgs      []string `json:"fixed_args"`
	Visibility     string   `json:"visibility"`
	Enabled        bool     `json:"enabled"`
}

type TransferRuntimesFile struct {
	Profiles     []TransferRuntimeProfile `json:"profiles"`
	RuntimesHint []TransferRuntimeHint    `json:"runtimes_hint"`
	// AgentHints link each exported agent to the runtime it ran on at the
	// source, which is the only way the target can tell which provider,
	// runtime mode and custom profile that agent expects. They live in the
	// outer runtimes file rather than in the V1 config bundle because the link
	// is a cross-instance fact (DENE-364).
	AgentHints []TransferAgentRuntimeHint `json:"agent_hints"`
}

type TransferRuntimeHint struct {
	SourceRuntimeID string `json:"source_runtime_id"`
	Provider        string `json:"provider,omitempty"`
	RuntimeMode     string `json:"runtime_mode,omitempty"`
	ProfileSourceID string `json:"profile_source_id,omitempty"`
	DisplayName     string `json:"display_name,omitempty"`
}

// TransferAgentRuntimeHint is the per-agent half of the binding hint: the
// source runtime an agent ran on, flattened to the three facts the target
// matches on (provider, runtime mode, custom profile display name).
type TransferAgentRuntimeHint struct {
	SourceAgentID   string `json:"source_agent_id"`
	SourceRuntimeID string `json:"source_runtime_id,omitempty"`
	Provider        string `json:"provider,omitempty"`
	RuntimeMode     string `json:"runtime_mode,omitempty"`
	ProfileSourceID string `json:"profile_source_id,omitempty"`
	ProfileName     string `json:"profile_name,omitempty"`
	RuntimeName     string `json:"runtime_name,omitempty"`
}

type TransferPreferences struct {
	PinnedAgents []TransferPinnedAgent `json:"pinned_agents"`
}

type TransferPinnedAgent struct {
	AgentID  string  `json:"agent_id"`
	Position float64 `json:"position"`
}

type TransferSessionRow struct {
	SourceID            string  `json:"source_id"`
	AgentID             string  `json:"agent_id"`
	ProjectID           *string `json:"project_id"`
	Title               string  `json:"title"`
	Status              string  `json:"status"`
	PinnedAt            *string `json:"pinned_at"`
	IsAgentIntro        bool    `json:"is_agent_intro"`
	ExplicitlyCreatedAt *string `json:"explicitly_created_at"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
	ChannelType         *string `json:"channel_type"`
	HadPendingTask      bool    `json:"had_pending_task"`
}

type TransferMessageRow struct {
	SourceID      string   `json:"source_id"`
	ChatSessionID string   `json:"chat_session_id"`
	Role          string   `json:"role"`
	MessageKind   string   `json:"message_kind"`
	Content       string   `json:"content"`
	FailureReason *string  `json:"failure_reason"`
	ElapsedMs     *int64   `json:"elapsed_ms"`
	CreatedAt     string   `json:"created_at"`
	AttachmentIDs []string `json:"attachment_ids"`
}

type TransferAttachmentRow struct {
	SourceID          string  `json:"source_id"`
	ChatSessionID     *string `json:"chat_session_id"`
	ChatMessageID     *string `json:"chat_message_id"`
	IssueID           *string `json:"issue_id"`
	CommentID         *string `json:"comment_id"`
	Filename          string  `json:"filename"`
	ContentType       string  `json:"content_type"`
	SizeBytes         int64   `json:"size_bytes"`
	CreatedAt         string  `json:"created_at"`
	SHA256            string  `json:"sha256,omitempty"`
	Body              string  `json:"body,omitempty"`
	BodyOmittedReason *string `json:"body_omitted_reason"`
}

type TransferConfigRequest struct {
	Manifest        TransferManifest     `json:"manifest"`
	People          []TransferPerson     `json:"people"`
	RuntimeProfiles TransferRuntimesFile `json:"runtime_profiles"`
	Preferences     TransferPreferences  `json:"preferences"`
	Config          ConfigBundle         `json:"config"`
	SecretsOmitted  []SecretOmitted      `json:"secrets_omitted"`
	DryRun          *bool                `json:"dry_run"`
	OnConflict      string               `json:"on_conflict"`
	// Options are the config-import switches the transfer path used to drop on
	// the floor, which is why every imported autopilot landed paused (DENE-363).
	// An absent options object keeps the zero value and therefore the behavior
	// older clients already observe.
	Options ConfigImportOptions `json:"options"`
}

type TransferConversationsRequest struct {
	Refs     TransferRefs         `json:"refs"`
	Sessions []TransferSessionRow `json:"sessions"`
	Messages []TransferMessageRow `json:"messages"`
	DryRun   *bool                `json:"dry_run"`
	Finalize bool                 `json:"finalize"`
}

// TransferIssueRow is one line of issues/issues-*.jsonl. StatusCategory rides
// along so the import can downgrade a status key the target catalog does not
// know without re-deriving the category from the key.
type TransferIssueRow struct {
	SourceID       string          `json:"source_id"`
	Number         int32           `json:"number"`
	Title          string          `json:"title"`
	Description    *string         `json:"description"`
	Status         string          `json:"status"`
	StatusCategory string          `json:"status_category,omitempty"`
	Priority       string          `json:"priority"`
	AssigneeType   *string         `json:"assignee_type"`
	AssigneeID     *string         `json:"assignee_id"`
	CreatorType    string          `json:"creator_type"`
	CreatorID      string          `json:"creator_id"`
	ParentIssueID  *string         `json:"parent_issue_id"`
	ProjectID      *string         `json:"project_id"`
	Position       float64         `json:"position"`
	Stage          *int32          `json:"stage"`
	StartDate      *string         `json:"start_date"`
	DueDate        *string         `json:"due_date"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
	LastActivityAt string          `json:"last_activity_at"`
	Metadata       json.RawMessage `json:"metadata"`
	Properties     json.RawMessage `json:"properties"`
}

// TransferCommentRow is one line of issues/comments-*.jsonl. Tombstones are
// exported with an empty content: their only job is to keep their replies on a
// direct parent pointer instead of flattening the thread.
type TransferCommentRow struct {
	SourceID       string   `json:"source_id"`
	IssueID        string   `json:"issue_id"`
	AuthorType     string   `json:"author_type"`
	AuthorID       string   `json:"author_id"`
	Content        string   `json:"content"`
	Type           string   `json:"type"`
	ParentID       *string  `json:"parent_id"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	ResolvedAt     *string  `json:"resolved_at"`
	ResolvedByType *string  `json:"resolved_by_type"`
	ResolvedByID   *string  `json:"resolved_by_id"`
	DeletedAt      *string  `json:"deleted_at"`
	AttachmentIDs  []string `json:"attachment_ids"`
}

// Relation kinds carried by issues/relations.jsonl. Three shapes share one
// JSONL file, so the import dispatches on Kind.
const (
	TransferRelationIssueLabel      = "issue_label"
	TransferRelationIssueReaction   = "issue_reaction"
	TransferRelationCommentReaction = "comment_reaction"
)

type TransferRelationRow struct {
	Kind              string `json:"kind"`
	IssueID           string `json:"issue_id,omitempty"`
	CommentID         string `json:"comment_id,omitempty"`
	LabelResourceType string `json:"label_resource_type,omitempty"`
	LabelName         string `json:"label_name,omitempty"`
	ActorType         string `json:"actor_type,omitempty"`
	ActorID           string `json:"actor_id,omitempty"`
	Emoji             string `json:"emoji,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
}

type TransferIssuesRequest struct {
	Refs      TransferRefs          `json:"refs"`
	Issues    []TransferIssueRow    `json:"issues"`
	Comments  []TransferCommentRow  `json:"comments"`
	Relations []TransferRelationRow `json:"relations"`
	DryRun    *bool                 `json:"dry_run"`
	Finalize  bool                  `json:"finalize"`
}

// TransferIssueLimitPolicy echoes what the entitlement provider told us about
// the workspace's issue quota. It is reported even when nothing is enforced so
// a quota rejection is never a silent line in a log.
type TransferIssueLimitPolicy struct {
	Action string `json:"action"`
	Limit  int64  `json:"limit,omitempty"`
	Used   int64  `json:"used,omitempty"`
}

type TransferIssuesReport struct {
	Applied          bool `json:"applied"`
	IssuesCreated    int  `json:"issues_created"`
	IssuesSkipped    int  `json:"issues_skipped"`
	CommentsCreated  int  `json:"comments_created"`
	CommentsSkipped  int  `json:"comments_skipped"`
	LabelsCreated    int  `json:"labels_created"`
	LabelsSkipped    int  `json:"labels_skipped"`
	ReactionsCreated int  `json:"reactions_created"`
	ReactionsSkipped int  `json:"reactions_skipped"`
	// SubscribersCreated counts the creator / assignee rows rebuilt at
	// finalize; the event listeners that normally write them never run here.
	SubscribersCreated int `json:"subscribers_created"`
	SubscribersSkipped int `json:"subscribers_skipped"`
	// ParentsBackfilled counts the second-pass parent pointers actually moved.
	ParentsBackfilled int  `json:"parents_backfilled"`
	Finalized         bool `json:"finalized"`
	// IssueCounter is the workspace watermark after finalize.
	IssueCounter int32 `json:"issue_counter,omitempty"`
	// IssueLimit is the resolved quota policy, echoed before any write.
	IssueLimit         *TransferIssueLimitPolicy `json:"issue_limit,omitempty"`
	StatusUnmapped     []UnmappedRef             `json:"status_unmapped,omitempty"`
	AssigneeUnmapped   []UnmappedRef             `json:"assignee_unmapped,omitempty"`
	CreatorUnmapped    []UnmappedRef             `json:"creator_unmapped,omitempty"`
	ProjectUnmapped    []UnmappedRef             `json:"project_unmapped,omitempty"`
	AuthorUnmapped     []UnmappedRef             `json:"comment_author_unmapped,omitempty"`
	ResolutionUnmapped []UnmappedRef             `json:"resolution_actor_unmapped,omitempty"`
	PropertyUnmapped   []UnmappedRef             `json:"property_unmapped,omitempty"`
	ParentUnmapped     []UnmappedRef             `json:"parent_unmapped,omitempty"`
	// ParentReparentedToAncestor records each comment whose source parent was
	// outside the bundle and that was therefore hung on a further ancestor.
	ParentReparentedToAncestor []UnmappedRef `json:"parent_reparented_to_ancestor,omitempty"`
	MentionUnmapped            []UnmappedRef `json:"mention_unmapped,omitempty"`
	// MentionUnmappedByType summarizes the rows above, because a few thousand
	// individual rows are unreadable on a terminal.
	MentionUnmappedByType map[string]int `json:"mention_unmapped_by_type,omitempty"`
	LabelUnmapped         []UnmappedRef  `json:"label_unmapped,omitempty"`
	ReactionUnmapped      []UnmappedRef  `json:"reaction_actor_unmapped,omitempty"`
}

type TransferAttachmentMeta struct {
	SourceID          string  `json:"source_id"`
	ChatSessionID     *string `json:"chat_session_id"`
	ChatMessageID     *string `json:"chat_message_id"`
	IssueID           *string `json:"issue_id"`
	CommentID         *string `json:"comment_id"`
	Filename          string  `json:"filename"`
	ContentType       string  `json:"content_type"`
	SizeBytes         int64   `json:"size_bytes"`
	CreatedAt         string  `json:"created_at"`
	SHA256            string  `json:"sha256,omitempty"`
	BodyOmittedReason *string `json:"body_omitted_reason"`
}

type TransferConfigReport struct {
	ConfigReport   *ConfigImportReport    `json:"config_report"`
	PeopleMap      []TransferPeopleMapRow `json:"people_map"`
	RuntimesToBind []TransferRuntimeBind  `json:"runtimes_to_bind"`
	Profiles       []ConfigImportItem     `json:"profiles,omitempty"`
	PinnedAgents   []ConfigImportItem     `json:"pinned_agents,omitempty"`
	Warnings       []ConfigWarning        `json:"warnings,omitempty"`
	ExportGaps     []TransferExportGap    `json:"export_gaps,omitempty"`
}

type TransferPeopleMapRow struct {
	SourceUserID string `json:"source_user_id"`
	TargetUserID string `json:"target_user_id,omitempty"`
	Mapped       bool   `json:"mapped"`
	Reason       string `json:"reason,omitempty"`
}

// Runtime binding report statuses. The three-tier rule from DENE-364 lands here:
// `bound` means the row was written, `pending` means a human still has to pick
// (or the auto-bind switch is off), `no_candidate` means the target has nothing
// that matches and the report explains what to connect first.
const (
	RuntimeBindBound       = "bound"
	RuntimeBindPending     = "pending"
	RuntimeBindNoCandidate = "no_candidate"
)

// Reasons a binding could not be planned. They are codes so the Desktop card
// can render a localized, actionable sentence; `Reason` carries the same thing
// in English for CLI and log readers.
const (
	// RuntimeBindReasonProviderUnknown: the bundle predates per-agent runtime
	// hints, so the target cannot know which provider the agent expects.
	RuntimeBindReasonProviderUnknown = "runtime_provider_unknown"
	// RuntimeBindReasonNoRuntime: the target workspace has no runtime matching
	// the source provider / mode / profile.
	RuntimeBindReasonNoRuntime = "no_runtime_for_provider"
	// RuntimeBindReasonBindFailed: a bind was attempted and the write failed
	// (permission, missing runtime, database). Reason carries the detail.
	RuntimeBindReasonBindFailed = "runtime_bind_failed"
)

// TransferRuntimeBind is one imported agent's runtime-binding row. It is both
// the plan (dry run) and the outcome (a real import): `Status` moves from
// pending to bound once the target wrote the pointer.
type TransferRuntimeBind struct {
	SourceAgentID string `json:"source_agent_id,omitempty"`
	AgentTargetID string `json:"agent_target_id,omitempty"`
	AgentName     string `json:"agent_name,omitempty"`
	// Source side: what the agent ran on in the source environment.
	Provider    string `json:"provider,omitempty"`
	RuntimeMode string `json:"runtime_mode,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
	// Status is one of RuntimeBindBound / RuntimeBindPending /
	// RuntimeBindNoCandidate.
	Status string `json:"status"`
	// ReasonCode / Reason explain a no_candidate row or a failed bind.
	ReasonCode string `json:"reason_code,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// BoundRuntimeID / BoundRuntimeName are set once Status is bound.
	BoundRuntimeID   string `json:"bound_runtime_id,omitempty"`
	BoundRuntimeName string `json:"bound_runtime_name,omitempty"`
	// Candidates are the target runtimes the agent could run on. Exactly one
	// means the auto-bind rule may act on it without guessing.
	CandidateIDs []string                   `json:"candidate_ids,omitempty"`
	Candidates   []TransferRuntimeCandidate `json:"candidates,omitempty"`
}

type TransferRuntimeCandidate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider,omitempty"`
	RuntimeMode string `json:"runtime_mode,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
}

// TransferBindRuntimesRequest is the explicit binding action the Desktop card
// sends for the agents the auto-bind rule deliberately left alone.
type TransferBindRuntimesRequest struct {
	Bindings []TransferRuntimeBinding `json:"bindings"`
}

type TransferRuntimeBinding struct {
	AgentID   string `json:"agent_id"`
	RuntimeID string `json:"runtime_id"`
}

// TransferBindRuntimesReport answers one line per requested binding so the card
// can mark the failed rows instead of losing the successful ones.
type TransferBindRuntimesReport struct {
	Applied  bool                            `json:"applied"`
	Bound    int                             `json:"bound"`
	Failed   int                             `json:"failed"`
	Bindings []TransferRuntimeBindingOutcome `json:"bindings"`
}

type TransferRuntimeBindingOutcome struct {
	AgentID     string `json:"agent_id"`
	RuntimeID   string `json:"runtime_id"`
	AgentName   string `json:"agent_name,omitempty"`
	RuntimeName string `json:"runtime_name,omitempty"`
	Bound       bool   `json:"bound"`
	ErrorCode   string `json:"error_code,omitempty"`
	Error       string `json:"error,omitempty"`
}

type TransferConversationsReport struct {
	Applied         bool          `json:"applied"`
	SessionsCreated int           `json:"sessions_created"`
	SessionsSkipped int           `json:"sessions_skipped"`
	MessagesCreated int           `json:"messages_created"`
	MessagesSkipped int           `json:"messages_skipped"`
	AgentUnmapped   []UnmappedRef `json:"agent_unmapped,omitempty"`
	ProjectUnmapped []UnmappedRef `json:"project_unmapped,omitempty"`
	ChannelDetached int           `json:"channel_source_detached"`
	Finalized       bool          `json:"finalized"`
}

type TransferAttachmentReport struct {
	Applied    bool   `json:"applied"`
	Created    bool   `json:"created"`
	Skipped    bool   `json:"skipped"`
	TargetID   string `json:"target_id,omitempty"`
	BodyStored bool   `json:"body_stored"`
	Reason     string `json:"reason,omitempty"`
}
