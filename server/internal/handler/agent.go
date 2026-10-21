package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Mirrors AGENT_DESCRIPTION_MAX_LENGTH in packages/core/agents/constants.ts
// and the agent_description_length CHECK constraint in migration 060. Counted
// in unicode code points (utf8.RuneCountInString), matching Postgres
// char_length and the front-end's String.prototype.length-with-counter UX.
const maxAgentDescriptionLength = 255

const (
	maxAgentConversationStarters      = 3
	maxAgentConversationStarterLabel  = 80
	maxAgentConversationStarterLength = 4000
)

type AgentConversationStarter struct {
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
}

type AgentResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	// RuntimeID is the empty string when the agent is unbound — it kept its
	// configuration and history when its runtime was deleted, and needs a new
	// runtime before it can run again (MUL-5559). The wire type stays a string
	// so installed clients keep parsing; RuntimeBound is the explicit signal.
	RuntimeID string `json:"runtime_id"`
	// RuntimeBound is false exactly when the agent has no runtime. UI should
	// branch on this rather than on RuntimeID being falsy, and must not confuse
	// it with a bound-but-offline runtime (a different user story: reconnect the
	// machine vs. pick a new one).
	RuntimeBound bool `json:"runtime_bound"`
	// RuntimeAvailability is the coarse liveness projection for a runtime that
	// may be hidden from the caller's runtime list. It deliberately carries no
	// timestamp, device, owner, configuration, or credential fields; clients
	// use it only when the full runtime row is unavailable.
	RuntimeAvailability string `json:"runtime_availability,omitempty"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	// Instructions is what this agent's owner wrote.
	Instructions string `json:"instructions"`
	// ConversationStarters are optional, agent-specific first-turn suggestions. An
	// empty list tells clients to render their localized fallback prompts.
	ConversationStarters []AgentConversationStarter `json:"conversation_starters"`
	// SystemKey identifies a product-defined agent. Empty for
	// every user- or template-created agent. The UI keys "this is maintained
	// by Multica" off this rather than off the display name, which owners may
	// change.
	SystemKey     string   `json:"system_key,omitempty"`
	AvatarURL     *string  `json:"avatar_url"`
	RuntimeMode   string   `json:"runtime_mode"`
	RuntimeConfig any      `json:"runtime_config"`
	CustomArgs    []string `json:"custom_args"`
	// custom_env is intentionally NOT serialized on agent resources. The
	// agent_list/get/create/update/archive/restore responses and WS events
	// only expose coarse metadata (has_custom_env, custom_env_key_count) so
	// the UI can show "N variables configured" without dragging secrets
	// across the API surface. Reading values requires the dedicated, audited
	// `GET /api/agents/{id}/env` endpoint; writing requires `PUT` to the
	// same path. agent-actor tokens are denied there. See MUL-2600.
	HasCustomEnv      bool   `json:"has_custom_env"`
	CustomEnvKeyCount int    `json:"custom_env_key_count"`
	Visibility        string `json:"visibility"`
	// PermissionMode is the invocation-permission mode (MUL-3963):
	// "private" (owner only) or "public_to" (allow-list in InvocationTargets).
	// Replaces Visibility as the authorization source; Visibility is kept as a
	// derived legacy field so old clients never see a permission widening.
	PermissionMode string `json:"permission_mode"`
	// InvocationTargets is the allow-list for a public_to agent. Empty for
	// private agents. Only populated on the detail / list / create / update
	// responses that load it; broadcast payloads leave it empty.
	InvocationTargets  []AgentInvocationTargetDTO `json:"invocation_targets"`
	Status             string                     `json:"status"`
	MaxConcurrentTasks int32                      `json:"max_concurrent_tasks"`
	OwnerID            *string                    `json:"owner_id"`
	CreatedAt          string                     `json:"created_at"`
	UpdatedAt          string                     `json:"updated_at"`
	ArchivedAt         *string                    `json:"archived_at"`
	ArchivedBy         *string                    `json:"archived_by"`
}

// runtimeConfigGatewayTokenMask is the placeholder the API substitutes for
// any non-empty `runtime_config.gateway.token` (openclaw gateway mode, issue
// #3260). The token is a bearer credential; surfacing the real value through
// GET responses would let anyone with read access to the agent dump the
// gateway secret. The mask is a sentinel — when the UI later PATCHes the
// agent and submits the same mask verbatim under that field, the update
// handler restores the persisted token instead of overwriting it.
const runtimeConfigGatewayTokenMask = "***"

func (h *Handler) agentToResponse(a db.Agent) AgentResponse {
	var rc any
	if a.RuntimeConfig != nil {
		json.Unmarshal(a.RuntimeConfig, &rc)
	}
	if rc == nil {
		rc = map[string]any{}
	}
	maskGatewayToken(rc)

	// Compute env metadata WITHOUT exposing the values. We unmarshal here
	// only to count keys; the map never reaches the response. A coarse
	// has_custom_env / key_count is what the UI gets — to read the values
	// the caller must hit GET /api/agents/{id}/env (agent owner or
	// workspace owner, audited).
	envKeyCount := 0
	if a.CustomEnv != nil {
		var customEnv map[string]string
		if err := json.Unmarshal(a.CustomEnv, &customEnv); err != nil {
			slog.Warn("failed to unmarshal agent custom_env", "agent_id", uuidToString(a.ID), "error", err)
		}
		envKeyCount = len(customEnv)
	}

	var customArgs []string
	if a.CustomArgs != nil {
		if err := json.Unmarshal(a.CustomArgs, &customArgs); err != nil {
			slog.Warn("failed to unmarshal agent custom_args", "agent_id", uuidToString(a.ID), "error", err)
		}
	}
	if customArgs == nil {
		customArgs = []string{}
	}

	conversationStarters := []AgentConversationStarter{}
	if len(a.ConversationStarters) > 0 {
		if err := json.Unmarshal(a.ConversationStarters, &conversationStarters); err != nil {
			slog.Warn("failed to unmarshal agent conversation_starters", "agent_id", uuidToString(a.ID), "error", err)
			conversationStarters = []AgentConversationStarter{}
		}
	}

	return AgentResponse{
		ID:                   uuidToString(a.ID),
		WorkspaceID:          uuidToString(a.WorkspaceID),
		RuntimeID:            uuidToString(a.RuntimeID),
		RuntimeBound:         a.RuntimeID.Valid,
		Name:                 a.Name,
		Description:          a.Description,
		Instructions:         a.Instructions,
		ConversationStarters: conversationStarters,
		SystemKey:            a.SystemKey.String,
		AvatarURL:            h.resolveAvatarURLPtr(textToPtr(a.AvatarUrl)),
		RuntimeMode:          a.RuntimeMode,
		RuntimeConfig:        rc,
		CustomArgs:           customArgs,
		HasCustomEnv:         envKeyCount > 0,
		CustomEnvKeyCount:    envKeyCount,
		Visibility:           a.Visibility,
		PermissionMode:       a.PermissionMode,
		InvocationTargets:    []AgentInvocationTargetDTO{},
		Status:               a.Status,
		MaxConcurrentTasks:   a.MaxConcurrentTasks,
		OwnerID:              uuidToPtr(a.OwnerID),
		CreatedAt:            timestampToString(a.CreatedAt),
		UpdatedAt:            timestampToString(a.UpdatedAt),
		ArchivedAt:           timestampToPtr(a.ArchivedAt),
		ArchivedBy:           uuidToPtr(a.ArchivedBy),
	}
}

// maskGatewayToken replaces runtime_config.gateway.token with the public
// mask sentinel when a non-empty value is present. No-op for any other
// shape so non-openclaw / non-gateway agents pass through untouched.
func maskGatewayToken(rc any) {
	root, ok := rc.(map[string]any)
	if !ok {
		return
	}
	gw, ok := root["gateway"].(map[string]any)
	if !ok {
		return
	}
	tok, _ := gw["token"].(string)
	if tok == "" {
		return
	}
	gw["token"] = runtimeConfigGatewayTokenMask
}

// preserveMaskedGatewayToken substitutes the previously persisted gateway
// token back into an incoming runtime_config when the request submitted the
// public mask sentinel under `gateway.token`. Without this the next PATCH
// after a GET would round-trip the masked sentinel into the database and
// silently destroy the real secret. The previous value is taken from the
// agent row the handler has just loaded for ownership / scoping checks.
func preserveMaskedGatewayToken(incoming any, persistedRuntimeConfig []byte) {
	root, ok := incoming.(map[string]any)
	if !ok {
		return
	}
	gw, ok := root["gateway"].(map[string]any)
	if !ok {
		return
	}
	tok, _ := gw["token"].(string)
	if tok != runtimeConfigGatewayTokenMask {
		return
	}
	// The incoming token is the mask — fish the real one out of the row.
	var prev struct {
		Gateway struct {
			Token string `json:"token"`
		} `json:"gateway"`
	}
	if len(persistedRuntimeConfig) == 0 {
		// No prior token to keep; the field becomes effectively empty.
		delete(gw, "token")
		return
	}
	if err := json.Unmarshal(persistedRuntimeConfig, &prev); err != nil || prev.Gateway.Token == "" {
		delete(gw, "token")
		return
	}
	gw["token"] = prev.Gateway.Token
}

// RepoData holds repository information included in claim responses so the
// daemon can set up worktrees for each workspace repo.
type RepoData struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Ref         string `json:"ref,omitempty"`
}

// ProjectResourceData is the wire shape for a project resource included in a
// claim response. The daemon reads this list and writes it into the agent's
// working directory so skills/agents can discover project-scoped context.
//
// resource_ref is type-specific JSON; the daemon doesn't interpret it beyond
// well-known fields like url for github_repo. New types can be added without
// changing this struct.
type ProjectResourceData struct {
	ID           string          `json:"id"`
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        string          `json:"label,omitempty"`
}

// taskIssueStatusCap bounds the custom statuses a claim payload carries. A
// defensive ceiling, not a product limit: a real catalog holds a handful of
// entries, and the brief must not grow without bound on a workspace that
// scripted hundreds. Overflow is reported via IssueStatusesOmitted so the
// brief can disclose the truncation.
const taskIssueStatusCap = 30

// TaskIssueStatusData is one active CUSTOM workspace status on the claim wire
// (MUL-6460). Only the fields an agent needs to choose and write the status
// travel: key is the CLI argument, name is what users call it in instructions,
// category uses the legacy wire enum for installed daemons (presentation only,
// not inherited platform behavior), and description is the
// admin's "when to use me" guidance — the disambiguator when a category holds
// more than one status. Color/position/id stay off the wire: they carry no
// behavioral meaning for an agent, and the server already emits entries in
// catalog order (category rank, then position, then key).
type TaskIssueStatusData struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description,omitempty"`
}

