package protocol

// Event types for WebSocket communication between server, web clients, and daemon.
const (
	// Issue events
	EventIssueCreated            = "issue:created"
	EventIssueUpdated            = "issue:updated"
	EventIssueDeleted            = "issue:deleted"
	EventIssueMetadataChanged    = "issue_metadata:changed"
	EventIssueAttachmentsChanged = "issue_attachments:changed"

	// Comment events
	EventCommentCreated       = "comment:created"
	EventCommentUpdated       = "comment:updated"
	EventCommentDeleted       = "comment:deleted"
	EventCommentResolved      = "comment:resolved"
	EventCommentUnresolved    = "comment:unresolved"
	EventReactionAdded        = "reaction:added"
	EventReactionRemoved      = "reaction:removed"
	EventIssueReactionAdded   = "issue_reaction:added"
	EventIssueReactionRemoved = "issue_reaction:removed"

	// Agent events
	EventAgentStatus   = "agent:status"
	EventAgentCreated  = "agent:created"
	EventAgentArchived = "agent:archived"
	EventAgentRestored = "agent:restored"

	// Task events (server <-> daemon).
	// Each event maps to a status transition on agent_task_queue. Front-end
	// subscribes by `task:` prefix and invalidates the workspace task
	// snapshot, so the granularity here is "what does the user want to see
	// change" — not "every internal status flip".
	EventTaskQueued                = "task:queued"                  // ∅ → queued (enqueue / retry create)
	EventTaskDispatch              = "task:dispatch"                // queued → dispatched (daemon claim)
	EventTaskRunning               = "task:running"                 // dispatched → running (daemon started)
	EventTaskWaitingLocalDirectory = "task:waiting_local_directory" // dispatched → waiting_local_directory (daemon parked on a busy local_directory path)
	EventTaskProgress              = "task:progress"
	EventTaskCompleted             = "task:completed" // running → completed
	EventTaskFailed                = "task:failed"    // running → failed
	EventTaskMessage               = "task:message"
	EventTaskCancelled             = "task:cancelled" // * → cancelled

	// Inbox events
	EventInboxNew           = "inbox:new"
	EventInboxRead          = "inbox:read"
	EventInboxUnread        = "inbox:unread"
	EventInboxArchived      = "inbox:archived"
	EventInboxUnarchived    = "inbox:unarchived"
	EventInboxBatchRead     = "inbox:batch-read"
	EventInboxBatchArchived = "inbox:batch-archived"

	// Workspace events
	EventWorkspaceUpdated = "workspace:updated"
	EventWorkspaceDeleted = "workspace:deleted"

	// Member events
	EventMemberAdded   = "member:added"
	EventMemberRemoved = "member:removed"

	// Subscriber events
	EventSubscriberAdded   = "subscriber:added"
	EventSubscriberRemoved = "subscriber:removed"

	// Activity events
	EventActivityCreated = "activity:created"

	// Chat events
	EventChatMessage = "chat:message"
	EventChatDone    = "chat:done"
	// EventChatCancelFinalized carries the deferred outcome of a cancelled
	// chat task once the daemon has flushed its transcript (or the sweeper
	// grace period expired): either a late "Stopped." assistant message or a
	// draft restore (#5219).
	EventChatCancelFinalized = "chat:cancel_finalized"
	EventChatSessionRead     = "chat:session_read"
	EventChatSessionDeleted  = "chat:session_deleted"
	EventChatSessionUpdated  = "chat:session_updated"

	// Project events
	EventProjectCreated         = "project:created"
	EventProjectUpdated         = "project:updated"
	EventProjectDeleted         = "project:deleted"
	EventProjectMembersChanged  = "project:members_changed"
	EventProjectResourceCreated = "project_resource:created"
	EventProjectResourceUpdated = "project_resource:updated"
	EventProjectResourceDeleted = "project_resource:deleted"

	// Label events
	EventLabelCreated       = "label:created"
	EventLabelUpdated       = "label:updated"
	EventLabelDeleted       = "label:deleted"
	EventIssueLabelsChanged = "issue_labels:changed"

	// Custom property events. Definitions are archived, never deleted, so
	// there is no property:deleted — archive arrives as property:updated.
	EventPropertyCreated        = "property:created"
	EventPropertyUpdated        = "property:updated"
	EventIssuePropertiesChanged = "issue_properties:changed"

	// Issue status catalog events (MUL-6243). ONE event for all four writes
	// — create, edit, archive, reorder — rather than a verb per write, because
	// the catalog is read as a whole table and every client answers all four
	// the same way: re-read it. Splitting it would mint four contracts that no
	// consumer can tell apart, and reorder has no single row to name anyway.
	// The `action` in the payload is advisory (it makes a frame in devtools
	// self-describing); nothing routes on it.
	EventIssueStatusChanged = "issue_status:changed"

	// Pin events
	EventPinCreated   = "pin:created"
	EventPinDeleted   = "pin:deleted"
	EventPinReordered = "pin:reordered"

	// Invitation events
	EventInvitationCreated  = "invitation:created"
	EventInvitationAccepted = "invitation:accepted"
	EventInvitationDeclined = "invitation:declined"
	EventInvitationRevoked  = "invitation:revoked"

	// Daemon events
	EventDaemonHeartbeat               = "daemon:heartbeat"
	EventDaemonHeartbeatAck            = "daemon:heartbeat_ack"
	EventDaemonRegister                = "daemon:register"
	EventDaemonTaskAvailable           = "daemon:task_available"
	EventDaemonTaskSupplementAvailable = "daemon:task_supplement_available"
	EventDaemonRuntimeProfilesChanged  = "daemon:runtime_profiles_changed"
	EventDaemonWorkspacesChanged       = "daemon:workspaces_changed"
	// EventDaemonPendingWork is a runtime-scoped hint that a heartbeat-carried
	// request (model discovery) is queued for that runtime. Without it the
	// daemon only learns about the request on its next scheduled heartbeat,
	// adding up to one HeartbeatInterval (15s by default) of dead wait to an
	// interactive UI flow (MUL-5444). The hint carries no work itself: the
	// daemon still pulls the request through the normal heartbeat claim, so a
	// lost or duplicated hint is harmless.
	EventDaemonPendingWork = "daemon:pending_work"
	// Generic daemon→server request/response over the WebSocket control
	// connection (MUL-4257). The daemon sends EventDaemonRPCRequest with a
	// correlation id + method + body; the server replies EventDaemonRPCResponse
	// with the same request id. This is the transport for WS-first claim (with
	// HTTP fallback) and any future daemon→server RPC.
	EventDaemonRPCRequest  = "daemon:rpc_request"
	EventDaemonRPCResponse = "daemon:rpc_response"

	// GitHub integration events
	EventGitHubInstallationCreated = "github_installation:created"
	EventGitHubInstallationDeleted = "github_installation:deleted"
	EventPullRequestLinked         = "pull_request:linked"
	EventPullRequestUpdated        = "pull_request:updated"
	EventPullRequestUnlinked       = "pull_request:unlinked"

	// VCS integration events (Forgejo / Gitea / GitLab)
	EventVCSConnectionCreated = "vcs_connection:created"
	EventVCSConnectionDeleted = "vcs_connection:deleted"
)
