package service

import "time"

const (
	TransferBundleFormat          = "multica.workspace-transfer"
	TransferBundleSchemaVersion   = 1
	TransferConversationsMaxBytes = 20 << 20
	TransferAttachmentMaxBytes    = 25 << 20
	TransferMessageShardMaxBytes  = 16 << 20
	TransferMessageShardMaxRows   = 5000
	TransferAttachmentBodyMax     = 25 << 20
)

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

type TransferAttachmentMeta struct {
	SourceID          string  `json:"source_id"`
	ChatSessionID     *string `json:"chat_session_id"`
	ChatMessageID     *string `json:"chat_message_id"`
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