// TaskCancellationActor is the point-in-time actor snapshot attached to a
// cancelled run. Type stays open for forward compatibility; current producers
// emit member, agent, or system.
type TaskCancellationActor struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type AgentTaskResponse struct {
	StartClaimSupported      bool                   `json:"start_claim_supported,omitempty"`
	CancelledByCommentChange bool                   `json:"cancelled_by_comment_change,omitempty"`
	CancelledBy              *TaskCancellationActor `json:"cancelled_by,omitempty"`

	ID              string `json:"id"`
	AgentID         string `json:"agent_id"`
	RuntimeID       string `json:"runtime_id"`
	IssueID         string `json:"issue_id"`
	WorkspaceID     string `json:"workspace_id"`
	WorkspaceSlug   string `json:"workspace_slug,omitempty"`
	IssueIdentifier string `json:"issue_identifier,omitempty"`
	// WorkspaceContext is the workspace-level system prompt set in workspace
	// settings (`workspace.context` DB column). Injected into the agent brief
	// as `## Workspace Context` so every agent running in this workspace —
	// regardless of issue or chat — sees the same
	// shared context. Empty when the workspace owner hasn't set it.
	WorkspaceContext string `json:"workspace_context,omitempty"`
	// IssueStatuses is the workspace's ACTIVE CUSTOM status catalog (MUL-6460),
	// injected into the agent brief so agents can see and use statuses beyond
	// the seven built-ins. Built-ins are omitted: their keys, names, and
	// semantics are locked (is_system rows cannot be renamed or archived), so
	// the daemon already knows them. Empty for a workspace with no custom
	// statuses — the daemon then renders the brief exactly as before, keeping
	// existing deployments byte-identical. Capped at taskIssueStatusCap
	// entries; IssueStatusesOmitted carries the overflow count.
	IssueStatuses []TaskIssueStatusData `json:"issue_statuses,omitempty"`
	// IssueStatusesOmitted is how many active custom statuses were dropped by
	// the cap, so the brief can say the list is incomplete instead of
	// presenting a truncated catalog as the whole one.
	IssueStatusesOmitted int                   `json:"issue_statuses_omitted,omitempty"`
	ThreadName           string                `json:"thread_name,omitempty"`  // semantic title for provider-native session/thread history
	MagpieGroup          string                `json:"magpie_group,omitempty"` // issue metadata magpie_group; the daemon honours it only for a group in the agent's runtime_config.magpie_groups
	Status               string                `json:"status"`
	Priority             int32                 `json:"priority"`
	DispatchedAt         *string               `json:"dispatched_at"`
	StartedAt            *string               `json:"started_at"`
	CompletedAt          *string               `json:"completed_at"`
	Result               any                   `json:"result"`
	Error                *string               `json:"error"`
	FailureReason        string                `json:"failure_reason,omitempty"` // see TaskService.MaybeRetryFailedTask
	Attempt              int32                 `json:"attempt"`
	MaxAttempts          int32                 `json:"max_attempts"`
	ParentTaskID         *string               `json:"parent_task_id,omitempty"`
	Agent                *TaskAgentData        `json:"agent,omitempty"`
	Repos                []RepoData            `json:"repos,omitempty"`
	ProjectID            string                `json:"project_id,omitempty"`          // issue's project, when present
	ProjectTitle         string                `json:"project_title,omitempty"`       // for surfacing in agent context
	ProjectDescription   string                `json:"project_description,omitempty"` // durable project-level context injected into the brief
	ProjectResources     []ProjectResourceData `json:"project_resources,omitempty"`   // resources attached to the project
	CreatedAt            string                `json:"created_at"`
	PriorSessionID       string                `json:"prior_session_id,omitempty"` // session ID from a previous task on same issue
	PriorWorkDir         string                `json:"prior_work_dir,omitempty"`   // work_dir from a previous task on same issue
	// PriorSessionResumeUnavailable is set when a more recent Codex session was
	// withheld because its rollout was missing (MUL-5305); PriorSessionID (if
	// any) is then an older fallback, and the daemon surfaces the continuity gap
	// in the brief even when that older session resumes cleanly. It is also set
	// when an automatic retry continues in its parent's workdir under a fresh
	// session (MUL-7034). omitempty keeps it off the wire for the common
	// (no-gap) case and for old daemons.
	PriorSessionResumeUnavailable bool   `json:"prior_session_resume_unavailable,omitempty"`
	WorkDir                       string `json:"work_dir,omitempty"` // local working directory pinned for this task; populated once the daemon reports it
	// RelativeWorkDir is a privacy-safe display form of WorkDir intended for
	// the UI. For standard tasks it strips the daemon's workspaces root while
	// preserving either the legacy or readable workspace/task segments; for local_directory
	// tasks the absolute path lives outside the envRoot layout, so we strip
	// recognised home-directory prefixes (`/Users/<name>/`, `/home/<name>/`,
	// `<drive>:/Users/<name>/`) and otherwise fall back to the basename so
	// the field never carries the user's home dir or account name. Empty
	// when WorkDir is empty, or when stripping leaves nothing. See
	// relativeWorkDir() for the full rules. Older clients can still read
	// WorkDir directly; newer UIs should prefer RelativeWorkDir.
	RelativeWorkDir string `json:"relative_work_dir,omitempty"`
	// DurableWorkDir is the daemon-confirmed directory that remains usable
	// after a disposable task worktree has been finalized and removed. It is a
	// point-in-time task snapshot and does not follow later resource edits.
	DurableWorkDir string `json:"durable_work_dir,omitempty"`
	// RelativeDurableWorkDir is the privacy-safe display form. The absolute
	// value is retained for explicit clipboard actions only.
	RelativeDurableWorkDir string `json:"relative_durable_work_dir,omitempty"`
	// BranchName is the git branch this run delivered its work on, set only by
	// older daemons' (since removed) worktree mode. Unlike WorkDir it is safe to show
	// verbatim: it is a ref inside the user's own repo, not a filesystem path.
	// Populated on both terminal paths — a failed run can still have committed
	// partial work, and that is when the pointer matters most.
	BranchName            string                 `json:"branch_name,omitempty"`
	TriggerCommentID      *string                `json:"trigger_comment_id,omitempty"`    // comment that triggered this task
	CoalescedCommentIDs   []string               `json:"coalesced_comment_ids,omitempty"` // MUL-4195: earlier comments folded into this run when it had not yet started, so a single run still covers every deliberate comment; trigger_comment_id is the newest. Surfaced so the UI can show which comments a run covered. omitempty so old clients ignore it
	CoalescedComments     []CoalescedCommentData `json:"coalesced_comments,omitempty"`    // MUL-4195: full detail (thread_id/author/created_at/content) of the folded comments, so the daemon prompt can address each without assuming they share the triggering thread. omitempty so old clients ignore it
	DeliveredCommentIDs   []string               `json:"delivered_comment_ids"`           // always present: [] is an authoritative empty receipt, while field absence identifies responses from legacy servers
	SupplementCapability  string                 `json:"supplement_capability,omitempty"`
	SupplementCommentIDs  []string               `json:"supplement_comment_ids,omitempty"`
	CanSupplement         bool                   `json:"can_supplement,omitempty"`
	TriggerThreadID       string                 `json:"trigger_thread_id,omitempty"`       // root comment ID for the triggering thread
	TriggerCommentContent string                 `json:"trigger_comment_content,omitempty"` // content of the triggering comment
	TriggerSummary        *string                `json:"trigger_summary,omitempty"`         // canonical short description snapshot — comment text / autopilot title — taken at task creation; survives source edits/deletes
	TriggerAuthorType     string                 `json:"trigger_author_type,omitempty"`     // "agent" or "member" — author kind of the triggering comment
	TriggerAuthorName     string                 `json:"trigger_author_name,omitempty"`     // display name of the triggering comment author
	NewCommentCount       int                    `json:"new_comment_count,omitempty"`       // ISSUE-WIDE comments since this agent's last run — every thread, not just the triggering one (CountNewCommentsSince); excludes the injected trigger and the agent's own comments; omitempty so old daemons ignore it
	NewCommentsSince      string                 `json:"new_comments_since,omitempty"`      // RFC3339 anchor (last run's started_at) the count is measured from; omitempty so old daemons ignore it. Suppressed with the count when the delta is zero — NewCommentsDeltaKnown, not this field, is what says the server looked
	// NewCommentsDeltaKnown reports that the issue-wide delta above was
	// actually COMPUTED this claim — both the anchor lookup and the count
	// query succeeded. Without it, NewCommentCount == 0 is ambiguous: a true
	// zero, a failed anchor read, a failed count read, a cold start with no
	// prior run, and an old server that never sends these fields all produce
	// the same zero. Only the first of those answers "has anything else been
	// said on this issue", so only the first may waive the workflow's comment
	// scan. Absent on old servers, which is the safe reading (MUL-6984).
	NewCommentsDeltaKnown bool `json:"new_comments_delta_known,omitempty"`
	// IssueStateDeltaKnown is the same contract as NewCommentsDeltaKnown, for
	// the ISSUE record rather than its comments: the server compared this
	// claim's title / description against the
	// snapshot taken when this agent last ran on this issue, and both the
	// lookup and the decode succeeded. Absent means NOT compared — a cold
	// start, no prior snapshot, a read error, a shape-version mismatch, or an
	// old server — and a daemon must then keep telling the agent to read the
	// issue. An empty IssueChangedFields is only "unchanged" alongside this
	// flag; on its own it is indistinguishable from "nobody looked" (MUL-7344).
	IssueStateDeltaKnown   bool                 `json:"issue_state_delta_known,omitempty"`
	IssueChangedFields     []string             `json:"issue_changed_fields,omitempty"`     // subset of title,description in that order; empty alongside IssueStateDeltaKnown means unchanged. Fields outside that set (status, assignee, priority, labels, parent, due date, stage, project, metadata) are NOT compared and must never be reported as checked. Status and assignee are out because IssueStatus / IssueAssigneeType / IssueAssigneeID ship their current values on every claim, so no comparison is needed to learn them; priority is out because it does not change what the agent does
	IssueStatus            string               `json:"issue_status,omitempty"`             // the issue's status key at claim time. Sent whether or not the delta is known: the agent needs it to decide workflow step 3 ("already in progress?") without a read
	IssueAssigneeType      string               `json:"issue_assignee_type,omitempty"`      // "agent", "member" or "squad" at claim time; empty when unassigned. With IssueAssigneeID, lets the agent tell "mine" from "someone else's" without a read
	IssueAssigneeID        string               `json:"issue_assignee_id,omitempty"`        // assignee UUID at claim time; empty when unassigned
	ChatSessionID          string               `json:"chat_session_id,omitempty"`          // non-empty for chat tasks
	ChatMessage            string               `json:"chat_message,omitempty"`             // user message for chat tasks
	ChatMessageAttachments []ChatAttachmentMeta `json:"chat_message_attachments,omitempty"` // attachments on the user message — agent calls `multica attachment download <id>` per entry
	ChatIntro              bool                 `json:"chat_intro,omitempty"`               // legacy compatibility for historical is_agent_intro sessions; new agent creation no longer creates these chats
	HandoffNote            string               `json:"handoff_note,omitempty"`             // legacy assignment handoff instruction retained for installed clients; rendered by the daemon only in the per-turn prompt
	// RequestingUserName + RequestingUserProfileDescription mirror the user
	// the agent is acting on behalf of (see daemon/types.go). v1 sources them
	// from the runtime owner so they're populated for daemon runtimes and
	// empty otherwise. The daemon emits both into the brief under
	// `## Requesting User`; the heading is skipped entirely when description
	// is empty.
	RequestingUserName               string `json:"requesting_user_name,omitempty"`
	RequestingUserProfileDescription string `json:"requesting_user_profile_description,omitempty"`
	// Initiator* are the existing daemon wire fields for the human whose
	// authority this run uses (originator_user_id). Despite their historical
	// names, they are not necessarily the direct trigger author; that actor is
	// represented by trigger_author_* for comment runs. Empty when no
	// originator exists. The daemon renders this per-turn as ## On Behalf Of;
	// credentials remain scoped to the runtime owner. See MUL-2645, GH-8674.
	InitiatorType  string `json:"initiator_type,omitempty"`  // "member" when an originator exists
	InitiatorID    string `json:"initiator_id,omitempty"`    // originator user UUID
	InitiatorName  string `json:"initiator_name,omitempty"`  // originator display name
	InitiatorEmail string `json:"initiator_email,omitempty"` // originator email
	Kind           string `json:"kind"`                      // source discriminator: "comment" | "chat" | "direct"
	// Attribution is the resolved accountable-human provenance for this run
	// (MUL-4302 §9): the source label + precise flag, the initiator (accountable)
	// and originator refs, the evidence pointer, and lineage. Always present (the
	// pure taskToResponse builds the labels + raw ids); initiator/originator names
	// are hydrated from the global user table only on user-facing surfaces.
	Attribution *TaskAttribution `json:"attribution,omitempty"`
	// Usage is this run's own token consumption, one entry per (provider, model)
	// it used — the same grain `task_usage` stores and the same grain the client
	// prices at. Hydrated on issue execution logs and explicit agent-history
	// accounting requests; normal UI history and daemon claims leave it nil so
	// those payloads do not carry accounting they do not use.
	//
	// nil and [] are both "no usage recorded" and the UI renders an em dash for
	// them — a run that predates usage reporting, or one that died before any
	// model call, genuinely has no number, and showing 0 would assert it was
	// free. omitempty keeps both off the wire.
	Usage []TaskUsageData `json:"usage,omitempty"`
	// AuthToken is the task-scoped `mat_` token the daemon must inject as
	// MULTICA_TOKEN in the agent process environment. The server binds it to
	// this (agent_id, task_id) pair at claim time and treats any request
	// authenticated with it as actor=agent, regardless of headers — so the
	// agent process cannot use it to read another agent's secrets via the
	// env-management endpoint. Claim fails closed when the runtime has no
	// owning user; the daemon must not fall back to its own credential. See
	// MUL-3292.
	AuthToken string `json:"auth_token,omitempty"`
}

