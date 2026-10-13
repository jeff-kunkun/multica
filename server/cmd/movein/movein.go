package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/mirror"
)

// mover copies one workspace of the old server into a local workspace.
//
// Every local row gets an id derived from the old one, so a second run finds
// what the first wrote and skips it. Rows are written straight to the
// database: no API handler runs, so nothing is routed, notified or put in an
// inbox. Every moved project, issue and comment is also a mirror_link row
// marked inbound, so the mirror treats the old original as the source and
// never writes over it.
type mover struct {
	old  *oldServer
	pool *pgxpool.Pool
	log  *slog.Logger

	oldWorkspace string
	target       string // mirror_link target key of the old workspace
	space        uuid.UUID

	workspaceID string
	userID      string // local member everything is written as

	oldSelf     string
	oldMembers  map[string]string // user id -> name
	oldAgents   map[string]oldAgent
	oldRuntimes map[string]string // runtime id -> provider

	issues   map[string]bool // old ids of the listed issues
	projects map[string]bool // old ids of the listed projects

	stats stats
	mu    sync.Mutex
}

type stats struct {
	Projects, ProjectsNew int
	Resources             int
	Issues, IssuesNew     int
	OldIssueTotal         int
	Comments              int
	Chats, ChatsNew       int
	Messages              int
}

const (
	kindChat     = "chat"
	kindMessage  = "chat_message"
	kindResource = "project_resource"
	workers      = 4
)

// spaceFor is the id namespace of one move: the same old workspace moved into
// the same local workspace always gets the same ids.
func spaceFor(workspaceID, target string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("multica-movein:"+workspaceID+":"+target))
}

func (m *mover) id(kind, oldID string) string {
	return uuid.NewSHA1(m.space, []byte(kind+"/"+oldID)).String()
}

// prepare resolves both workspaces and loads the old side's names.
func (m *mover) prepare(ctx context.Context, fromURL, fromWorkspace, toWorkspace, asEmail string) error {
	var spaces []oldWorkspace
	if err := m.old.list(ctx, "/api/workspaces", "workspaces", &spaces); err != nil {
		return fmt.Errorf("list old workspaces: %w", err)
	}
	for _, w := range spaces {
		if w.ID == fromWorkspace || w.Slug == fromWorkspace {
			m.oldWorkspace = w.ID
		}
	}
	if m.oldWorkspace == "" {
		return fmt.Errorf("old workspace %q not found", fromWorkspace)
	}
	m.old.workspace = m.oldWorkspace
	m.target = mirror.TargetKey(fromURL, m.oldWorkspace)

	err := m.pool.QueryRow(ctx,
		`SELECT id::text FROM workspace WHERE slug = $1 OR id::text = $1`, toWorkspace).Scan(&m.workspaceID)
	if err != nil {
		return fmt.Errorf("local workspace %q: %w", toWorkspace, err)
	}
	m.space = spaceFor(m.workspaceID, m.target)

	if asEmail != "" {
		err = m.pool.QueryRow(ctx, `
			SELECT u.id::text FROM "user" u JOIN member mb ON mb.user_id = u.id
			WHERE lower(u.email) = lower($1) AND mb.workspace_id = $2`, asEmail, m.workspaceID).Scan(&m.userID)
	} else {
		err = m.pool.QueryRow(ctx, `
			SELECT user_id::text FROM member WHERE workspace_id = $1 AND role = 'owner'
			ORDER BY created_at LIMIT 1`, m.workspaceID).Scan(&m.userID)
	}
	if err != nil {
		return fmt.Errorf("local member to write as: %w", err)
	}

	var me struct {
		ID string `json:"id"`
	}
	if err := m.old.get(ctx, "/api/me", &me); err != nil {
		return fmt.Errorf("old /api/me: %w", err)
	}
	m.oldSelf = me.ID

	var members []oldMember
	if err := m.old.list(ctx, "/api/workspaces/"+m.oldWorkspace+"/members", "members", &members); err != nil {
		return fmt.Errorf("old members: %w", err)
	}
	m.oldMembers = map[string]string{}
	for _, mb := range members {
		m.oldMembers[mb.UserID] = mb.Name
	}
	var agents []oldAgent
	if err := m.old.list(ctx, "/api/agents?include_archived=true", "agents", &agents); err != nil {
		return fmt.Errorf("old agents: %w", err)
	}
	m.oldAgents = map[string]oldAgent{}
	for _, a := range agents {
		m.oldAgents[a.ID] = a
	}
	var runtimes []oldRuntime
	if err := m.old.list(ctx, "/api/runtimes", "runtimes", &runtimes); err != nil {
		// Runtimes only steer which local member takes an old chat.
		m.log.Warn("old runtimes unavailable; chats go to members by name", "error", err)
	}
	m.oldRuntimes = map[string]string{}
	for _, r := range runtimes {
		m.oldRuntimes[r.ID] = r.Provider
	}
	return nil
}

