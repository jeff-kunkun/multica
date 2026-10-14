package daemon

import (
	"encoding/json"
)

// AgentEntry describes a single available agent CLI.
type AgentEntry struct {
	Path string // stable startup-resolved CLI entry point; launch resolution may follow platform links to a concrete path
	// Command is the bare command name or MULTICA_*_PATH value that Path was
	// resolved from at startup. It is kept so the daemon can re-resolve Path
	// if the pinned executable later vanishes — e.g. a version manager
	// (Homebrew Cask, nvm/fnm) does an in-place upgrade that deletes the old
	// versioned directory Path points into. Empty for synthesized entries
	// (custom runtime profiles) that carry an absolute path directly. See
	// Daemon.resolveAgentEntry and MUL-4486.
	Command string
}

// Runtime represents a registered daemon runtime.
type Runtime struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	// ProfileID is non-empty when this runtime was registered from a
	// workspace custom runtime profile (MUL-3284). It links the runtime row
	// back to the profile so the daemon can resolve the profile's
	// command_name to the executable to launch. Built-in (provider-detected)
	// runtimes leave this empty.
	ProfileID string `json:"profile_id,omitempty"`
}

// RepoData holds repository information from the workspace.
type RepoData struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Ref         string `json:"ref,omitempty"`
}

// ProjectResourceData mirrors handler.ProjectResourceData — a single project
// resource as delivered to the daemon. resource_ref is type-specific JSON.
type ProjectResourceData struct {
	ID           string          `json:"id"`
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        string          `json:"label,omitempty"`
}

// IssueStatusData mirrors one active custom workspace status from the claim
// payload (MUL-6460). Mirror field: internal/handler/agent.go
// TaskIssueStatusData, same JSON names.
type IssueStatusData struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description,omitempty"`
}