// TaskAttribution is the wire shape of a run's accountable-human provenance
// (MUL-4302 §9). Source/Precise/Evidence/lineage come straight from the row (pure);
// Initiator/Originator carry the raw user id always and the display name/email/avatar
// only after hydration on a user-facing surface.
type TaskAttribution struct {
	// Source is the waterfall level that resolved the accountable human:
	// direct_human | delegation | comment_source | rule_owner | owner_fallback |
	// backfill | unattributed. Never blank (a pre-migration NULL renders "unattributed").
	Source string `json:"source"`
	// Precise is false for degraded sources (owner_fallback / backfill / unattributed);
	// the UI marks these distinctly and they count against the coverage metric.
	Precise bool `json:"precise"`
	// Initiator is the accountable human (accountable_user_id). Nil when unattributed.
	Initiator *AttributionUser `json:"initiator,omitempty"`
	// Originator is the authorization human (originator_user_id); nil for
	// degraded sources (legacy rule_owner / owner_fallback), where no human
	// authorized the run.
	Originator *AttributionUser `json:"originator,omitempty"`
	// Evidence points at the direct cause of the run so the UI can jump to it.
	Evidence            *TaskEvidence `json:"evidence,omitempty"`
	RuleVersionID       string        `json:"rule_version_id,omitempty"`
	DelegatedFromTaskID string        `json:"delegated_from_task_id,omitempty"`
	RetryOfTaskID       string        `json:"retry_of_task_id,omitempty"`
	RerunOfTaskID       string        `json:"rerun_of_task_id,omitempty"`
}

// AttributionUser is a departed-member-safe user ref, resolved from the global user
// table. Name/Email/AvatarURL are empty until hydrated.
type AttributionUser struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

// TaskEvidence is the kind-tagged handle to a run's direct cause (a comment,
// source task, legacy autopilot run or rule version, ...).
type TaskEvidence struct {
	Kind  string `json:"kind"`
	RefID string `json:"ref_id"`
}

// taskAttributionBase builds the pure (no-DB) part of a run's attribution from the
// row: the source label + precise flag, evidence, lineage, and the raw initiator /
// originator user ids. Names are filled later by hydrateTaskAttributions.
func taskAttributionBase(t db.AgentTaskQueue) *TaskAttribution {
	src := attribution.Source(t.OriginatorSource.String)
	attr := &TaskAttribution{
		Source:              src.String(), // empty (pre-migration) → "unattributed"
		Precise:             src.Precise(),
		RuleVersionID:       uuidToString(t.RuleVersionID),
		DelegatedFromTaskID: uuidToString(t.DelegatedFromTaskID),
		RetryOfTaskID:       uuidToString(t.RetryOfTaskID),
		RerunOfTaskID:       uuidToString(t.RerunOfTaskID),
	}
	if t.AccountableUserID.Valid {
		attr.Initiator = &AttributionUser{ID: uuidToString(t.AccountableUserID)}
	}
	if t.OriginatorUserID.Valid {
		attr.Originator = &AttributionUser{ID: uuidToString(t.OriginatorUserID)}
	}
	if t.TriggerEvidenceKind.Valid && t.TriggerEvidenceKind.String != "" {
		attr.Evidence = &TaskEvidence{Kind: t.TriggerEvidenceKind.String, RefID: uuidToString(t.TriggerEvidenceRefID)}
	}
	return attr
}

// hydrateTaskAttributions fills the display name / email / avatar on the initiator
// and originator refs of the given attributions, resolving from the GLOBAL user
// table in one batch (departed-safe, N+1-free). Best-effort: on a lookup failure the
// raw user ids already present are left as-is — names are display sugar, not gating.
func (h *Handler) hydrateTaskAttributions(ctx context.Context, attrs []*TaskAttribution) {
	seen := make(map[string]struct{})
	var ids []pgtype.UUID
	add := func(ref *AttributionUser) {
		if ref == nil || ref.ID == "" {
			return
		}
		if _, ok := seen[ref.ID]; ok {
			return
		}
		if u, err := util.ParseUUID(ref.ID); err == nil {
			seen[ref.ID] = struct{}{}
			ids = append(ids, u)
		}
	}
	for _, a := range attrs {
		if a == nil {
			continue
		}
		add(a.Initiator)
		add(a.Originator)
	}
	if len(ids) == 0 {
		return
	}
	users, err := h.Queries.GetUsersByIDs(ctx, ids)
	if err != nil {
		return
	}
	byID := make(map[string]db.GetUsersByIDsRow, len(users))
	for _, u := range users {
		byID[uuidToString(u.ID)] = u
	}
	fill := func(ref *AttributionUser) {
		if ref == nil {
			return
		}
		if u, ok := byID[ref.ID]; ok {
			ref.Name = u.Name
			ref.Email = u.Email
			if u.AvatarUrl.Valid {
				ref.AvatarURL = h.resolveAvatarURL(u.AvatarUrl.String)
			}
		}
	}
	for _, a := range attrs {
		if a == nil {
			continue
		}
		fill(a.Initiator)
		fill(a.Originator)
	}
}

// attributionsOf collects the (non-nil) TaskAttribution pointers from a slice of
// task responses so a list endpoint can hydrate them all in one batch.
func attributionsOf(resps []AgentTaskResponse) []*TaskAttribution {
	out := make([]*TaskAttribution, 0, len(resps))
	for i := range resps {
		if resps[i].Attribution != nil {
			out = append(out, resps[i].Attribution)
		}
	}
	return out
}

