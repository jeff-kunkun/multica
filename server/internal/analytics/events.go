package analytics

import "strings"

// Event names. This file is the source-of-truth catalog; keep
// packages/core/analytics in sync with it.
const (
	EventSignup             = "signup"
	EventWorkspaceCreated   = "workspace_created"
	EventRuntimeRegistered  = "runtime_registered"
	EventRuntimeReady       = "runtime_ready"
	EventRuntimeFailed      = "runtime_failed"
	EventRuntimeOffline     = "runtime_offline"
	EventIssueExecuted      = "issue_executed"
	EventIssueCreated       = "issue_created"
	EventChatMessageSent    = "chat_message_sent"
	EventTeamInviteSent     = "team_invite_sent"
	EventTeamInviteAccepted = "team_invite_accepted"
	EventAgentCreated       = "agent_created"
	EventFeedbackSubmitted  = "feedback_submitted"
)

const EventSchemaVersion = 2

// metricsOnlyEvents lists every server-side event that is recorded to
// Prometheus (via metrics.IncForEvent, for Grafana) but deliberately NOT
// shipped to PostHog. metrics.RecordEvent consults this set and skips the
// PostHog Capture for these names while still incrementing the counter.
//
// As of MUL-4127, PostHog is no longer used for server-side product analytics:
// the acquisition / activation / expansion funnel is now read straight from the
// operational database and from these Grafana counters, so the redundant
// PostHog copy of every product event was retired. That makes ALL server-side
// events metrics-only — both the product-behaviour group and the original
// high-volume runtime telemetry are Prometheus-only. PostHog now only
// receives frontend error/crash telemetry ($exception, client_crash,
// client_unresponsive); see packages/core/analytics.
//
// Note: agent_task_* lifecycle events are also Prometheus-only, but their
// Prometheus side is handled by typed BusinessMetrics.RecordTask* methods, so
// they never build an analytics.Event in the first place and don't need an
// entry here.
var metricsOnlyEvents = map[string]struct{}{
	// Product-behaviour events — DB + Grafana are the source of truth
	// (MUL-4127); the PostHog copy was redundant.
	EventSignup:             {},
	EventWorkspaceCreated:   {},
	EventIssueCreated:       {},
	EventIssueExecuted:      {},
	EventChatMessageSent:    {},
	EventTeamInviteSent:     {},
	EventTeamInviteAccepted: {},
	EventAgentCreated:       {},
	EventFeedbackSubmitted:  {},
	// High-volume runtime lifecycle telemetry — always
	// Prometheus-only (Grafana already carries the equivalent counters).
	EventRuntimeRegistered: {},
	EventRuntimeReady:      {},
	EventRuntimeFailed:     {},
	EventRuntimeOffline:    {},
}

// IsMetricsOnly reports whether an event name is recorded to Prometheus but must
// not be sent to PostHog. As of MUL-4127 this is true for every server-side
// event. See metricsOnlyEvents.
func IsMetricsOnly(name string) bool {
	_, ok := metricsOnlyEvents[name]
	return ok
}

const (
	SourceManual = "manual"
	SourceChat   = "chat"
	SourceAPI    = "api"
)

// CoreProperties are the shared join and segmentation fields used by the
// canonical PostHog events. Empty values are omitted, except is_demo which is
// always stamped so dashboards can filter demo data without sparse-property
// edge cases.
type CoreProperties struct {
	UserID        string
	WorkspaceID   string
	AgentID       string
	TaskID        string
	IssueID       string
	ChatSessionID string
	Source        string
	RuntimeMode   string
	Provider      string
	IsDemo        bool
}

type TaskContext = CoreProperties

// Platform is used as the "platform" event property so funnels can split by
// web / desktop / cli. Request-path events use PlatformServer as a fallback
// when the caller is a server-originating action (e.g. auto-created user);
// otherwise the frontend passes the real platform via a header / body field
// in later iterations.
const (
	PlatformServer  = "server"
	PlatformWeb     = "web"
	PlatformDesktop = "desktop"
	PlatformCLI     = "cli"
)

// Signup builds the signup event. signupSource is populated from the
// frontend's stored UTM/referrer cookie if present; leave empty otherwise.
func Signup(userID, email, signupSource string) Event {
	return Event{
		Name:       EventSignup,
		DistinctID: userID,
		Properties: map[string]any{
			"email_domain":  emailDomain(email),
			"signup_source": signupSource,
		},
		SetOnce: map[string]any{
			"email":         email,
			"signup_source": signupSource,
		},
	}
}

// WorkspaceCreated builds the workspace_created event. "Is this the user's
// first workspace?" is deliberately not stamped here — it's derived in
// PostHog by checking whether the user has a prior workspace_created event.
func WorkspaceCreated(userID, workspaceID string) Event {
	return Event{
		Name:        EventWorkspaceCreated,
		DistinctID:  userID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(nil, CoreProperties{
			UserID:      userID,
			WorkspaceID: workspaceID,
			Source:      SourceManual,
		}),
	}
}