func (m *mover) run(ctx context.Context) error {
	// Issues are listed first so project memories can point at them.
	all, err := m.listIssues(ctx)
	if err != nil {
		return err
	}
	if err := m.moveProjects(ctx); err != nil {
		return err
	}
	if err := m.moveIssues(ctx, all); err != nil {
		return err
	}
	return m.moveChats(ctx)
}

// ---- projects ----

func (m *mover) moveProjects(ctx context.Context) error {
	var list []oldProject
	if err := m.old.list(ctx, "/api/projects", "projects", &list); err != nil {
		return fmt.Errorf("old projects: %w", err)
	}
	m.projects = map[string]bool{}
	for _, p := range list {
		m.projects[p.ID] = true
	}
	for _, p := range list {
		var resources []oldResource
		if err := m.old.list(ctx, "/api/projects/"+p.ID+"/resources", "resources", &resources); err != nil {
			return fmt.Errorf("old project %s resources: %w", p.ID, err)
		}
		if err := m.inTx(ctx, func(tx pgx.Tx) error { return m.writeProject(ctx, tx, p, resources) }); err != nil {
			return fmt.Errorf("project %q: %w", p.Title, err)
		}
	}
	return nil
}

func (m *mover) writeProject(ctx context.Context, tx pgx.Tx, p oldProject, resources []oldResource) error {
	localID := m.id(mirror.KindProject, p.ID)
	m.stats.Projects++
	tag, err := tx.Exec(ctx, `
		INSERT INTO project (id, workspace_id, title, description, icon, status, priority,
		                     start_date, due_date, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::date, $9::date, $10, $11)
		ON CONFLICT (id) DO NOTHING`,
		localID, m.workspaceID, p.Title, m.text(deref(p.Description)), nullable(deref(p.Icon)),
		oneOf(p.Status, "planned", "planned", "in_progress", "paused", "completed", "cancelled"),
		oneOf(p.Priority, "none", "urgent", "high", "medium", "low", "none"),
		nullable(day(p.StartDate)), nullable(day(p.DueDate)), stamp(p.CreatedAt), stamp(p.UpdatedAt))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	m.stats.ProjectsNew++
	for _, r := range resources {
		_, err := tx.Exec(ctx, `
			INSERT INTO project_resource (id, project_id, workspace_id, resource_type, resource_ref,
			                              label, position, created_at, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (id) DO NOTHING`,
			m.id(kindResource, r.ID), localID, m.workspaceID, r.ResourceType, []byte(r.ResourceRef),
			r.Label, r.Position, stamp(r.CreatedAt), m.userID)
		if err != nil {
			return err
		}
		m.stats.Resources++
	}
	return m.link(ctx, tx, mirror.KindProject, localID, p.ID)
}

// ---- issues ----