// ChatAttachmentMeta is the structured attachment metadata embedded in
// claim responses for chat tasks. The agent uses these to run
// `multica attachment download <id>` rather than guessing from the
// markdown URL (which is signed and 30-min expiring on private CDN).
// The mirror struct on the daemon side lives in internal/daemon/types.go
// and uses the same JSON field names.
type ChatAttachmentMeta struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
}

// CoalescedCommentData carries the full detail of a comment that was folded
// into a not-yet-started run (MUL-4195) so the daemon can embed it directly in
// the prompt. The earlier merge path only shipped comment IDs plus a
// "they are in the triggering thread" hint, which is WRONG when the folded
// comments span multiple threads (an issue's assignee can be triggered from
// different threads). Shipping thread_id / author / created_at / content lets
// the prompt address each folded comment without assuming a single thread or
// relying on a `--recent N` window that may not cover them all. The mirror
// struct on the daemon side lives in internal/daemon/types.go with the same
// JSON field names.
type CoalescedCommentData struct {
	ID         string `json:"id"`
	ThreadID   string `json:"thread_id,omitempty"`
	AuthorType string `json:"author_type,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	Content    string `json:"content"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// TaskUsageData is one (provider, model) slice of a single run's token usage.
// Field names match the runtime/dashboard usage rows exactly so the client can
// feed it to the same `estimateCost` / `estimateCacheSavings` helpers without
// an adapter.
//
// CostUsdTicks is the provider's own price for these tokens (1e-10 USD) and is
// nil when the provider reported none — the client then estimates that slice
// from its rate table. A pointer, not a zero value: 0 ticks is a real answer
// ("the provider says this was free") and must stay distinguishable from
// "the provider said nothing".
type TaskUsageData struct {
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	CostUsdTicks     *int64 `json:"cost_usd_ticks,omitempty"`
}

// TaskAgentData holds agent info included in claim responses so the daemon
// can set up the execution environment (branch naming, instructions).
type TaskAgentData struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Instructions string            `json:"instructions"`
	CustomEnv    map[string]string `json:"custom_env,omitempty"`
	CustomArgs   []string          `json:"custom_args,omitempty"`
	// RuntimeConfig is the agent's saved runtime_config JSON as-is. The
	// daemon decodes it per-provider — e.g. the openclaw backend reads
	// `mode` + `gateway.*` to choose between embedded and gateway routing
	// (issue #3260). Other providers ignore the payload entirely. Sent
	// raw so the daemon can evolve its schema without a server roundtrip.
	RuntimeConfig json.RawMessage `json:"runtime_config,omitempty"`
}

// visibleTaskHistory omits unused assignee fallbacks created by older versions.
// Dispatch only begins preparation, so a fallback cancelled before StartTask
// is still unused. Keep started fallbacks and ordinary cancellations visible,
// and retain the underlying scheduling records for audit.
// Keep this predicate in sync with ListAgentTasks in pkg/db/queries/agent.sql.
func visibleTaskHistory(tasks []db.AgentTaskQueue) []db.AgentTaskQueue {
	return slices.DeleteFunc(tasks, func(task db.AgentTaskQueue) bool {
		return task.EscalationForTaskID.Valid &&
			!task.StartedAt.Valid &&
			(task.Status == "deferred" || task.Status == "cancelled")
	})
}

// taskToResponse maps a queue row to its wire shape. workspaceID is threaded
// in because the row itself doesn't carry one (workspace lives on the agent
// / issue / chat session) — we ask the caller to resolve it once and pass it
// down. It populates WorkspaceID and powers the privacy-safe RelativeWorkDir
// derivation; pass "" only on daemon-facing paths that genuinely don't have
// it, in which case RelativeWorkDir falls back to the existing WorkDir.
func taskToResponse(t db.AgentTaskQueue, workspaceID string) AgentTaskResponse {
	var cancellation struct {
		TaskID string `json:"comment_change_cancelled_task_id"`
	}
	_ = json.Unmarshal(t.Context, &cancellation)
	var result any
	if t.Result != nil {
		json.Unmarshal(t.Result, &result)
	}
	failureReason := ""
	if t.FailureReason.Valid {
		failureReason = t.FailureReason.String
	}
	workDir := ""
	if t.WorkDir.Valid {
		workDir = t.WorkDir.String
	}
	durableWorkDir := ""
	if t.DurableWorkDir.Valid {
		durableWorkDir = t.DurableWorkDir.String
	}
	branchName := ""
	if t.BranchName.Valid {
		branchName = t.BranchName.String
	}
	handoffNote := ""
	if t.HandoffNote.Valid {
		handoffNote = t.HandoffNote.String
	}
	return AgentTaskResponse{
		// Task-scoped provenance must not transfer through copied retry context.
		CancelledByCommentChange: t.Status == "cancelled" && cancellation.TaskID != "" && cancellation.TaskID == uuidToString(t.ID),
		CancelledBy:              taskCancellationActorToResponse(t),

		ID:                     uuidToString(t.ID),
		AgentID:                uuidToString(t.AgentID),
		RuntimeID:              uuidToString(t.RuntimeID),
		IssueID:                uuidToString(t.IssueID),
		WorkspaceID:            workspaceID,
		Status:                 t.Status,
		Priority:               t.Priority,
		DispatchedAt:           timestampToPtr(t.DispatchedAt),
		StartedAt:              timestampToPtr(t.StartedAt),
		CompletedAt:            timestampToPtr(t.CompletedAt),
		Result:                 result,
		Error:                  textToPtr(t.Error),
		FailureReason:          failureReason,
		BranchName:             branchName,
		Attempt:                t.Attempt,
		MaxAttempts:            t.MaxAttempts,
		ParentTaskID:           uuidToPtr(t.ParentTaskID),
		CreatedAt:              timestampToString(t.CreatedAt),
		TriggerCommentID:       uuidToPtr(t.TriggerCommentID),
		CoalescedCommentIDs:    uuidsToStrings(t.CoalescedCommentIds),
		DeliveredCommentIDs:    uuidStringsOrEmpty(t.DeliveredCommentIds),
		TriggerSummary:         textToPtr(t.TriggerSummary),
		HandoffNote:            handoffNote,
		WorkDir:                workDir,
		RelativeWorkDir:        relativeWorkDir(workDir, workspaceID, uuidToString(t.ID)),
		DurableWorkDir:         durableWorkDir,
		RelativeDurableWorkDir: relativeWorkDir(durableWorkDir, "", ""),
		ChatSessionID:          uuidToString(t.ChatSessionID),
		Kind:                   computeTaskKind(t),
		// Attribution labels + evidence + lineage + raw user ids (pure). Names are
		// hydrated separately on user-facing surfaces (MUL-4302 §9).
		Attribution: taskAttributionBase(t),
	}
}

func taskCancellationActorToResponse(t db.AgentTaskQueue) *TaskCancellationActor {
	if t.Status != "cancelled" || !t.CancelledByType.Valid || t.CancelledByType.String == "" {
		return nil
	}
	actor := &TaskCancellationActor{
		Type: t.CancelledByType.String,
		ID:   uuidToString(t.CancelledByID),
	}
	if t.CancelledByName.Valid {
		actor.Name = t.CancelledByName.String
	}
	return actor
}

// relativeWorkDir produces a privacy-safe display form of the daemon-reported
// absolute work_dir. The contract: the returned string must never contain
// the user's home directory prefix or their account name. The chip is
// rendered in transcripts that frequently end up in screen shares,
// screenshots, and recordings, so this function is the only guard.
//
//   - For standard tasks, it validates the adjacent workspace/task segments
//     by their stable ID suffixes, then strips everything before them. This
//     accepts both legacy `<wsUUID>/<taskShort>` roots and readable
//     `<workspaceSlug>-<wsShort>/<issueKey>-<taskShort>` roots without treating
//     the labels as identity.
//   - For local_directory tasks the absolute path lives outside the envRoot
//     layout. We try to recognise common home-directory prefixes
//     (`/Users/<name>/`, `/home/<name>/`, `<drive>:/Users/<name>/`) and strip
//     them, returning the remainder (e.g. `repos/foo`). When the prefix
//     can't be recognised — unusual home layouts, network mounts, paths
//     under `/opt`, `/srv`, etc. — we fall back to the basename so we never
//     accidentally render a path component that happens to be a username.
//
// Returns empty when work_dir is empty, or when stripping leaves nothing
// (i.e. work_dir was exactly the user's home — rendering nothing is
// preferable to a chip that says `<name>`). taskDirSegment() must stay in
// lock-step with server/internal/daemon/execenv/git.go:taskKey — both
// consume the same task UUID. legacyTaskDirSegment keeps privacy-safe display
// working for pre-#7347 roots and roots created by an earlier build of this PR.
func relativeWorkDir(workDir, workspaceID, taskID string) string {
	if workDir == "" {
		return ""
	}
	// Normalize Windows separators so the rest of the function only
	// reasons about forward slashes.
	normalized := strings.ReplaceAll(workDir, "\\", "/")

	if workspaceID != "" && taskID != "" {
		parts := strings.Split(normalized, "/")
		for i := 0; i+1 < len(parts); i++ {
			if matchesWorkspacePathSegment(parts[i], workspaceID) &&
				matchesTaskPathSegment(parts[i+1], taskID) {
				return strings.Join(parts[i:], "/")
			}
		}
	}

	if stripped, ok := stripHomePrefix(normalized); ok {
		return stripped
	}

	return basename(normalized)
}

func matchesWorkspacePathSegment(segment, workspaceID string) bool {
	lower := strings.ToLower(segment)
	legacy := legacyTaskDirSegment(workspaceID)
	current := strings.ToLower(taskDirSegment(workspaceID))
	return strings.EqualFold(segment, workspaceID) ||
		strings.HasSuffix(lower, "-"+legacy) || strings.HasSuffix(lower, "-"+current)
}