// Task represents a claimed task from the server.
// Agent data (name, skills) is populated by the claim endpoint.
type Task struct {
	// StartClaimSupported gates retries when talking to older servers.
	StartClaimSupported bool   `json:"start_claim_supported,omitempty"`
	DispatchedAt        string `json:"dispatched_at,omitempty"`
	ID                  string `json:"id"`
	AgentID             string `json:"agent_id"`
	RuntimeID           string `json:"runtime_id"`
	IssueID             string `json:"issue_id"`
	WorkspaceID         string `json:"workspace_id"`
	WorkspaceSlug       string `json:"workspace_slug,omitempty"`
	IssueIdentifier     string `json:"issue_identifier,omitempty"`
	// WorkspaceContext mirrors workspace.context (the per-workspace system
	// prompt set in Settings → General). Server populates this on every claim
	// regardless of task kind so the daemon can inject `## Workspace Context`
	// into the brief. Empty when the owner hasn't set one.
	WorkspaceContext string `json:"workspace_context,omitempty"`
	// IssueStatuses mirrors the claim payload's active CUSTOM status catalog
	// (MUL-6460): key/name/category/description per status, already in catalog
	// order. Rendered into the brief's status-command line; empty (including on
	// old servers that never send the field) keeps the brief byte-identical to
	// the built-in-only form. IssueStatusesOmitted is the cap overflow count.
	IssueStatuses                 []IssueStatusData      `json:"issue_statuses,omitempty"`
	IssueStatusesOmitted          int                    `json:"issue_statuses_omitted,omitempty"`
	ThreadName                    string                 `json:"thread_name,omitempty"` // semantic title for provider-native session/thread history
	Agent                         *AgentData             `json:"agent,omitempty"`
	Repos                         []RepoData             `json:"repos,omitempty"`
	ProjectID                     string                 `json:"project_id,omitempty"`                       // active project for this task, when present
	ProjectTitle                  string                 `json:"project_title,omitempty"`                    // human-readable project title for context injection
	ProjectDescription            string                 `json:"project_description,omitempty"`              // durable project-level context injected into the brief
	ProjectResources              []ProjectResourceData  `json:"project_resources,omitempty"`                // project-scoped resources to expose to the agent
	PriorSessionID                string                 `json:"prior_session_id,omitempty"`                 // Claude session ID from a previous task on this issue
	PriorWorkDir                  string                 `json:"prior_work_dir,omitempty"`                   // work_dir from a previous task on this issue
	PriorSessionResumeUnavailable bool                   `json:"prior_session_resume_unavailable,omitempty"` // MUL-5305: server signals a more recent Codex session was withheld (rollout missing) and PriorSessionID (if any) is an older fallback; the run must disclose the continuity gap even if that older session resumes cleanly. Absent/false on old servers.
	TriggerCommentID              string                 `json:"trigger_comment_id,omitempty"`               // comment that triggered this task
	CoalescedCommentIDs           []string               `json:"coalesced_comment_ids,omitempty"`            // MUL-4195: earlier comments folded into this run while it was still queued; the agent must address these in addition to the (newest) triggering comment. Empty for old servers / non-merged runs
	CoalescedComments             []CoalescedCommentData `json:"coalesced_comments,omitempty"`               // MUL-4195: full detail of the folded comments (thread_id/author/created_at/content) so the prompt can address each without assuming a shared thread. Empty for old servers / non-merged runs
	TriggerThreadID               string                 `json:"trigger_thread_id,omitempty"`                // root comment ID for the triggering thread; falls back to trigger_comment_id on old servers
	TriggerCommentContent         string                 `json:"trigger_comment_content,omitempty"`          // content of the triggering comment
	TriggerAuthorType             string                 `json:"trigger_author_type,omitempty"`              // "agent" or "member" — author kind for the triggering comment
	TriggerAuthorName             string                 `json:"trigger_author_name,omitempty"`              // display name of the triggering comment author
	NewCommentCount               int                    `json:"new_comment_count,omitempty"`                // issue-wide comments since this agent's last run (excludes its own and the injected trigger); 0/omitted for old daemons or cold start
	NewCommentsSince              string                 `json:"new_comments_since,omitempty"`               // RFC3339 anchor (last run's started_at) the count is measured from; empty on cold start
	NewCommentsDeltaKnown         bool                   `json:"new_comments_delta_known,omitempty"`         // the server actually computed the issue-wide delta this claim (both reads succeeded). A zero NewCommentCount means "nothing was said" only when this is true; otherwise the zero is a failed read, a cold start, or an old server, and the prompt must not present it as the comment scan's answer (MUL-6984)
	IssueStateDeltaKnown          bool                   `json:"issue_state_delta_known,omitempty"`          // MUL-7344: the server compared the issue's title/description against the snapshot taken at this agent's previous run on this issue. Same contract as NewCommentsDeltaKnown — absent means NOT compared (cold start, no prior snapshot, read error, old server), and the prompt must then keep telling the agent to read the issue
	IssueChangedFields            []string               `json:"issue_changed_fields,omitempty"`             // subset of title,description in that order; empty alongside IssueStateDeltaKnown means unchanged. Fields outside that set (status, assignee, priority, labels, parent, due, stage, project, metadata) are not compared and must never be reported as checked; status and assignee ship their current values instead
	IssueStatus                   string                 `json:"issue_status,omitempty"`                     // the issue's status key at claim time; sent whether or not the delta is known
	IssueAssigneeType             string                 `json:"issue_assignee_type,omitempty"`              // "agent", "member" or "squad" at claim time; empty when unassigned
	IssueAssigneeID               string                 `json:"issue_assignee_id,omitempty"`                // assignee UUID at claim time; empty when unassigned
	ChatSessionID                 string                 `json:"chat_session_id,omitempty"`                  // non-empty for chat tasks
	ChatMessage                   string                 `json:"chat_message,omitempty"`                     // user message content for chat tasks
	ChatMessageAttachments        []ChatAttachmentMeta   `json:"chat_message_attachments,omitempty"`         // attachments linked to the chat message; agent uses these to `multica attachment download <id>`
	ChatIntro                     bool                   `json:"chat_intro,omitempty"`                       // legacy compatibility for historical is_agent_intro sessions; new agent creation no longer creates these chats
	HandoffNote                   string                 `json:"handoff_note,omitempty"`                     // legacy assignment handoff instruction; rendered only in the per-turn prompt

	// RequestingUserName + RequestingUserProfileDescription describe the human
	// the agent is working on behalf of. v1 sources them from the runtime
	// owner (the user who registered the daemon). Empty when the runtime has
	// no owner (cloud / system runtimes) or the user hasn't set a description.
	// Injected into the brief under `## Requesting User`; omitted entirely
	// when description is empty so the agent doesn't see a useless heading.
	RequestingUserName               string `json:"requesting_user_name,omitempty"`
	RequestingUserProfileDescription string `json:"requesting_user_profile_description,omitempty"`
	// Initiator* are the existing claim fields for the human whose authority
	// this run uses (originator_user_id). The direct comment trigger author is
	// carried separately in trigger_author_*. Empty when no originator exists.
	// The daemon renders ## On Behalf Of per turn; its effective credentials
	// remain scoped to the runtime owner. See MUL-2645, GH-8674.
	InitiatorType  string `json:"initiator_type,omitempty"`
	InitiatorID    string `json:"initiator_id,omitempty"`
	InitiatorName  string `json:"initiator_name,omitempty"`
	InitiatorEmail string `json:"initiator_email,omitempty"`
	// AuthToken is the task-scoped credential the server mints at claim time.
	// The daemon injects it into the spawned agent as MULTICA_TOKEN so the
	// agent never sees the daemon's own (often workspace-owner) credential.
	// Empty or non-task-scoped values are fatal for writable agent tasks; the
	// daemon must not fall back to its own token. See MUL-3292.
	AuthToken string `json:"auth_token,omitempty"`
}