func (m *mover) listIssues(ctx context.Context) ([]oldIssue, error) {
	var all []oldIssue
	seen := map[string]bool{}
	total := 0
	for offset := 0; ; offset += 100 {
		var page struct {
			Issues []oldIssue `json:"issues"`
			Total  int        `json:"total"`
		}
		path := "/api/issues?sort=created_at&direction=asc&limit=100&offset=" + strconv.Itoa(offset)
		if err := m.old.get(ctx, path, &page); err != nil {
			return nil, fmt.Errorf("old issues: %w", err)
		}
		total = page.Total
		for _, is := range page.Issues {
			if !seen[is.ID] {
				seen[is.ID] = true
				all = append(all, is)
			}
		}
		if len(page.Issues) < 100 {
			break
		}
	}
	m.issues = seen
	m.stats.OldIssueTotal = total
	m.log.Info("old issues listed", "count", len(all), "total", total)
	return all, nil
}

func (m *mover) moveIssues(ctx context.Context, all []oldIssue) error {

	// A child refers to its parent, so issues go in level by level: roots
	// first, then their children, and so on.
	byID := map[string]oldIssue{}
	for _, is := range all {
		byID[is.ID] = is
	}
	depth := func(is oldIssue) int {
		d := 0
		for p := deref(is.ParentIssueID); m.issues[p] && d <= len(all); p = deref(byID[p].ParentIssueID) {
			d++
		}
		return d
	}
	var levels [][]oldIssue
	for _, is := range all {
		d := depth(is)
		for len(levels) <= d {
			levels = append(levels, nil)
		}
		levels[d] = append(levels[d], is)
	}
	done := 0
	for _, level := range levels {
		if err := m.moveLevel(ctx, level); err != nil {
			return err
		}
		done += len(level)
		m.log.Info("issues", "done", done, "of", len(all))
	}
	return nil
}

// moveLevel moves issues that do not depend on each other, a few at a time.
func (m *mover) moveLevel(ctx context.Context, level []oldIssue) error {
	jobs := make(chan oldIssue)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for is := range jobs {
				if err := m.moveIssue(ctx, is); err != nil {
					errs <- fmt.Errorf("issue %s: %w", is.Identifier, err)
					return
				}
			}
		}()
	}
	var failed error