func matchesTaskPathSegment(segment, taskID string) bool {
	lower := strings.ToLower(segment)
	legacy := legacyTaskDirSegment(taskID)
	current := strings.ToLower(taskDirSegment(taskID))
	return strings.EqualFold(segment, legacy) || strings.EqualFold(segment, current) ||
		strings.HasSuffix(lower, "-"+legacy) || strings.HasSuffix(lower, "-"+current)
}

// taskDirSegmentLen and taskDirSegment mirror execenv.taskKeyLen /
// execenv.taskKey — the LAST 12 hex chars of the task id. Kept inline here so
// the agent handler has zero imports from the daemon package (which would
// create an unwanted cycle between handler and daemon).
//
// It must keep taking the TAIL. A UUIDv7's leading hex chars are timestamp
// bits shared by every task created within ~65.5s (#7326); the daemon stopped
// reading from that end, and this reconstruction only matches while both sides
// agree.
const taskDirSegmentLen = 12

func taskDirSegment(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > taskDirSegmentLen {
		return s[len(s)-taskDirSegmentLen:]
	}
	return s
}

// legacyTaskDirSegment mirrors the historical shortID layout: the first eight
// dash-free characters. It is display compatibility only, never new identity.
func legacyTaskDirSegment(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > 8 {
		return strings.ToLower(s[:8])
	}
	return strings.ToLower(s)
}

// homeDirPattern matches the well-known per-user home layouts on macOS,
// Linux, and Windows after backslash normalization:
//
//	/Users/<name>[/<rest>]
//	/home/<name>[/<rest>]
//	<drive>:/Users/<name>[/<rest>]
//
// Case-insensitive because macOS and Windows are case-insensitive at the
// filesystem layer; matching `/users/...` the same as `/Users/...` keeps
// the strip robust against unusual casings seen on shared drives.
// Capture group 1 is the optional remainder after the username segment.
var homeDirPattern = regexp.MustCompile(`(?i)^(?:[A-Za-z]:)?/(?:Users|home)/[^/]+(?:/(.*))?$`)

// stripHomePrefix recognises common home-directory layouts and returns
// the path remainder after the username segment. Returns (remainder, true)
// when a known home prefix matched. The remainder may be the empty string
// (work_dir was exactly the home directory) — the caller treats that as
// "nothing safe to display".
func stripHomePrefix(p string) (string, bool) {
	m := homeDirPattern.FindStringSubmatch(p)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// basename returns the last non-empty segment of a forward-slash path.
// Used as the ultimate privacy-safe fallback when we can't otherwise
// recognise the path: a single segment can never expose the home prefix,
// and the leaf is almost always the most useful piece of context anyway
// (typically the repo directory name for local_directory tasks).
func basename(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

// computeTaskKind picks the stable source-discriminator string task UIs use.
// Chat has a dedicated FK; issue tasks split into comment-triggered and
// direct runs.
func computeTaskKind(t db.AgentTaskQueue) string {
	if uuidToString(t.ChatSessionID) != "" {
		return "chat"
	}
	if uuidToString(t.TriggerCommentID) != "" {
		return "comment"
	}
	return "direct"
}

// loadAgentRuntimeAvailability returns only a coarse liveness bucket for
// agent presence. Runtime rows are loaded internally even when the caller is
// not allowed to list or inspect the private runtime; no runtime fields are
// copied onto an agent response. The existing runtime-list visibility contract
// is mirrored so the bucket is only copied when the full row is hidden.
func (h *Handler) loadAgentRuntimeAvailability(ctx context.Context, agents []db.Agent, workspaceID, viewerID, viewerRole string, now time.Time) (map[string]string, error) {
	// Owner/admin runtime lists already contain every row, so their normal
	// client-side derivation is authoritative and no projection query is needed.
	if viewerRole == "owner" {
		return map[string]string{}, nil
	}

	runtimeIDs := make([]pgtype.UUID, 0, len(agents))
	for _, agent := range agents {
		// Archived presence always resolves to "archived", so its runtime state
		// is neither user-visible nor a reason for clients to keep polling.
		if !agent.ArchivedAt.Valid && agent.RuntimeID.Valid {
			runtimeIDs = append(runtimeIDs, agent.RuntimeID)
		}
	}
	result := make(map[string]string, len(runtimeIDs))
	if len(runtimeIDs) == 0 {
		return result, nil
	}

	// Read directly rather than through RuntimeLookup: this resolves rows for a
	// list of agents instead of resolving a runtime a caller asked for, so it
	// has no honest source label on multica_agent_runtime_lookup_total yet. See
	// the exception noted on service.RuntimeLookup (MUL-6884).
	runtimes, err := h.Queries.GetAgentRuntimes(ctx, runtimeIDs)
	if err != nil {
		return nil, err
	}
	for _, runtime := range runtimes {
		// Agent/runtime workspace consistency is normally enforced at bind time;
		// keep the projection fail-closed if a legacy row violates it.
		if uuidToString(runtime.WorkspaceID) != workspaceID {
			continue
		}
		// ListAgentRuntimes exposes every row to workspace owner and only
		// owner/public rows to regular members. Keep the coarse bridge for the
		// rows that the viewer's runtime list cannot carry.
		if runtime.Visibility == "public" ||
			(runtime.OwnerID.Valid && uuidToString(runtime.OwnerID) == viewerID) {
			continue
		}
		result[uuidToString(runtime.ID)] = deriveAgentRuntimeAvailability(runtime, now)
	}
	return result, nil
}

// deriveAgentRuntimeAvailability owns the three-state runtime reachability
// policy: online, unstable (offline but seen within five minutes) or offline.
func deriveAgentRuntimeAvailability(runtime db.AgentRuntime, now time.Time) string {
	if runtime.Status == "online" {
		return "online"
	}
	if runtime.Status != "" && runtime.LastSeenAt.Valid && now.Sub(runtime.LastSeenAt.Time) < 5*time.Minute {
		return "unstable"
	}
	return "offline"
}

func (h *Handler) ListAgents(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	userID := requestUserID(r)

	var agents []db.Agent
	var err error
	if r.URL.Query().Get("include_archived") == "true" {
		agents, err = h.Queries.ListAllAgents(r.Context(), parseUUID(workspaceID))
	} else {
		agents, err = h.Queries.ListAgents(r.Context(), parseUUID(workspaceID))
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agents")
		return
	}
	runtimeAvailabilityByID, err := h.loadAgentRuntimeAvailability(r.Context(), agents, workspaceID, userID, member.Role, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent runtime availability")
		return
	}

	// Resolve the request actor once. Agents bypass the view gate to preserve
	// A2A collaboration; members see a private agent only when they own it or
	// are workspace owner, and a public_to agent only when on its
	// invocation allow-list. Targets are batch-loaded to avoid an N+1 and
	// reused to enrich each response's invocation_targets.
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	targetsByAgent, ok := h.loadInvocationTargetsByAgent(r.Context(), agents)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to load agent invocation targets")
		return
	}
	visible := make([]AgentResponse, 0, len(agents))
	for _, a := range agents {
		targets := targetsByAgent[uuidToString(a.ID)]
		if actorType == "member" {
			if !memberAllowedToViewAgent(a, targets, actorID, member.Role) {
				continue
			}
		}
		resp := h.agentToResponse(a)
		// The map is keyed by runtime, and active + archived agents may share one.
		// Keep the archived guard here as well as in the loader so an active sibling
		// cannot leak its projection onto an archived response.
		if availability, ok := runtimeAvailabilityByID[resp.RuntimeID]; ok && !a.ArchivedAt.Valid {
			resp.RuntimeAvailability = availability
		}
		applyInvocationTargetsToResponse(&resp, targets)
		visible = append(visible, resp)
	}

	writeJSON(w, http.StatusOK, visible)
}

func (h *Handler) GetAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	// Private-agent gate: members must be in allowed_principals to view
	// (and therefore navigate to) a private agent. The 403 lets the front-end
	// render an explicit "no access" placeholder instead of a 404 — see
	// agent-detail-page.tsx.
	workspaceID := uuidToString(agent.WorkspaceID)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}
	resp := h.agentToResponse(agent)
	viewerRole := ""
	if member, ok := ctxMember(r.Context()); ok {
		viewerRole = member.Role
	}
	runtimeAvailability, err := h.loadAgentRuntimeAvailability(r.Context(), []db.Agent{agent}, workspaceID, userID, viewerRole, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent runtime availability")
		return
	}
	if availability, ok := runtimeAvailability[resp.RuntimeID]; ok {
		resp.RuntimeAvailability = availability
	}
	if !h.enrichAgentResponseWithTargetsHTTP(w, r, &resp, agent.ID) {
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type CreateAgentRequest struct {
	Name                 string                     `json:"name"`
	Description          string                     `json:"description"`
	Instructions         string                     `json:"instructions"`
	ConversationStarters []AgentConversationStarter `json:"conversation_starters"`
	AvatarURL            *string                    `json:"avatar_url"`
	RuntimeID            string                     `json:"runtime_id"`
	RuntimeConfig        any                        `json:"runtime_config"`
	CustomEnv            map[string]string          `json:"custom_env"`
	CustomArgs           []string                   `json:"custom_args"`
	Visibility           string                     `json:"visibility"`
	// PermissionMode + InvocationTargets are the new invocation-permission
	// inputs (MUL-3963). When permission_mode is present it is authoritative
	// and Visibility is ignored; when absent, legacy Visibility is mapped
	// (private -> private, workspace -> public_to+workspace target). On create
	// only the caller can be the owner, so targets are accepted unconditionally.
	PermissionMode     *string                    `json:"permission_mode"`
	InvocationTargets  []AgentInvocationTargetDTO `json:"invocation_targets"`
	MaxConcurrentTasks int32                      `json:"max_concurrent_tasks"`
	// Template records the creation-source attribution used by the
	// `agent_created` analytics event. Empty identifies a manually authored
	// agent.
	Template string `json:"template"`
}

func decodeJSONBodyWithRawFields(body io.Reader, dst any) (map[string]json.RawMessage, error) {
	payload, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(payload, dst); err != nil {
		return nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}

	return raw, nil
}

func normaliseAgentConversationStarters(starters []AgentConversationStarter) ([]AgentConversationStarter, error) {
	if len(starters) > maxAgentConversationStarters {
		return nil, fmt.Errorf("conversation_starters must contain at most %d items", maxAgentConversationStarters)
	}

	normalised := make([]AgentConversationStarter, 0, len(starters))
	for i, item := range starters {
		item.Label = strings.TrimSpace(item.Label)
		item.Prompt = strings.TrimSpace(item.Prompt)
		if item.Label == "" {
			return nil, fmt.Errorf("conversation_starters[%d].label is required", i)
		}
		if item.Prompt == "" {
			return nil, fmt.Errorf("conversation_starters[%d].prompt is required", i)
		}
		if utf8.RuneCountInString(item.Label) > maxAgentConversationStarterLabel {
			return nil, fmt.Errorf("conversation_starters[%d].label must be %d characters or fewer", i, maxAgentConversationStarterLabel)
		}
		if utf8.RuneCountInString(item.Prompt) > maxAgentConversationStarterLength {
			return nil, fmt.Errorf("conversation_starters[%d].prompt must be %d characters or fewer", i, maxAgentConversationStarterLength)
		}
		normalised = append(normalised, item)
	}
	return normalised, nil
}

func (h *Handler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	var req CreateAgentRequest
	rawFields, err := decodeJSONBodyWithRawFields(r.Body, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ownerID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if utf8.RuneCountInString(req.Description) > maxAgentDescriptionLength {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("description must be %d characters or fewer", maxAgentDescriptionLength))
		return
	}
	if req.RuntimeID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}
	conversationStarters, err := normaliseAgentConversationStarters(req.ConversationStarters)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Visibility == "" {
		req.Visibility = "private"
	}
	if err := defaultAndValidateAgentMaxConcurrentTasks(rawFields, &req.MaxConcurrentTasks); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	runtimeUUID, ok := parseUUIDOrBadRequest(w, req.RuntimeID, "runtime_id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	// Resolve invocation permission (MUL-3963). permission_mode is
	// authoritative when present; otherwise the legacy visibility value is
	// mapped. On create the caller is always the owner, so targets are
	// accepted unconditionally.
	_, hasTargets := rawFields["invocation_targets"]
	legacyVis := req.Visibility
	perm, _, permErr := parsePermissionInput(wsUUID, req.PermissionMode, req.InvocationTargets, req.PermissionMode != nil, hasTargets, &legacyVis)
	if permErr != nil {
		writeError(w, http.StatusBadRequest, permErr.Error())
		return
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
		ID:          runtimeUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid runtime_id")
		return
	}

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	if !canUseRuntimeForAgent(member, runtime) {
		writeError(w, http.StatusForbidden, "this runtime is private; only its owner can create agents on it")
		return
	}

	// Probe workspace agent count BEFORE the insert so the funnel has a
	// clean "first agent ever in this workspace" signal. A non-fatal read: if the
	// list fails we fall through with isFirstAgent=false rather than
	// blocking creation, since the primary DB operation is the insert.
	isFirstAgent := false
	if existing, listErr := h.Queries.ListAgents(r.Context(), wsUUID); listErr == nil {
		isFirstAgent = len(existing) == 0
	}

	// A create has no prior token to restore, so if the caller submitted the
	// public mask sentinel as gateway.token (e.g. replayed a masked GET body)
	// drop it rather than persisting a literal "***" as a real bearer token.
	preserveMaskedGatewayToken(req.RuntimeConfig, nil)
	rc, _ := json.Marshal(req.RuntimeConfig)
	if req.RuntimeConfig == nil {
		rc = []byte("{}")
	}

	ce, _ := json.Marshal(req.CustomEnv)
	if req.CustomEnv == nil {
		ce = []byte("{}")
	}

	ca, _ := json.Marshal(req.CustomArgs)
	if req.CustomArgs == nil {
		ca = []byte("[]")
	}

	sp, _ := json.Marshal(conversationStarters)

	avatarURL, ok := h.newAgentAvatar(w, r, req.AvatarURL)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent create transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	created, err := qtx.CreateAgent(r.Context(), db.CreateAgentParams{
		WorkspaceID:          wsUUID,
		Name:                 req.Name,
		Description:          req.Description,
		Instructions:         req.Instructions,
		AvatarUrl:            avatarURL,
		RuntimeMode:          runtime.RuntimeMode,
		RuntimeConfig:        rc,
		RuntimeID:            runtime.ID,
		Visibility:           perm.legacyVisibility(),
		PermissionMode:       perm.mode,
		MaxConcurrentTasks:   req.MaxConcurrentTasks,
		OwnerID:              parseUUID(ownerID),
		CustomEnv:            ce,
		CustomArgs:           ca,
		ConversationStarters: sp,
	})
	if err != nil {
		// Unique constraint on (workspace_id, name) — return a clear conflict error
		// so the UI can show the right message instead of a generic 500.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "agent_workspace_name_unique" {
			writeError(w, http.StatusConflict, fmt.Sprintf("an agent named %q already exists in this workspace", req.Name))
			return
		}
		slog.Warn("create agent failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to create agent")
		return
	}
	if err := replaceInvocationTargetsWithQueries(r.Context(), qtx, created.ID, parseUUID(ownerID), perm.targets); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent access")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent create")
		return
	}
	slog.Info("agent created", append(logger.RequestAttrs(r), "agent_id", uuidToString(created.ID), "name", created.Name, "workspace_id", workspaceID)...)

	if runtime.Status == "online" {
		h.TaskService.ReconcileAgentStatus(r.Context(), created.ID)
		created, _ = h.Queries.GetAgent(r.Context(), created.ID)
	}

	resp := h.agentToResponse(created)
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, created.ID); err != nil {
		slog.Warn("create agent: load invocation targets for response failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(created.ID))...)
	}
	actorType, actorID := h.resolveActor(r, ownerID, workspaceID)
	h.publish(protocol.EventAgentCreated, workspaceID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})

	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.AgentCreated(
		ownerID,
		workspaceID,
		uuidToString(created.ID),
		runtime.Provider,
		runtime.RuntimeMode,
		req.Template,
		isFirstAgent,
	))

	writeJSON(w, http.StatusCreated, resp)
}