// ChatAttachmentMeta is the structured attachment metadata the daemon
// hands to the agent for chat tasks. We pass id + filename + content_type
// so the chat prompt can list them explicitly and instruct the agent to
// run `multica attachment download <id>` instead of guessing from a
// signed CDN URL (which expires).
type ChatAttachmentMeta struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
}

// CoalescedCommentData mirrors the server-side struct (handler.CoalescedCommentData):
// the full detail of a comment folded into this run while it was still queued
// (MUL-4195). The prompt embeds each one directly so the agent addresses every
// folded comment without assuming they all live in the triggering thread.
type CoalescedCommentData struct {
	ID         string `json:"id"`
	ThreadID   string `json:"thread_id,omitempty"`
	AuthorType string `json:"author_type,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	Content    string `json:"content"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// AgentData holds agent details returned by the claim endpoint.
type AgentData struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Instructions string            `json:"instructions"`
	CustomEnv    map[string]string `json:"custom_env,omitempty"`
	CustomArgs   []string          `json:"custom_args,omitempty"`
	McpConfig    json.RawMessage   `json:"mcp_config,omitempty"`
	// RuntimeConfig is the per-provider runtime_config JSON as stored on
	// the agent record, forwarded verbatim by the claim endpoint. The
	// daemon decodes provider-specific fields (e.g. openclaw mode +
	// gateway endpoint, see issue #3260); other backends ignore it.
	RuntimeConfig json.RawMessage `json:"runtime_config,omitempty"`
}

// TaskUsageEntry represents token usage for a single model during a task execution.
type TaskUsageEntry struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	// CostUSDTicks is the provider's own price for this usage, in 1e-10 USD.
	// Omitted when the agent reports no cost, which is the common case — the
	// server then leaves the column NULL and the client estimates from the
	// pricing table instead. See agent.TokenUsage.CostUSDTicks.
	CostUSDTicks int64 `json:"cost_usd_ticks,omitempty"`
}

// TaskResult is the outcome of executing a task.
type TaskResult struct {
	Status        string `json:"status"`
	Comment       string `json:"comment"`
	EnvType       string `json:"env_type,omitempty"`
	SessionID     string `json:"session_id,omitempty"` // Claude session ID for future resumption
	WorkDir       string `json:"work_dir,omitempty"`   // working directory used during execution
	EnvRoot       string `json:"-"`                    // env root dir for writing GC metadata (not sent to server)
	FailureReason string `json:"-"`                    // classifier forwarded to FailTask on the blocked path; empty falls back to 'agent_error'
	// SessionRolloutMissing is set when the daemon withheld this task's Codex
	// session because its rollout was not in the store (MUL-5305). Forwarded to
	// the terminal report so the server clears the resume pointer and flags the
	// continuity gap for the next claim. Not part of the wire result itself.
	SessionRolloutMissing bool `json:"-"`
	// RetiredSessionID names a session this run was told to resume and then
	// abandoned as unresumable (GH #6066). Forwarded on every terminal path,
	// including the completed one: a fresh-session retry that SUCCEEDS is
	// precisely when the abandoned id would otherwise stay selectable.
	RetiredSessionID string           `json:"-"`
	Usage            []TaskUsageEntry `json:"usage,omitempty"` // per-model token usage
}