feed:
	for _, is := range level {
		select {
		case jobs <- is:
		case failed = <-errs:
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	close(errs)
	if failed == nil {
		failed = <-errs
	}
	return failed
}

func (m *mover) moveIssue(ctx context.Context, is oldIssue) error {
	localID := m.id(mirror.KindIssue, is.ID)
	var exists bool
	if err := m.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issue WHERE id = $1)`, localID).Scan(&exists); err != nil {
		return err
	}
	m.count(func(s *stats) { s.Issues++ })
	if exists {
		return nil
	}
	comments, err := m.oldComments(ctx, is.ID)
	if err != nil {
		return err
	}
	return m.inTx(ctx, func(tx pgx.Tx) error {
		number, err := m.number(ctx, tx, is.Number)
		if err != nil {
			return err
		}
		status := "done"
		if is.Status == "cancelled" || is.StatusCategory == "cancelled" {
			status = "cancelled"
		}
		position := float64(is.Number)
		if is.Position != nil {
			position = *is.Position
		}
		var parent, project any
		if p := deref(is.ParentIssueID); m.issues[p] {
			parent = m.id(mirror.KindIssue, p)
		}
		if p := deref(is.ProjectID); m.projects[p] {
			project = m.id(mirror.KindProject, p)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO issue (id, workspace_id, number, title, description, status, priority,
			                   creator_type, creator_id, parent_issue_id, project_id, position,
			                   created_at, updated_at, last_activity_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'member', $8, $9, $10, $11, $12, $13, $13)`,
			localID, m.workspaceID, number, is.Title, m.text(deref(is.Description))+m.record(is), status,
			oneOf(is.Priority, "none", "urgent", "high", "medium", "low", "none"),
			m.userID, parent, project, position, stamp(is.CreatedAt), stamp(is.UpdatedAt))
		if err != nil {
			return err
		}
		if err := m.link(ctx, tx, mirror.KindIssue, localID, is.ID); err != nil {
			return err
		}
		kept := map[string]bool{}
		for _, c := range comments {
			kept[c.ID] = true
		}
		for _, c := range comments {
			commentID := m.id(mirror.KindComment, c.ID)
			var parentComment any
			if p := deref(c.ParentID); kept[p] {
				parentComment = m.id(mirror.KindComment, p)
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO comment (id, issue_id, workspace_id, author_type, author_id, content, type,
				                     parent_id, created_at, updated_at)
				VALUES ($1, $2, $3, 'member', $4, $5, $6, $7, $8, $9)
				ON CONFLICT (id) DO NOTHING`,
				commentID, localID, m.workspaceID, m.userID, m.speaker(c)+m.text(c.Content),
				oneOf(c.Type, "comment", "comment", "status_change", "progress_update", "system"),
				parentComment, stamp(c.CreatedAt), stamp(firstNonEmpty(c.UpdatedAt, c.CreatedAt)))
			if err != nil {
				return err
			}
			if err := m.link(ctx, tx, mirror.KindComment, commentID, c.ID); err != nil {
				return err
			}
		}
		m.count(func(s *stats) { s.IssuesNew++; s.Comments += len(comments) })
		return nil
	})
}

// commentCap is the old server's per-request comment limit; a full page is
// followed by a request for what came after its newest comment.
const commentCap = 2000

func (m *mover) oldComments(ctx context.Context, issueID string) ([]oldComment, error) {
	var out []oldComment
	seen := map[string]bool{}
	since := ""
	for {
		path := "/api/issues/" + issueID + "/comments"
		if since != "" {
			path += "?since=" + url.QueryEscape(since)
		}
		var page []oldComment
		if err := m.old.get(ctx, path, &page); err != nil {
			return nil, fmt.Errorf("comments: %w", err)
		}
		added := 0
		for _, c := range page {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			added++
			if c.DeletedAt == nil {
				out = append(out, c)
			}
			if c.CreatedAt > since {
				since = c.CreatedAt
			}
		}
		if len(page) < commentCap || added == 0 {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return parentsFirst(out), nil
}

// parentsFirst orders comments so every reply follows the comment it answers,
// which can carry a later timestamp. A reply whose parent is gone is a root.
func parentsFirst(cs []oldComment) []oldComment {
	present := map[string]bool{}
	for _, c := range cs {
		present[c.ID] = true
	}
	placed := map[string]bool{}
	out := make([]oldComment, 0, len(cs))
	for len(out) < len(cs) {
		before := len(out)
		for _, c := range cs {
			p := deref(c.ParentID)
			if !placed[c.ID] && (!present[p] || placed[p]) {
				placed[c.ID] = true
				out = append(out, c)
			}
		}
		if len(out) == before {
			// A parent loop: place the rest as roots.
			for _, c := range cs {
				if !placed[c.ID] {
					placed[c.ID] = true
					c.ParentID = nil
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// number keeps the old issue number when it is free here, so references like
// DENE-12 still point at the same issue; otherwise it takes the next one.
func (m *mover) number(ctx context.Context, tx pgx.Tx, want int32) (int32, error) {
	var counter int32
	if err := tx.QueryRow(ctx, `SELECT issue_counter FROM workspace WHERE id = $1 FOR UPDATE`, m.workspaceID).Scan(&counter); err != nil {
		return 0, err
	}
	free := func(n int32) (bool, error) {
		var taken bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issue WHERE workspace_id = $1 AND number = $2)`, m.workspaceID, n).Scan(&taken)
		return !taken, err
	}
	n := want
	ok := false
	if n > 0 {
		var err error
		if ok, err = free(n); err != nil {
			return 0, err
		}
	}
	if !ok {
		for n = counter + 1; ; n++ {
			if ok, err := free(n); err != nil {
				return 0, err
			} else if ok {
				break
			}
		}
	}
	_, err := tx.Exec(ctx, `UPDATE workspace SET issue_counter = GREATEST(issue_counter, $2) WHERE id = $1`, m.workspaceID, n)
	return n, err
}