type UpdateAgentRequest struct {
	Name                 *string                     `json:"name"`
	Description          *string                     `json:"description"`
	Instructions         *string                     `json:"instructions"`
	ConversationStarters *[]AgentConversationStarter `json:"conversation_starters"`
	AvatarURL            *string                     `json:"avatar_url"`
	RuntimeID            *string                     `json:"runtime_id"`
	RuntimeConfig        any                         `json:"runtime_config"`
	// custom_env is intentionally NOT updatable through this endpoint.
	// Use `PUT /api/agents/{id}/env` for env changes — that path admits
	// the agent owner or a workspace owner, denies agent actors,
	// and writes a persisted audit log entry. A `PUT /api/agents/{id}`
	// body that carries
	// `custom_env` is rejected with 400 in the handler below so a
	// caller never believes they rotated a secret when the value is
	// actually unchanged, and so a client that round-tripped a
	// previously-returned masked map cannot silently overwrite real
	// secret values with literal `****`. See MUL-2600.
	CustomArgs *[]string `json:"custom_args"`
	Visibility *string   `json:"visibility"`
	// PermissionMode + InvocationTargets are the invocation-permission inputs
	// (MUL-3963). Owner-only writes: a
	// non-owner admin passing them is silently ignored, because the invoke
	// gate is owner/allow-list based and an admin-authored allow-list would
	// confuse the owner about who can run their agent. permission_mode is
	// authoritative when present; otherwise legacy visibility is mapped.
	PermissionMode     *string                     `json:"permission_mode"`
	InvocationTargets  *[]AgentInvocationTargetDTO `json:"invocation_targets"`
	Status             *string                     `json:"status"`
	MaxConcurrentTasks *int32                      `json:"max_concurrent_tasks"`
}

// broadcastAgentResponse prepares an AgentResponse for the WebSocket bus.
// Subscribers include agent processes authenticated with their own task
// tokens, so secret-bearing fields must never ride along.
func broadcastAgentResponse(resp AgentResponse) AgentResponse {
	out := resp
	// Belt-and-suspenders: agentToResponse already masks gateway.token on
	// every read, so by the time a response reaches this broadcast helper
	// the field is already "***". Re-mask anyway so a future refactor that
	// bypasses agentToResponse (e.g. constructing AgentResponse from raw
	// db.Agent in a new handler) cannot silently leak the token to every
	// WebSocket subscriber on the workspace, agent processes included.
	maskGatewayToken(out.RuntimeConfig)
	return out
}

// canManageAgent checks whether the current user can update or archive an agent.
// Only the agent owner or workspace owner can manage any agent,
// regardless of whether it is public or private.
func (h *Handler) canManageAgent(w http.ResponseWriter, r *http.Request, agent db.Agent) bool {
	wsID := uuidToString(agent.WorkspaceID)
	member, ok := h.requireWorkspaceRole(w, r, wsID, "agent not found", "owner", "member")
	if !ok {
		return false
	}
	isAdmin := member.Role == "owner"
	isAgentOwner := uuidToString(agent.OwnerID) == requestUserID(r)
	if !isAdmin && !isAgentOwner {
		writeError(w, http.StatusForbidden, "only the agent owner can manage this agent")
		return false
	}
	return true
}