// RuntimeRegistered fires on the first time a (workspace, daemon, provider)
// triple is upserted. The handler uses a `xmax = 0` flag returned from the
// upsert query to distinguish inserts from updates — heartbeats and repeat
// registrations never emit this event.
//
// ownerID may be empty when the daemon authenticates via a daemon token
// (no user context); downstream funnels that need per-user attribution
// fall back to `workspace_id` as the grouping key.
func RuntimeRegistered(ownerID, workspaceID, runtimeID, daemonID, provider, runtimeVersion, cliVersion string) Event {
	distinct := ownerID
	if distinct == "" {
		// A per-workspace synthetic id keeps PostHog from merging unrelated
		// daemon registrations across workspaces under a single "anonymous"
		// person. It's stable within a workspace so repeat heartbeats (which
		// don't emit anyway) would at least group correctly.
		distinct = "workspace:" + workspaceID
	}
	return Event{
		Name:        EventRuntimeRegistered,
		DistinctID:  distinct,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"runtime_id":      runtimeID,
			"daemon_id":       daemonID,
			"provider":        provider,
			"runtime_mode":    "local",
			"runtime_version": runtimeVersion,
			"cli_version":     cliVersion,
		}, CoreProperties{
			UserID:      ownerID,
			WorkspaceID: workspaceID,
			Source:      SourceManual,
			RuntimeMode: "local",
			Provider:    provider,
		}),
	}
}

func RuntimeReady(ownerID, workspaceID, runtimeID, daemonID, provider string, readyDurationMS int64) Event {
	distinct := ownerID
	if distinct == "" {
		distinct = "workspace:" + workspaceID
	}
	props := map[string]any{
		"runtime_id": runtimeID,
		"daemon_id":  daemonID,
	}
	if readyDurationMS > 0 {
		props["ready_duration_ms"] = readyDurationMS
	}
	return Event{
		Name:        EventRuntimeReady,
		DistinctID:  distinct,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(props, CoreProperties{
			UserID:      ownerID,
			WorkspaceID: workspaceID,
			Source:      SourceManual,
			RuntimeMode: "local",
			Provider:    provider,
		}),
	}
}

func RuntimeFailed(ownerID, workspaceID, daemonID, provider, failureReason, errorType string, recoverable bool) Event {
	distinct := ownerID
	if distinct == "" && workspaceID != "" {
		distinct = "workspace:" + workspaceID
	}
	return Event{
		Name:        EventRuntimeFailed,
		DistinctID:  distinct,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"daemon_id":      daemonID,
			"failure_reason": failureReason,
			"error_type":     errorType,
			"recoverable":    recoverable,
		}, CoreProperties{
			UserID:      ownerID,
			WorkspaceID: workspaceID,
			Source:      SourceManual,
			RuntimeMode: "local",
			Provider:    provider,
		}),
	}
}

func RuntimeOffline(ownerID, workspaceID, runtimeID, daemonID, provider string) Event {
	distinct := ownerID
	if distinct == "" {
		distinct = "workspace:" + workspaceID
	}
	return Event{
		Name:        EventRuntimeOffline,
		DistinctID:  distinct,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"runtime_id": runtimeID,
			"daemon_id":  daemonID,
		}, CoreProperties{
			UserID:      ownerID,
			WorkspaceID: workspaceID,
			Source:      SourceManual,
			RuntimeMode: "local",
			Provider:    provider,
		}),
	}
}

// IssueExecuted fires at most once per issue lifetime — on the first task
// completion that flips `issues.first_executed_at` from NULL via an atomic
// UPDATE. Retries, re-assignments, and comment-triggered follow-ups never
// re-emit, which is what keeps the ≥1/≥2/≥5/≥10 funnel buckets honest.
//
// Deliberately not stamped here: the workspace's Nth-issue ordinal.
// Computing it at emit time is not atomic (two concurrent first-completions
// both read count=1, both emit n=1), and PostHog derives the same number
// exactly at query time from the event stream.
func IssueExecuted(actorID, workspaceID, issueID, taskID, agentID, source, runtimeMode, provider string, taskDurationMS int64) Event {
	return Event{
		Name:        EventIssueExecuted,
		DistinctID:  actorID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"issue_id":         issueID,
			"task_id":          taskID,
			"agent_id":         agentID,
			"task_duration_ms": taskDurationMS,
			"duration_ms":      taskDurationMS,
		}, CoreProperties{
			UserID:      nonAgentUserID(actorID),
			WorkspaceID: workspaceID,
			AgentID:     agentID,
			TaskID:      taskID,
			IssueID:     issueID,
			Source:      source,
			RuntimeMode: runtimeMode,
			Provider:    provider,
		}),
	}
}

func IssueCreated(actorID, workspaceID, issueID, agentID, taskID, source, platform string) Event {
	props := map[string]any{}
	if platform != "" {
		props["platform"] = platform
	}
	return Event{
		Name:        EventIssueCreated,
		DistinctID:  actorID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(props, CoreProperties{
			UserID:      nonAgentUserID(actorID),
			WorkspaceID: workspaceID,
			AgentID:     agentID,
			TaskID:      taskID,
			IssueID:     issueID,
			Source:      source,
		}),
	}
}