var statusLabel = map[string]string{
	"backlog": "待规划", "todo": "待办", "in_progress": "进行中", "in_review": "待审核",
	"done": "已完成", "blocked": "受阻", "cancelled": "已取消",
}

// record is the footer that keeps what the move changed: the old number,
// who held the issue and the status it had.
func (m *mover) record(is oldIssue) string {
	holder := "无"
	switch deref(is.AssigneeType) {
	case "member":
		holder = firstNonEmpty(m.oldMembers[deref(is.AssigneeID)], "成员")
	case "agent":
		holder = firstNonEmpty(m.oldAgents[deref(is.AssigneeID)].Name, "智能体")
	case "squad":
		holder = "小队"
	}
	status := firstNonEmpty(statusLabel[is.Status], is.StatusName, is.Status)
	return fmt.Sprintf("\n\n---\n搬家记录：原编号 %s · 原执行人 %s · 原状态 %s", is.Identifier, holder, status)
}

// speaker names who wrote an old comment, since every moved comment is
// written as the local member. Kun's own comments and system rows need none.
func (m *mover) speaker(c oldComment) string {
	var name string
	switch c.AuthorType {
	case "member":
		if c.AuthorID == m.oldSelf {
			return ""
		}
		name = m.oldMembers[c.AuthorID]
	case "agent":
		name = m.oldAgents[c.AuthorID].Name
	default:
		return ""
	}
	return firstNonEmpty(name, "旧办公室") + "："
}

// ---- chats ----

type localAgent struct {
	id, name, provider string
}

func (m *mover) moveChats(ctx context.Context) error {
	var sessions []oldChat
	if err := m.old.list(ctx, "/api/chat/sessions?status=all", "sessions", &sessions); err != nil {
		return fmt.Errorf("old chats: %w", err)
	}
	if len(sessions) == 0 {
		return nil
	}
	agents, err := m.localAgents(ctx)
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		return errors.New("the local workspace has no agents yet; old chats need one to belong to")
	}
	for _, s := range sessions {
		if err := m.moveChat(ctx, s, agents); err != nil {
			return fmt.Errorf("chat %q: %w", s.Title, err)
		}
	}
	return nil
}