func (h *Handler) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, existing) {
		return
	}

	var req UpdateAgentRequest
	rawFields, err := decodeJSONBodyWithRawFields(r.Body, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Hard-reject any attempt to write custom_env through the generic
	// update endpoint. Silently dropping the field (which is what an
	// `omitempty` field would do) was the pre-PR behaviour and led to
	// users believing they had rotated a secret when the value was
	// actually unchanged. env values move only through `PUT
	// /api/agents/{id}/env` — that endpoint admits the agent owner or a
	// workspace owner, denies agent actors, and writes a queryable
	// audit row.
	if _, ok := rawFields["custom_env"]; ok {
		writeError(w, http.StatusBadRequest, "custom_env is no longer accepted on this endpoint; use PUT /api/agents/{id}/env (or `multica agent env set`)")
		return
	}

	params := db.UpdateAgentParams{
		ID: existing.ID,
	}
	if req.Name != nil {
		params.Name = pgtype.Text{String: *req.Name, Valid: true}
	}
	if req.Description != nil {
		if utf8.RuneCountInString(*req.Description) > maxAgentDescriptionLength {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("description must be %d characters or fewer", maxAgentDescriptionLength))
			return
		}
		params.Description = pgtype.Text{String: *req.Description, Valid: true}
	}
	if req.Instructions != nil {
		params.Instructions = pgtype.Text{String: *req.Instructions, Valid: true}
	}
	if req.ConversationStarters != nil {
		conversationStarters, err := normaliseAgentConversationStarters(*req.ConversationStarters)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		encoded, _ := json.Marshal(conversationStarters)
		params.ConversationStarters = encoded
	}
	if req.AvatarURL != nil {
		avatarURL, ok := h.acceptAvatarURL(w, r, *req.AvatarURL, existing.AvatarUrl.String)
		if !ok {
			return
		}
		params.AvatarUrl = pgtype.Text{String: avatarURL, Valid: true}
	}
	if req.RuntimeConfig != nil {
		// Restore the persisted gateway token when the request submitted the
		// public mask sentinel. Without this, a UI that GETs the agent and
		// PATCHes the same payload back round-trips "***" into the database
		// and silently destroys the real secret (issue #3260).
		preserveMaskedGatewayToken(req.RuntimeConfig, existing.RuntimeConfig)
		rc, _ := json.Marshal(req.RuntimeConfig)
		params.RuntimeConfig = rc
	}
	if req.CustomArgs != nil {
		ca, _ := json.Marshal(*req.CustomArgs)
		params.CustomArgs = ca
	}

	if req.RuntimeID != nil {
		runtimeUUID, ok := parseUUIDOrBadRequest(w, *req.RuntimeID, "runtime_id")
		if !ok {
			return
		}
		runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
			ID:          runtimeUUID,
			WorkspaceID: existing.WorkspaceID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid runtime_id")
			return
		}
		// Same gate as CreateAgent — prevents UpdateAgent from being used to
		// re-bind an agent onto someone else's private runtime, which would
		// otherwise be a quiet end-run around the CreateAgent check.
		member, ok := h.workspaceMember(w, r, uuidToString(existing.WorkspaceID))
		if !ok {
			return
		}
		if !canUseRuntimeForAgent(member, runtime) {
			writeError(w, http.StatusForbidden, "this runtime is private; only its owner can move agents onto it")
			return
		}
		params.RuntimeID = runtime.ID
		params.RuntimeMode = pgtype.Text{String: runtime.RuntimeMode, Valid: true}
	}
	// Invocation permission (MUL-3963). OWNER-ONLY write: access is the one
	// agent property a workspace admin may NOT change (only the owner decides
	// who can run their agent, so admin-authored access would be confusing).
	//
	// Non-owner behaviour: a *real* change is rejected with 403 so the contract
	// is explicit and matches the owner-only UI (the picker is read-only for
	// non-owners). A no-op resubmit — an admin editing OTHER fields via a
	// PATCH-as-PUT client that echoes the unchanged permission back — is
	// tolerated (dropped) so it doesn't break legitimate admin edits.
	_, hasPermissionMode := rawFields["permission_mode"]
	_, hasTargets := rawFields["invocation_targets"]
	permissionTouched := hasPermissionMode || hasTargets || req.Visibility != nil
	replacePermissionTargets := false
	var resolvedPerm resolvedPermission
	if permissionTouched {
		isAgentOwner := uuidToString(existing.OwnerID) == requestUserID(r)
		if !isAgentOwner {
			changed, permErr := h.permissionInputChangesAgent(r.Context(), existing, req, hasPermissionMode, hasTargets)
			if permErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to evaluate invocation permission change")
				return
			}
			if changed {
				writeError(w, http.StatusForbidden, "only the agent owner can change access (permission_mode / invocation_targets)")
				return
			}
			slog.Debug("update agent: non-owner permission fields matched current state; ignored",
				append(logger.RequestAttrs(r), "agent_id", id)...)
		} else {
			var targetsDTO []AgentInvocationTargetDTO
			if req.InvocationTargets != nil {
				targetsDTO = *req.InvocationTargets
			}
			perm, _, permErr := parsePermissionInput(existing.WorkspaceID, req.PermissionMode, targetsDTO, hasPermissionMode, hasTargets, req.Visibility)
			if permErr != nil {
				writeError(w, http.StatusBadRequest, permErr.Error())
				return
			}
			resolvedPerm = perm
			replacePermissionTargets = true
			params.PermissionMode = pgtype.Text{String: perm.mode, Valid: true}
			params.Visibility = pgtype.Text{String: perm.legacyVisibility(), Valid: true}
		}
	}
	if req.Status != nil {
		params.Status = pgtype.Text{String: *req.Status, Valid: true}
	}
	if req.MaxConcurrentTasks != nil {
		if err := validateAgentMaxConcurrentTasks(*req.MaxConcurrentTasks); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.MaxConcurrentTasks = pgtype.Int4{Int32: *req.MaxConcurrentTasks, Valid: true}
	}
	updated, err := h.Queries.UpdateAgent(r.Context(), params)
	if err != nil {
		// Unique constraint on (workspace_id, name) — mirror CreateAgent and
		// return a clear conflict instead of a 500 that leaks the raw
		// constraint name. The name can still be held by an *archived* agent
		// (the constraint does not exclude archived rows), so this is the only
		// signal the caller gets that a rename collided rather than the server
		// faulting.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "agent_workspace_name_unique" {
			name := ""
			if req.Name != nil {
				name = *req.Name
			}
			writeError(w, http.StatusConflict, fmt.Sprintf("an agent named %q already exists in this workspace", name))
			return
		}
		slog.Warn("update agent failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to update agent")
		return
	}

	// Invocation targets (MUL-3963): replace wholesale when the owner touched
	// permission. Done after the row update so a permission_mode flip and its
	// targets land together.
	if replacePermissionTargets {
		if err := h.replaceInvocationTargets(r.Context(), updated.ID, parseUUID(requestUserID(r)), resolvedPerm.targets); err != nil {
			slog.Warn("update agent: persist invocation targets failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to update invocation targets")
			return
		}
	}

	resp := h.agentToResponse(updated)
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, updated.ID); err != nil {
		slog.Warn("update agent: load invocation targets for response failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to load agent invocation targets")
		return
	}
	slog.Info("agent updated", append(logger.RequestAttrs(r), "agent_id", id, "workspace_id", uuidToString(updated.WorkspaceID))...)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, uuidToString(updated.WorkspaceID))
	h.publish(protocol.EventAgentStatus, uuidToString(updated.WorkspaceID), actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) ArchiveAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "agent is already archived")
		return
	}

	// A system agent belongs to the product, not to the workspace; the
	// product owns its lifecycle.
	if agent.SystemKey.Valid && agent.SystemKey.String != "" {
		writeError(w, http.StatusBadRequest, "this agent is built into Multica and cannot be archived")
		return
	}

	userID := requestUserID(r)
	archived, err := h.Queries.ArchiveAgent(r.Context(), db.ArchiveAgentParams{
		ID:         agent.ID,
		ArchivedBy: parseUUID(userID),
	})
	if err != nil {
		slog.Warn("archive agent failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to archive agent")
		return
	}

	// Cancel all pending/active tasks for this agent. The cancel and its
	// delegated-failure settlement commit together — a settlement issued after
	// the cancel committed could never be repaired. Chat tasks publish
	// task:cancelled after commit for chat lifecycle consumers; the aggregate
	// agent:archived event below remains unchanged.
	if _, err := h.TaskService.CancelTasksForArchivedAgent(r.Context(), agent.ID); err != nil {
		slog.Warn("cancel agent tasks on archive failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
	}

	wsID := uuidToString(archived.WorkspaceID)
	slog.Info("agent archived", append(logger.RequestAttrs(r), "agent_id", id, "workspace_id", wsID)...)
	resp := h.agentToResponse(archived)
	actorType, actorID := h.resolveActor(r, userID, wsID)
	h.publish(protocol.EventAgentArchived, wsID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) RestoreAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	if !agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "agent is not archived")
		return
	}

	restored, err := h.Queries.RestoreAgent(r.Context(), agent.ID)
	if err != nil {
		slog.Warn("restore agent failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to restore agent")
		return
	}

	wsID := uuidToString(restored.WorkspaceID)
	slog.Info("agent restored", append(logger.RequestAttrs(r), "agent_id", id, "workspace_id", wsID)...)
	resp := h.agentToResponse(restored)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, wsID)
	h.publish(protocol.EventAgentRestored, wsID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
	writeJSON(w, http.StatusOK, resp)
}

// CancelAgentTasks bulk-cancels every active task (queued/dispatched/running)
// belonging to an agent. Powers the agents-list "Cancel all tasks" row
// action. Same permission gate as archive (canManageAgent — owner or
// workspace admin/owner). Each cancelled row triggers a task:cancelled WS
// event so connected clients clear their live cards immediately.
//
// Note: a `running` task on the daemon side won't actually halt for up to
// ~5 seconds (daemon polls GetTaskStatus on that interval). The DB row is
// marked cancelled instantly, but the child process keeps going briefly;
// see daemon/daemon.go:919-942 for the polling loop. Surface this in the
// confirm-dialog copy so users aren't surprised by trailing transcript
// lines.
type cancelAgentTasksResponse struct {
	Cancelled int `json:"cancelled"`
}

func (h *Handler) CancelAgentTasks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}

	cancelled, err := h.TaskService.CancelTasksForAgent(r.Context(), parseUUID(id))
	if err != nil {
		slog.Warn("cancel agent tasks failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to cancel tasks")
		return
	}

	slog.Info("agent tasks cancelled",
		append(logger.RequestAttrs(r), "agent_id", id, "count", len(cancelled))...)
	writeJSON(w, http.StatusOK, cancelAgentTasksResponse{Cancelled: len(cancelled)})
}

