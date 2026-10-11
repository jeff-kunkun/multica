package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/service"
)

type botWakeBlockRecord struct {
	ID            string    `json:"id"`
	BotID         string    `json:"bot_id"`
	AgentID       string    `json:"agent_id"`
	RootID        string    `json:"root_id"`
	TaskID        string    `json:"task_id,omitempty"`
	IssueID       string    `json:"issue_id,omitempty"`
	ChatSessionID string    `json:"chat_session_id,omitempty"`
	Limit         int       `json:"limit"`
	CreatedAt     time.Time `json:"created_at"`
}

type botWakeChainReport struct {
	Limit           int                  `json:"limit"`
	ReasonCode      string               `json:"reason_code"`
	Reset           string               `json:"reset"`
	HumanExempt     bool                 `json:"human_exempt"`
	VerifiedQuote   bool                 `json:"verified_quote_exempt"`
	SettingsKey     string               `json:"settings_key"`
	AuditVisible    bool                 `json:"audit_visible"`
	BlockedLast24h  *int                 `json:"blocked_last_24h,omitempty"`
	RecentBlocks    []botWakeBlockRecord `json:"recent_blocks,omitempty"`
	AuditReadFailed bool                 `json:"audit_read_failed,omitempty"`
}

// Only authenticated Bot provenance may narrow an audit to that Bot. Humans
// with workspace administration rights inspect visible blocks; other members
// see the guard's public contract without private chat or target identifiers.
func (h *Handler) botWakeChainReport(r *http.Request, workspaceID string) botWakeChainReport {
	out := botWakeChainReport{Limit: 4, ReasonCode: "bot_wake_chain_limit", Reset: "agent_reply_or_completed_run", HumanExempt: true, VerifiedQuote: true, SettingsKey: "bot_wake_chain_limit"}
	if h.Queries == nil {
		return out
	}
	if ws, err := h.Queries.GetWorkspace(r.Context(), parseUUID(workspaceID)); err == nil {
		out.Limit = service.BotWakeChainLimit(ws.Settings)
	}
	if h.DB == nil {
		return out
	}
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return out
	}
	bot := botsource.FromContext(r.Context())
	if !bot.ID.Valid {
		member, err := h.getWorkspaceMember(r.Context(), requestUserID(r), workspaceID)
		if err != nil || !roleAllowed(member.Role, "owner", "admin") {
			return out
		}
	}
	viewer, err := h.visibilityViewerForUser(r.Context(), parseUUID(workspaceID), parseUUID(requestUserID(r)))
	if err != nil {
		out.AuditReadFailed = true
		return out
	}
	out.AuditVisible = true
	args := []any{parseUUID(workspaceID), bot.ID, viewer.userID, viewer.projectIDs, viewer.role != "guest"}
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	issueVisible := viewer.issueVisibilitySQL("i", addArg)
	// This is the same visibility matrix as chatAccessFor: creator, workspace,
	// or an accessible project/direct share. An admin is not a chat creator.
	where := `b.workspace_id=$1 AND ($2::uuid IS NULL OR b.bot_id=$2)
	AND b.created_at >= now()-interval '24 hours'
	AND (b.issue_id IS NULL OR EXISTS (SELECT 1 FROM issue i WHERE i.id=b.issue_id AND (` + issueVisible + `)))
	AND (b.chat_session_id IS NULL OR EXISTS (SELECT 1 FROM chat_session cs WHERE cs.id=b.chat_session_id AND (
	cs.creator_id=$3 OR (cs.visibility='workspace' AND $5::boolean) OR (cs.visibility='project' AND (
	cs.project_id=ANY($4::uuid[]) OR EXISTS (SELECT 1 FROM chat_session_project csp WHERE csp.chat_session_id=cs.id AND csp.project_id=ANY($4::uuid[]))
	OR EXISTS (SELECT 1 FROM resource_share rs WHERE rs.workspace_id=cs.workspace_id AND rs.resource_type='chat_session' AND rs.resource_id=cs.id::text AND rs.member_id=$3)
	)))))`
	var count int
	if err := h.DB.QueryRow(r.Context(), `SELECT count(*) FROM bot_wake_block b WHERE `+where, args...).Scan(&count); err != nil {
		out.AuditReadFailed = true
		return out
	}
	out.BlockedLast24h = &count
	rows, err := h.DB.Query(r.Context(), `SELECT b.id::text,COALESCE(b.bot_id::text,''),COALESCE(b.agent_id::text,''),COALESCE(b.root_id::text,''),COALESCE(b.task_id::text,''),COALESCE(b.issue_id::text,''),COALESCE(b.chat_session_id::text,''),b.limit_value,b.created_at FROM bot_wake_block b WHERE `+where+` ORDER BY b.created_at DESC,b.id DESC LIMIT 100`, args...)
	if err != nil {
		out.AuditReadFailed = true
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var record botWakeBlockRecord
		if err := rows.Scan(&record.ID, &record.BotID, &record.AgentID, &record.RootID, &record.TaskID, &record.IssueID, &record.ChatSessionID, &record.Limit, &record.CreatedAt); err != nil {
			out.AuditReadFailed = true
			return out
		}
		out.RecentBlocks = append(out.RecentBlocks, record)
	}
	if rows.Err() != nil {
		out.AuditReadFailed = true
	}
	return out
}
