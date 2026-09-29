package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/gitconn"
	"github.com/multica-ai/multica/server/internal/repoident"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// noteUnconnectedProjectRepo asks the repo registrant once when a project
// binds a repository no connection covers. Failure to ask does not fail the
// bind: the project resource is already saved.
func (h *Handler) noteUnconnectedProjectRepo(ctx context.Context, project db.Project, resourceType string, ref []byte, creator pgtype.UUID) {
	repo, ok := gitconn.FromResource(resourceType, ref)
	if !ok {
		return
	}
	if reg := h.lookupRepoRegistrant(ctx, project.WorkspaceID, repo.Key); reg != "" {
		repo.Registrant = reg
	} else if creator.Valid {
		repo.Registrant = uuidToString(creator)
	}
	if h.repoCovered(ctx, project.WorkspaceID, repo) {
		return
	}
	h.askForConnection(ctx, project.WorkspaceID, repo, &project, nil)
}

// askForConnection notifies one person about one repository. A second call
// before a covering connection is saved writes nothing. An issue uses the
// summon entry; a project bind with no issue writes the inbox row directly.
// If the notification fails, the dedupe row is removed so a retry can ask.
func (h *Handler) askForConnection(ctx context.Context, ws pgtype.UUID, repo gitconn.Repo, project *db.Project, issue *db.Issue) {
	recipient := h.connectionRecipient(ctx, ws, repo, project)
	if !recipient.Valid {
		return
	}
	nudge, err := h.Queries.InsertConnectionNudge(ctx, db.InsertConnectionNudgeParams{
		WorkspaceID: ws, RepoKey: repo.Key, RecipientID: recipient,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Warn("connection nudge insert failed", "repo", repo.Key, "error", err)
		return
	}
	msg := h.connectionAskMessage(ctx, ws, repo)
	if issue != nil {
		out, summonErr := h.summonPerson(ctx, service.SummonInput{
			Issue:      *issue,
			Recipient:  recipient,
			CallerType: "system",
			Source:     service.SummonSourceNeedsHuman,
			Reason:     msg,
			InboxTitle: "需要你接上仓库连接",
		})
		if summonErr != nil {
			_ = h.Queries.DeleteConnectionNudge(ctx, db.DeleteConnectionNudgeParams{WorkspaceID: ws, RepoKey: repo.Key})
			return
		}
		if out.InboxItem != nil {
			_ = h.Queries.SetConnectionNudgeInbox(ctx, db.SetConnectionNudgeInboxParams{
				WorkspaceID: ws, RepoKey: repo.Key, InboxItemID: out.InboxItem.ID,
			})
		}
		_ = nudge
		return
	}
	item, err := h.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		ID:            dbid.NewV7(),
		WorkspaceID:   ws,
		RecipientType: "member",
		RecipientID:   recipient,
		Type:          service.InboxTypeNeedsYou,
		Severity:      "action_required",
		Title:         "需要你接上仓库连接",
		Body:          pgtype.Text{String: msg, Valid: true},
		ActorType:     pgtype.Text{String: "system", Valid: true},
		Details:       []byte(`{"source":"connection"}`),
	})
	if err != nil {
		slog.Warn("connection nudge inbox failed", "repo", repo.Key, "error", err)
		_ = h.Queries.DeleteConnectionNudge(ctx, db.DeleteConnectionNudgeParams{WorkspaceID: ws, RepoKey: repo.Key})
		return
	}
	_ = h.Queries.SetConnectionNudgeInbox(ctx, db.SetConnectionNudgeInboxParams{
		WorkspaceID: ws, RepoKey: repo.Key, InboxItemID: item.ID,
	})
}

func (h *Handler) connectionAskMessage(ctx context.Context, ws pgtype.UUID, repo gitconn.Repo) string {
	slug := ""
	if workspace, err := h.Queries.GetWorkspace(ctx, ws); err == nil {
		slug = workspace.Slug
	}
	host, _, _, _ := gitconn.Parts(repo.Key)
	return gitconn.AskMessage(repo.Key, gitconn.SettingsURL(h.cfg.PublicURL, slug, host), gitconn.AddCommand(host))
}