// HeaderAgentTasksNextCursor preserves the array response used by installed clients.
// An absent header means this is the last page.
const HeaderAgentTasksNextCursor = "X-Agent-Tasks-Next-Cursor"

const defaultAgentTasksLimit = 200
const maxAgentTasksLimit = 200

func (h *Handler) ListAgentTasks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	// Run history is part of the private-agent gate ("查看历史会话"). Same
	// 403 semantics as GetAgent.
	workspaceID := uuidToString(agent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}

	includeUsage := false
	switch raw := strings.TrimSpace(r.URL.Query().Get("include_usage")); raw {
	case "", "false":
	case "true":
		includeUsage = true
	default:
		writeError(w, http.StatusBadRequest, "include_usage must be true or false")
		return
	}

	limit := defaultAgentTasksLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, maxAgentTasksLimit)
	}
	// An infinite upper bound gives both first and subsequent pages an indexed
	// tuple comparison, without an optional-cursor OR in the query plan.
	beforeCreatedAt := pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true}
	beforeID := agent.ID
	if raw := r.URL.Query().Get("before"); raw != "" {
		beforeCreatedAt, beforeID = parseTranscriptCursor(raw)
		if !beforeCreatedAt.Valid || !beforeID.Valid {
			writeError(w, http.StatusBadRequest, "invalid before cursor")
			return
		}
	}
	tasks, err := h.Queries.ListAgentTasks(r.Context(), db.ListAgentTasksParams{
		AgentID: agent.ID, BeforeCreatedAt: beforeCreatedAt, BeforeID: beforeID,
		PageLimit: int32(limit + 1),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent tasks")
		return
	}

	nextCursor := ""
	if len(tasks) > limit {
		tasks = tasks[:limit]
		last := tasks[len(tasks)-1]
		nextCursor = transcriptCursor(last.CreatedAt.Time, last.ID)
	}
	resp := make([]AgentTaskResponse, len(tasks))
	var taskIDs []pgtype.UUID
	if includeUsage {
		taskIDs = make([]pgtype.UUID, len(tasks))
	}
	for i, t := range tasks {
		resp[i] = taskToResponse(t, workspaceID)
		if includeUsage {
			taskIDs[i] = t.ID
		}
	}
	h.hydrateTaskSupplementMetadata(r.Context(), r, agent.WorkspaceID, tasks, resp)
	h.hydrateTaskAttributions(r.Context(), attributionsOf(resp))
	if includeUsage {
		if err := h.hydrateAgentTaskUsage(r.Context(), agent.ID, taskIDs, resp); err != nil {
			slog.Warn("list agent task usage failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to list agent task usage")
			return
		}
	}

	if nextCursor != "" {
		w.Header().Set(HeaderAgentTasksNextCursor, nextCursor)
	}
	writeJSON(w, http.StatusOK, resp)
}

// AgentActivityBucket is one day-bucketed throughput sample for the
// Agents-list ACTIVITY sparkline. bucket_at is midnight UTC of the day.
type AgentActivityBucket struct {
	AgentID        string  `json:"agent_id"`
	BucketAt       string  `json:"bucket_at"`
	TaskCount      int32   `json:"task_count"`
	FailedCount    int32   `json:"failed_count"`
	CompletedCount int32   `json:"completed_count"`
	CancelledCount int32   `json:"cancelled_count"`
	DurationMs     float64 `json:"duration_ms"`
	DurationCount  int32   `json:"duration_count"`
}

// AgentRunCount is the trailing-30-day total task run count per agent,
// powering the Agents-list RUNS column.
type AgentRunCount struct {
	AgentID  string `json:"agent_id"`
	RunCount int32  `json:"run_count"`
}

// WorkspaceWorkingAgent is the privacy-safe agent summary returned by the
// workspace working-agents endpoint. It deliberately carries only display
// information plus the current running-task count and referenced issue ids;
// full AgentResponse fields include runtime and integration configuration that
// this chip does not need.
type WorkspaceWorkingAgent struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	AvatarURL        *string  `json:"avatar_url"`
	RunningTaskCount int32    `json:"running_task_count"`
	IssueIDs         []string `json:"issue_ids"`
}

// ListWorkspaceWorkingAgents returns currently working user-authored agents in
// the workspace, independent of issue filters or Table pagination. The
// optional type query selects issue or chat work; omitting it keeps
// the all-sources projection. scope=mine narrows issue work to the authenticated
// member's selected My Issues relation. Access filtering mirrors the other
// workspace-wide agent aggregations so a private/non-allow-listed agent is
// never exposed by its name, avatar, count, or even presence.
func (h *Handler) ListWorkspaceWorkingAgents(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	workType := strings.TrimSpace(r.URL.Query().Get("type"))
	switch workType {
	case "", "issue", "chat":
	default:
		writeError(w, http.StatusBadRequest, "invalid type: must be issue or chat")
		return
	}

	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	mineRelation := strings.TrimSpace(r.URL.Query().Get("relation"))
	var memberID pgtype.UUID
	switch scope {
	case "":
		if mineRelation != "" {
			writeError(w, http.StatusBadRequest, "relation requires scope=mine")
			return
		}
	case "mine":
		if workType != "issue" {
			writeError(w, http.StatusBadRequest, "scope=mine requires type=issue")
			return
		}
		if mineRelation == "" {
			mineRelation = "any"
		}
		switch mineRelation {
		case "assigned", "created", "involved", "any":
		default:
			writeError(w, http.StatusBadRequest, "invalid relation: must be assigned, created, involved, or any")
			return
		}
		userID, ok := requireUserID(w, r)
		if !ok {
			return
		}
		var err error
		memberID, err = util.ParseUUID(userID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "user not authenticated")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid scope: must be mine")
		return
	}

	// Narrows the projection to one issue's direct children, so an issue
	// detail's sub-issue header reads this endpoint instead of deriving a
	// count client-side. Left zero when absent, which the query treats as
	// NULL and therefore as "no narrowing" — an older client that never
	// sends it keeps the exact workspace-wide behaviour.
	var parentIssueID pgtype.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("parent")); raw != "" {
		if workType != "issue" {
			writeError(w, http.StatusBadRequest, "parent requires type=issue")
			return
		}
		if scope != "" {
			writeError(w, http.StatusBadRequest, "parent cannot be combined with scope")
			return
		}
		var ok bool
		parentIssueID, ok = parseUUIDOrBadRequest(w, raw, "parent")
		if !ok {
			return
		}
	}

	rows, err := h.Queries.ListWorkspaceWorkingAgents(
		r.Context(),
		db.ListWorkspaceWorkingAgentsParams{
			WorkspaceID:   parseUUID(workspaceID),
			WorkType:      workType,
			MineRelation:  mineRelation,
			MemberID:      memberID,
			ParentIssueID: parentIssueID,
		},
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspace working agents")
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]WorkspaceWorkingAgent, 0, len(rows))
	for _, row := range rows {
		agentID := uuidToString(row.ID)
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		resp = append(resp, WorkspaceWorkingAgent{
			ID:               agentID,
			Name:             row.Name,
			AvatarURL:        h.resolveAvatarURLPtr(textToPtr(row.AvatarUrl)),
			RunningTaskCount: row.RunningTaskCount,
			IssueIDs:         uuidStringsOrEmpty(row.IssueIds),
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetWorkspaceAgentRunCounts returns 30-day total run counts for every
// agent in the workspace. Same single-fetch pattern as live-tasks /
// activity to keep the Agents list cheap regardless of agent count.
func (h *Handler) GetWorkspaceAgentRunCounts(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	rows, err := h.Queries.GetWorkspaceAgentRunCounts(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get agent run counts")
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]AgentRunCount, 0, len(rows))
	for _, row := range rows {
		agentID := uuidToString(row.AgentID)
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		resp = append(resp, AgentRunCount{
			AgentID:  agentID,
			RunCount: row.RunCount,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetWorkspaceAgentActivity30d returns per-agent daily task counts for the
// last 30 days, anchored on completed_at. Single workspace-wide read backs
// both the Agents list sparkline (uses the trailing 7 buckets) and the
// agent detail "Last 30 days" panel (uses all 30) — one fetch is cheaper
// than two. Front-end fills missing days with zero; the back-end omits
// empty buckets to keep the response small.
func (h *Handler) GetWorkspaceAgentActivity30d(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	rows, err := h.Queries.GetWorkspaceAgentActivity30d(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get agent activity")
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]AgentActivityBucket, 0, len(rows))
	for _, row := range rows {
		agentID := uuidToString(row.AgentID)
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		resp = append(resp, AgentActivityBucket{
			AgentID:        agentID,
			BucketAt:       timestampToString(row.Bucket),
			TaskCount:      row.TaskCount,
			FailedCount:    row.FailedCount,
			CompletedCount: row.CompletedCount,
			CancelledCount: row.CancelledCount,
			DurationMs:     row.DurationMs,
			DurationCount:  row.DurationCount,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// ListWorkspaceAgentTaskSnapshot returns the workspace-wide task data the
// front-end reads for two things: every active task
// (queued/dispatched/running/waiting_local_directory), which is the current
// workload presence derives from, plus each agent's most recent OUTCOME task
// (completed/failed only), which is no longer part of presence since #1823 and
// only feeds the agent hover card's "last activity" line. Cancelled tasks are
// excluded from the outcome half by design — cancel is a procedural signal
// ("attempt aborted"), not an outcome, so it must not mask a prior failure.
// Per-agent filtering happens in the front-end against this snapshot.
//
// The outcome half is deliberately still served here so shipped desktop builds
// keep working; MUL-5436 tracks moving it to a dedicated lazy endpoint.
func (h *Handler) ListWorkspaceAgentTaskSnapshot(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	tasks, err := h.Queries.ListWorkspaceAgentTaskSnapshot(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent task snapshot")
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]AgentTaskResponse, 0, len(tasks))
	for _, t := range tasks {
		if _, ok := allowed[uuidToString(t.AgentID)]; !ok {
			continue
		}
		resp = append(resp, taskToResponse(t, workspaceID))
	}
	h.hydrateTaskAttributions(r.Context(), attributionsOf(resp))

	writeJSON(w, http.StatusOK, resp)
}