func (m *mover) localAgents(ctx context.Context) ([]localAgent, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT a.id::text, a.name, COALESCE(r.provider, '')
		FROM agent a LEFT JOIN agent_runtime r ON r.id = a.runtime_id
		WHERE a.workspace_id = $1 AND a.archived_at IS NULL
		ORDER BY a.created_at`, m.workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []localAgent
	for rows.Next() {
		var a localAgent
		if err := rows.Scan(&a.id, &a.name, &a.provider); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// chatAgent picks who takes an old chat here: the agent with the same name,
// else the first one on the same coding CLI, else the first agent.
func (m *mover) chatAgent(oldAgentID string, agents []localAgent) string {
	old := m.oldAgents[oldAgentID]
	for _, a := range agents {
		if old.Name != "" && a.name == old.Name {
			return a.id
		}
	}
	if provider := m.oldRuntimes[old.RuntimeID]; provider != "" {
		for _, a := range agents {
			if a.provider == provider {
				return a.id
			}
		}
	}
	return agents[0].id
}

func (m *mover) moveChat(ctx context.Context, s oldChat, agents []localAgent) error {
	localID := m.id(kindChat, s.ID)
	var exists bool
	if err := m.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM chat_session WHERE id = $1)`, localID).Scan(&exists); err != nil {
		return err
	}
	m.stats.Chats++
	if exists {
		return nil
	}
	var messages []oldMessage
	if err := m.old.list(ctx, "/api/chat/sessions/"+s.ID+"/messages", "messages", &messages); err != nil {
		return fmt.Errorf("messages: %w", err)
	}
	var project any
	if p := deref(s.ProjectID); m.projects[p] {
		project = m.id(mirror.KindProject, p)
	}
	return m.inTx(ctx, func(tx pgx.Tx) error {
		// Read up to now: nothing moved in counts as unread.
		_, err := tx.Exec(ctx, `
			INSERT INTO chat_session (id, workspace_id, agent_id, creator_id, title, status, project_id,
			                          created_at, updated_at, explicitly_created_at, last_read_at, unread_since)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $8, now(), NULL)`,
			localID, m.workspaceID, m.chatAgent(s.AgentID, agents), m.userID, s.Title,
			oneOf(s.Status, "archived", "active", "archived"), project, stamp(s.CreatedAt), stamp(s.UpdatedAt))
		if err != nil {
			return err
		}
		n := 0
		for _, msg := range messages {
			if msg.Role != "user" && msg.Role != "assistant" {
				continue
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO chat_message (id, chat_session_id, role, content, created_at, failure_reason, elapsed_ms)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (id) DO NOTHING`,
				m.id(kindMessage, msg.ID), localID, msg.Role, m.text(msg.Content), stamp(msg.CreatedAt),
				msg.FailureReason, msg.ElapsedMs)
			if err != nil {
				return err
			}
			n++
		}
		// Messages keep their old times; the read cursor must be past them.
		if _, err := tx.Exec(ctx, `
			UPDATE chat_session SET last_read_at = GREATEST(now(),
			    (SELECT max(created_at) FROM chat_message WHERE chat_session_id = $1))
			WHERE id = $1`, localID); err != nil {
			return err
		}
		m.stats.ChatsNew++
		m.stats.Messages += n
		return nil
	})
}

// ---- helpers ----

func (m *mover) link(ctx context.Context, tx pgx.Tx, kind, localID, oldID string) error {
	// Inbound with a zero version: the mirror never writes over the old
	// original, and never takes the old status back over the local one.
	_, err := tx.Exec(ctx, `
		INSERT INTO mirror_link (target, kind, local_id, remote_id, source_updated_at, inbound)
		VALUES ($1, $2, $3, $4, $5, true)
		ON CONFLICT (target, kind, local_id) DO NOTHING`,
		m.target, kind, localID, oldID, time.Time{})
	return err
}

func (m *mover) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, m.pool, fn)
}

func (m *mover) count(fn func(*stats)) {
	m.mu.Lock()
	fn(&m.stats)
	m.mu.Unlock()
}

// mentionParts matches [label](mention://kind/id).
var mentionParts = regexp.MustCompile(`\[([^\]]*)\]\(mention://([a-z_]+)/([^)\s]*)\)`)

// text points issue and project mentions at their moved copies. Every other
// mention names someone who did not move, so it keeps only its label.
func (m *mover) text(s string) string {
	if !strings.Contains(s, "mention://") {
		return s
	}
	return mentionParts.ReplaceAllStringFunc(s, func(match string) string {
		p := mentionParts.FindStringSubmatch(match)
		label, kind, id := p[1], p[2], p[3]
		switch {
		case kind == "issue" && m.issues[id]:
			return "[" + label + "](mention://issue/" + m.id(mirror.KindIssue, id) + ")"
		case kind == "project" && m.projects[id]:
			return "[" + label + "](mention://project/" + m.id(mirror.KindProject, id) + ")"
		}
		return label
	})
}

// stamp parses an old timestamp; a missing one becomes now.
func stamp(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Now()
}

func day(s *string) string {
	d := deref(s)
	if len(d) >= 10 {
		return d[:10]
	}
	return ""
}

func oneOf(v, fallback string, allowed ...string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return fallback
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