func (h *Handler) connectionRecipient(ctx context.Context, ws pgtype.UUID, repo gitconn.Repo, project *db.Project) pgtype.UUID {
	var candidates []pgtype.UUID
	if id, err := util.ParseUUID(repo.Registrant); err == nil && id.Valid {
		candidates = append(candidates, id)
	}
	if project != nil {
		if project.LeadType.Valid && project.LeadType.String == "member" && project.LeadID.Valid {
			candidates = append(candidates, project.LeadID)
		}
		if project.CreatedBy.Valid {
			candidates = append(candidates, project.CreatedBy)
		}
	}
	for _, id := range candidates {
		if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID: id, WorkspaceID: ws,
		}); err == nil {
			return id
		}
	}
	return pgtype.UUID{}
}

func (h *Handler) clearCoveredNudges(ctx context.Context, conn db.VcsConnection) {
	nudges, err := h.Queries.ListConnectionNudgesByWorkspace(ctx, conn.WorkspaceID)
	if err != nil {
		return
	}
	view := vcsConnView(conn)
	for _, nudge := range nudges {
		repo := gitconn.Repo{Key: nudge.RepoKey, Registrant: uuidToString(nudge.RecipientID)}
		if !gitconn.Matches(view, repo) {
			continue
		}
		_ = h.Queries.DeleteConnectionNudge(ctx, db.DeleteConnectionNudgeParams{
			WorkspaceID: conn.WorkspaceID, RepoKey: nudge.RepoKey,
		})
	}
}

func vcsConnView(c db.VcsConnection) gitconn.Conn {
	covers := c.Covers
	if covers == nil {
		covers = []string{}
	}
	owner := ""
	if c.Personal && c.OwnerKey.Valid {
		owner = uuidToString(c.OwnerKey)
	}
	return gitconn.Conn{
		ID: uuidToString(c.ID), Provider: c.Provider, InstanceURL: c.InstanceUrl,
		AccountLogin: c.AccountLogin, Covers: covers, Personal: c.Personal, OwnerID: owner,
	}
}

func (h *Handler) repoCovered(ctx context.Context, ws pgtype.UUID, repo gitconn.Repo) bool {
	if h.appCoversRepo(ctx, ws, repo.Key) {
		return true
	}
	rows, err := h.Queries.ListVCSConnectionsByWorkspace(ctx, ws)
	if err != nil {
		return false
	}
	for _, row := range rows {
		if gitconn.Matches(vcsConnView(row), repo) {
			return true
		}
	}
	return false
}

func (h *Handler) appCoversRepo(ctx context.Context, ws pgtype.UUID, repoKey string) bool {
	rows, err := h.Queries.ListGitHubInstallationsByWorkspace(ctx, ws)
	if err != nil {
		return false
	}
	for _, row := range rows {
		if gitconn.AppCovers(row.AccountLogin, repoKey) {
			return true
		}
	}
	return false
}

func (h *Handler) lookupRepoRegistrant(ctx context.Context, ws pgtype.UUID, repoKey string) string {
	for _, repo := range h.workspaceGitRepos(ctx, ws) {
		if repo.Key == repoKey && repo.Registrant != "" {
			return repo.Registrant
		}
	}
	return ""
}

func (h *Handler) workspaceGitRepos(ctx context.Context, ws pgtype.UUID) []gitconn.Repo {
	workspace, err := h.Queries.GetWorkspace(ctx, ws)
	if err != nil || len(workspace.Repos) == 0 {
		return nil
	}
	var refs []workspaceRepoRef
	if err := json.Unmarshal(workspace.Repos, &refs); err != nil {
		return nil
	}
	out := make([]gitconn.Repo, 0, len(refs))
	for _, ref := range refs {
		key := string(repoident.NormalizeURL(ref.URL))
		if key == "" {
			continue
		}
		out = append(out, gitconn.Repo{Key: key, URL: ref.URL, Registrant: ref.CreatedBy})
	}
	return out
}
