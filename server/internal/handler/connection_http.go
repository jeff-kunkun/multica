package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/gitconn"
	"github.com/multica-ai/multica/server/internal/integrations/vcs"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// connectionOwner decides who a new connection belongs to. An agent --yes
// forces a personal connection owned by the task initiator, and it only
// matches repositories that person registered. A workspace connection stays
// admin-only. The server computes covers from the token; the request cannot
// set them.
func (h *Handler) connectionOwner(w http.ResponseWriter, r *http.Request, ws pgtype.UUID, member db.Member, req connectVCSRequest) (personal bool, owner pgtype.UUID, ok bool) {
	owner = pgtype.UUID{Valid: true}
	if req.AgentYes {
		actorType, _ := h.resolveActor(r, uuidToString(member.UserID), uuidToString(ws))
		if actorType != "agent" {
			writeError(w, http.StatusBadRequest, "agent_yes is only accepted from an agent task")
			return false, owner, false
		}
		taskID, err := util.ParseUUID(r.Header.Get("X-Task-ID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "agent_yes requires the task that started this run")
			return false, owner, false
		}
		task, err := h.Queries.GetAgentTask(r.Context(), taskID)
		if err != nil || !task.InitiatorUserID.Valid {
			writeError(w, http.StatusBadRequest, "这条任务没有发起人，不能用 --yes 登记个人连接")
			return false, owner, false
		}
		if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
			UserID: task.InitiatorUserID, WorkspaceID: ws,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "任务发起人不是这个工作区的成员")
			return false, owner, false
		}
		return true, task.InitiatorUserID, true
	}
	if !req.Personal {
		if !roleAllowed(member.Role, "owner", "admin") {
			writeError(w, http.StatusForbidden, "只有管理员可以添加工作区连接")
			return false, owner, false
		}
		return false, owner, true
	}
	if !member.UserID.Valid {
		writeError(w, http.StatusBadRequest, "personal connection requires a member")
		return false, owner, false
	}
	return true, member.UserID, true
}

func canManageConnection(member db.Member, conn db.VcsConnection) bool {
	if roleAllowed(member.Role, "owner", "admin") {
		return true
	}
	return conn.Personal && conn.OwnerKey.Valid && uuidToString(conn.OwnerKey) == uuidToString(member.UserID)
}

// TestVCSConnection re-checks a stored token. The token is not returned.
func (h *Handler) TestVCSConnection(w http.ResponseWriter, r *http.Request) {
	wsUUID, conn, ok := h.loadManagedConnection(w, r)
	if !ok {
		return
	}
	_ = wsUUID
	if !h.isVCSConfigured() {
		writeFeatureDisabled(w, "vcs_not_configured", "vcs integration not configured (MULTICA_VCS_SECRET_KEY unset)")
		return
	}
	provider, found := vcs.For(conn.Provider)
	if !found {
		writeError(w, http.StatusBadRequest, "unsupported provider")
		return
	}
	token, err := h.openVCSSecret(conn.AccessTokenEncrypted)
	if err != nil || token == "" {
		writeError(w, http.StatusInternalServerError, "failed to read connection")
		return
	}
	account, err := provider.ValidateToken(r.Context(), conn.InstanceUrl, token)
	if err != nil {
		if errors.Is(err, vcs.ErrUnauthorized) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "the provider rejected the access token"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "could not reach the provider instance"})
		return
	}
	covers := account.Covers
	if covers == nil {
		covers = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "account_login": account.Login, "covers": covers,
	})
}

// GetVCSRepoStatus reports which connection, if any, covers each known repository.
func (h *Handler) GetVCSRepoStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	rows, err := h.Queries.ListVCSConnectionsByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list connections")
		return
	}
	installs, err := h.Queries.ListGitHubInstallationsByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list installations")
		return
	}
	repos := h.reposForStatus(r.Context(), wsUUID)
	type repoStatus struct {
		RepoKey      string `json:"repo_key"`
		URL          string `json:"url,omitempty"`
		Status       string `json:"status"`
		ConnectionID string `json:"connection_id,omitempty"`
		AccountLogin string `json:"account_login,omitempty"`
		MatchedBy    string `json:"matched_by"`
	}
	out := make([]repoStatus, 0, len(repos))
	for _, repo := range repos {
		item := repoStatus{RepoKey: repo.Key, URL: repo.URL, Status: "missing", MatchedBy: ""}
		for _, inst := range installs {
			if gitconn.AppCovers(inst.AccountLogin, repo.Key) {
				item.Status = "connected"
				item.MatchedBy = "app"
				item.AccountLogin = inst.AccountLogin
				break
			}
		}
		if item.MatchedBy == "" {
			if conn, ok := matchingTokenConn(rows, repo); ok {
				item.Status = "connected"
				item.MatchedBy = "token"
				item.ConnectionID = uuidToString(conn.ID)
				item.AccountLogin = conn.AccountLogin
			}
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": out})
}

func (h *Handler) reposForStatus(ctx context.Context, ws pgtype.UUID) []gitconn.Repo {
	out := h.workspaceGitRepos(ctx, ws)
	rows, err := h.Queries.ListGitProjectResourcesByWorkspace(ctx, ws)
	if err == nil {
		for _, row := range rows {
			repo, ok := gitconn.FromResource(row.ResourceType, row.ResourceRef)
			if !ok {
				continue
			}
			if reg := h.lookupRepoRegistrant(ctx, ws, repo.Key); reg != "" {
				repo.Registrant = reg
			} else if row.CreatedBy.Valid {
				repo.Registrant = uuidToString(row.CreatedBy)
			}
			out = append(out, repo)
		}
	}
	return dedupeRepos(out)
}

func (h *Handler) loadManagedConnection(w http.ResponseWriter, r *http.Request) (pgtype.UUID, db.VcsConnection, bool) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return pgtype.UUID{}, db.VcsConnection{}, false
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return pgtype.UUID{}, db.VcsConnection{}, false
	}
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "connectionId"), "connection id")
	if !ok {
		return pgtype.UUID{}, db.VcsConnection{}, false
	}
	conn, err := h.Queries.GetVCSConnectionByID(r.Context(), idUUID)
	if err != nil || uuidToString(conn.WorkspaceID) != uuidToString(wsUUID) {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to load connection")
			return pgtype.UUID{}, db.VcsConnection{}, false
		}
		writeError(w, http.StatusNotFound, "connection not found")
		return pgtype.UUID{}, db.VcsConnection{}, false
	}
	if !canManageConnection(member, conn) {
		writeError(w, http.StatusForbidden, "只有管理员或这条个人连接的本人可以操作")
		return pgtype.UUID{}, db.VcsConnection{}, false
	}
	return wsUUID, conn, true
}