func ChatMessageSent(userID, workspaceID, chatSessionID, taskID, agentID, runtimeMode, provider, platform string) Event {
	props := map[string]any{}
	if platform != "" {
		props["platform"] = platform
	}
	return Event{
		Name:        EventChatMessageSent,
		DistinctID:  userID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(props, CoreProperties{
			UserID:        userID,
			WorkspaceID:   workspaceID,
			AgentID:       agentID,
			TaskID:        taskID,
			ChatSessionID: chatSessionID,
			Source:        SourceChat,
			RuntimeMode:   runtimeMode,
			Provider:      provider,
		}),
	}
}

// TeamInviteSent fires when a workspace admin creates an invitation.
// inviteMethod is "email" for now; future non-email invite flows can pass
// their own value to keep this stable.
func TeamInviteSent(inviterID, workspaceID, invitedEmail, inviteMethod string) Event {
	return Event{
		Name:        EventTeamInviteSent,
		DistinctID:  inviterID,
		WorkspaceID: workspaceID,
		Properties: map[string]any{
			"invited_email_domain": emailDomain(invitedEmail),
			"invite_method":        inviteMethod,
		},
	}
}

// TeamInviteAccepted fires when the invitee accepts and joins the workspace.
// daysSinceInvite lets us segment fast-acceptance (warm) from long-tail
// acceptance (someone dug through old email).
func TeamInviteAccepted(inviteeID, workspaceID string, daysSinceInvite int64) Event {
	return Event{
		Name:        EventTeamInviteAccepted,
		DistinctID:  inviteeID,
		WorkspaceID: workspaceID,
		Properties: map[string]any{
			"days_since_invite": daysSinceInvite,
		},
	}
}

// AgentCreated fires whenever a new agent is added to a workspace.
// `isFirstAgentInWorkspace` lets the funnel isolate the first agent from
// later agent additions.
//
// template is the creation-source attribution supplied by the caller; empty
// identifies a manually authored agent.
func AgentCreated(actorID, workspaceID, agentID, provider, runtimeMode, template string, isFirstAgentInWorkspace bool) Event {
	return Event{
		Name:        EventAgentCreated,
		DistinctID:  actorID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"agent_id":                    agentID,
			"provider":                    provider,
			"runtime_mode":                runtimeMode,
			"template":                    template,
			"is_first_agent_in_workspace": isFirstAgentInWorkspace,
		}, CoreProperties{
			UserID:      actorID,
			WorkspaceID: workspaceID,
			AgentID:     agentID,
			Source:      SourceManual,
			RuntimeMode: runtimeMode,
			Provider:    provider,
		}),
	}
}

// FeedbackSubmitted fires after a feedback row is successfully inserted.
// The raw message is stored in the DB and never broadcast — we only emit a
// coarse length bucket, an image-presence flag, the kind picker selection,
// and the client platform / version so support can segment without leaking
// content.
func FeedbackSubmitted(userID, workspaceID, kind string, messageLen int, hasImages bool, platform, appVersion string) Event {
	props := map[string]any{
		"message_length_bucket": feedbackLengthBucket(messageLen),
		"has_images":            hasImages,
	}
	if kind != "" {
		props["kind"] = kind
	}
	if platform != "" {
		props["platform"] = platform
	}
	if appVersion != "" {
		props["app_version"] = appVersion
	}
	return Event{
		Name:        EventFeedbackSubmitted,
		DistinctID:  userID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(props, CoreProperties{
			UserID:      userID,
			WorkspaceID: workspaceID,
			Source:      "ops_feedback",
		}),
	}
}

func withCoreProperties(props map[string]any, core CoreProperties) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	if core.UserID != "" {
		props["user_id"] = core.UserID
	}
	if core.AgentID != "" {
		props["agent_id"] = core.AgentID
	}
	if core.TaskID != "" {
		props["task_id"] = core.TaskID
	}
	if core.IssueID != "" {
		props["issue_id"] = core.IssueID
	}
	if core.ChatSessionID != "" {
		props["chat_session_id"] = core.ChatSessionID
	}
	if core.Source != "" {
		props["source"] = core.Source
	}
	if core.RuntimeMode != "" {
		props["runtime_mode"] = core.RuntimeMode
	}
	if core.Provider != "" {
		props["provider"] = core.Provider
	}
	props["is_demo"] = core.IsDemo
	return props
}

func nonAgentUserID(distinct string) string {
	if distinct == "" || strings.Contains(distinct, ":") {
		return ""
	}
	return distinct
}

func feedbackLengthBucket(n int) string {
	switch {
	case n < 100:
		return "0-100"
	case n < 500:
		return "100-500"
	case n < 2000:
		return "500-2000"
	default:
		return "2000+"
	}
}

func emailDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}
