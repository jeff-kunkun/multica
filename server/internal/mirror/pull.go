package mirror

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The pull brings back what people do on the target: comments on the copies,
// issues they create there or hand to Kun or one of Kun's agents, and status
// changes on the copies. Everything lands through the local server's public
// API as the member whose token EnablePull got, so a pulled comment wakes the
// issue's assignee exactly as a comment typed here would.
//
// Echo control is mirror_link. A pulled comment is linked with Inbound set, so
// the forward pass never writes it back; anything whose remote id is already
// linked — every copy the forward pass made — is never pulled; and nothing
// the target assignee (the token's own member there) wrote is pulled.
//
// The target is read through its issue list sorted by updated_at, which a new
// comment also bumps. The position is a mirror_link row of kind cursor, so a
// restart resumes; the first pull only records where the target is now.

const (
	kindCursor = "cursor"
	cursorID   = "00000000-0000-0000-0000-000000000000"
	// Re-read this much before the cursor: a row's updated_at is taken when
	// its transaction starts, so it can commit after a later one was read.
	// Re-reading is safe because every pulled object is linked.
	pullOverlap  = time.Minute
	pullPageSize = 100
	pullMaxPages = 20
)

// EnablePull turns on the pull. local is this server's API as the member that
// pulled objects are posted as; targetWorkspace is the target workspace id.
func (m *Mirror) EnablePull(local Remote, targetWorkspace string) {
	m.local = local
	m.targetWorkspace = targetWorkspace
}

type remoteIssue struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Description   *string `json:"description"`
	Status        string  `json:"status"`
	Priority      string  `json:"priority"`
	AssigneeType  *string `json:"assignee_type"`
	AssigneeID    *string `json:"assignee_id"`
	CreatorType   string  `json:"creator_type"`
	CreatorID     string  `json:"creator_id"`
	ParentIssueID *string `json:"parent_issue_id"`
	ProjectID     *string `json:"project_id"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

type remoteComment struct {
	ID         string  `json:"id"`
	AuthorType string  `json:"author_type"`
	AuthorID   string  `json:"author_id"`
	Content    string  `json:"content"`
	Type       string  `json:"type"`
	ParentID   *string `json:"parent_id"`
	CreatedAt  string  `json:"created_at"`
	DeletedAt  *string `json:"deleted_at"`
}

// pullRun is one pull: reverse indexes of the links and the name lookups,
// each loaded on first use.
type pullRun struct {
	m    *Mirror
	pass *pass

	byRemote map[string]map[string]string // kind -> remote id -> local id

	targetMembers map[string]string // user id -> name
	targetAgents  map[string]string // agent id -> name
	localAgents   map[string]string // name -> agent id
	localSelf     string
}

func (m *Mirror) pull(ctx context.Context, p *pass) error {
	run := &pullRun{m: m, pass: p, byRemote: map[string]map[string]string{}}
	for kind, links := range m.links {
		idx := map[string]string{}
		for _, l := range links {
			idx[l.RemoteID] = l.LocalID
		}
		run.byRemote[kind] = idx
	}

	if m.cursor == nil {
		// First pull: start from the newest change on the target, so
		// nothing that predates the mirror is pulled.
		var page struct {
			Issues []remoteIssue `json:"issues"`
		}
		if err := m.remote.Get(ctx, "/api/issues?sort=updated_at&direction=desc&limit=1", &page); err != nil {
			return err
		}
		at := time.Now()
		if len(page.Issues) > 0 {
			if t, err := parseTime(page.Issues[0].UpdatedAt); err == nil {
				at = t
			}
		}
		return m.saveCursor(ctx, at)
	}

	since := m.cursor.SourceUpdatedAt.Add(-pullOverlap)
	issues, err := run.changedIssues(ctx, since)
	if err != nil {
		return err
	}
	// Oldest first, so a parent created on the target before its child is
	// linked by the time the child is pulled.
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].at.Before(issues[j].at) })

	next := m.cursor.SourceUpdatedAt
	for _, ci := range issues {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := run.issue(ctx, ci.remoteIssue, since); err != nil {
			m.log.Warn("mirror: pull failed", "remote_issue", ci.ID, "error", err)
			if !refused(err) {
				// Come back to it next pass.
				break
			}
		}
		if ci.at.After(next) {
			next = ci.at
		}
	}
	if next.After(m.cursor.SourceUpdatedAt) {
		return m.saveCursor(ctx, next)
	}
	return nil
}

func (m *Mirror) saveCursor(ctx context.Context, at time.Time) error {
	l := Link{Kind: kindCursor, LocalID: cursorID, RemoteID: "", SourceUpdatedAt: at}
	if err := m.src.SaveLink(ctx, l); err != nil {
		return err
	}
	m.cursor = &l
	return nil
}

type changedIssue struct {
	remoteIssue
	at time.Time
}

// changedIssues lists target issues updated after since, newest first.
func (r *pullRun) changedIssues(ctx context.Context, since time.Time) ([]changedIssue, error) {
	var out []changedIssue
	for page := 0; page < pullMaxPages; page++ {
		var resp struct {
			Issues []remoteIssue `json:"issues"`
		}
		path := "/api/issues?sort=updated_at&direction=desc&limit=" + strconv.Itoa(pullPageSize) +
			"&offset=" + strconv.Itoa(page*pullPageSize)
		if err := r.m.remote.Get(ctx, path, &resp); err != nil {
			return nil, err
		}
		for _, is := range resp.Issues {
			at, err := parseTime(is.UpdatedAt)
			if err != nil {
				return nil, fmt.Errorf("issue %s updated_at: %w", is.ID, err)
			}
			if !at.After(since) {
				return out, nil
			}
			out = append(out, changedIssue{is, at})
		}
		if len(resp.Issues) < pullPageSize {
			break
		}
	}
	return out, nil
}

// issue pulls one changed target issue.
func (r *pullRun) issue(ctx context.Context, ri remoteIssue, since time.Time) error {
	if localID, ok := r.byRemote[KindIssue][ri.ID]; ok {
		if err := r.status(ctx, localID, ri); err != nil {
			return err
		}
		return r.comments(ctx, localID, ri.ID, since)
	}
	if !r.wanted(ri, since) {
		return nil
	}
	localID, err := r.newIssue(ctx, ri.ID)
	if err != nil {
		return err
	}
	return r.comments(ctx, localID, ri.ID, time.Time{})
}

// status takes a status set on the target copy. It only applies when the
// forward pass had nothing newer to say about the issue: otherwise the local
// change wins and has just been written over the target.
func (r *pullRun) status(ctx context.Context, localID string, ri remoteIssue) error {
	link := r.m.links[KindIssue][localID]
	version, live := r.pass.versions[KindIssue][localID]
	if !live || !link.SourceUpdatedAt.Equal(version) {
		return nil
	}
	if _, written := r.pass.tried[KindIssue+"/"+localID]; written {
		// This pass wrote (or failed to write) the local state out; what
		// the target showed when listed is older.
		return nil
	}
	is, err := r.m.src.Issue(ctx, localID)
	if err != nil {
		return err
	}
	if is.Status == ri.Status {
		return nil
	}
	return r.m.local.Update(ctx, "/api/issues/"+localID, map[string]any{"status": ri.Status})
}

// wanted reports whether an unlinked target issue belongs here: one created
// since the last pull, or an open one held by Kun or an agent. Kun's own
// issues there predate the office and stay.
func (r *pullRun) wanted(ri remoteIssue, since time.Time) bool {
	if ri.CreatorType == "member" && ri.CreatorID == r.m.assignee {
		return false
	}
	if ri.Status == "done" || ri.Status == "cancelled" {
		return false
	}
	if created, err := parseTime(ri.CreatedAt); err == nil && created.After(since) {
		return true
	}
	switch deref(ri.AssigneeType) {
	case "agent":
		return true
	case "member":
		return deref(ri.AssigneeID) == r.m.assignee
	}
	return false
}

func (r *pullRun) newIssue(ctx context.Context, remoteID string) (string, error) {
	var ri remoteIssue
	if err := r.m.remote.Get(ctx, "/api/issues/"+remoteID, &ri); err != nil {
		return "", err
	}
	assigneeType, assigneeID, err := r.localAssignee(ctx, ri)
	if err != nil {
		return "", err
	}
	description, err := r.localMentions(ctx, deref(ri.Description))
	if err != nil {
		return "", err
	}
	status := ri.Status
	if !standardStatus[status] {
		status = "todo"
	}
	body := map[string]any{
		"title":           ri.Title,
		"description":     description,
		"status":          status,
		"priority":        ri.Priority,
		"assignee_type":   assigneeType,
		"assignee_id":     assigneeID,
		"parent_issue_id": nullable(r.byRemote[KindIssue][deref(ri.ParentIssueID)]),
		"project_id":      nullable(r.byRemote[KindProject][deref(ri.ProjectID)]),
	}
	localID, err := r.m.local.Create(ctx, "/api/issues", body)
	if err != nil {
		return "", err
	}
	// A zero version makes the next forward pass write the local state over
	// the original, which also takes it out of the target's routing.
	l := Link{Kind: KindIssue, LocalID: localID, RemoteID: ri.ID}
	if err := r.m.src.SaveLink(ctx, l); err != nil {
		return "", err
	}
	r.m.links[KindIssue][localID] = l
	r.byRemote[KindIssue][ri.ID] = localID
	return localID, nil
}

var standardStatus = map[string]bool{
	"backlog": true, "todo": true, "in_progress": true, "in_review": true,
	"done": true, "blocked": true, "cancelled": true,
}

// localAssignee keeps an agent assignee when an agent of the same name exists
// here; anything else goes to the local member pulls post as.
func (r *pullRun) localAssignee(ctx context.Context, ri remoteIssue) (string, string, error) {
	if deref(ri.AssigneeType) == "agent" {
		targetAgents, err := r.targetAgentNames(ctx)
		if err != nil {
			return "", "", err
		}
		localAgents, err := r.localAgentIDs(ctx)
		if err != nil {
			return "", "", err
		}
		if id, ok := localAgents[targetAgents[deref(ri.AssigneeID)]]; ok {
			return "agent", id, nil
		}
	}
	self, err := r.self(ctx)
	if err != nil {
		return "", "", err
	}
	return "member", self, nil
}

// comments pulls the target comments of one issue created after since.
func (r *pullRun) comments(ctx context.Context, localIssue, remoteIssue string, since time.Time) error {
	path := "/api/issues/" + remoteIssue + "/comments"
	if !since.IsZero() {
		path += "?since=" + url.QueryEscape(since.UTC().Format(time.RFC3339Nano))
	}
	var list []remoteComment
	if err := r.m.remote.Get(ctx, path, &list); err != nil {
		return err
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].CreatedAt < list[j].CreatedAt })
	for _, rc := range list {
		if _, linked := r.byRemote[KindComment][rc.ID]; linked {
			continue
		}
		if rc.Type != "comment" || rc.DeletedAt != nil || strings.TrimSpace(rc.Content) == "" {
			continue
		}
		if rc.AuthorType == "member" && rc.AuthorID == r.m.assignee {
			continue
		}
		if err := r.comment(ctx, localIssue, rc); err != nil {
			return err
		}
	}
	return nil
}

func (r *pullRun) comment(ctx context.Context, localIssue string, rc remoteComment) error {
	speaker, err := r.speaker(ctx, rc)
	if err != nil {
		return err
	}
	content, err := r.localMentions(ctx, rc.Content)
	if err != nil {
		return err
	}
	body := map[string]any{"content": speaker + "：" + content, "type": "comment"}
	if parent := r.byRemote[KindComment][deref(rc.ParentID)]; parent != "" {
		body["parent_id"] = parent
	}
	localID, err := r.m.local.Create(ctx, "/api/issues/"+localIssue+"/comments", body)
	if err != nil {
		return err
	}
	l := Link{Kind: KindComment, LocalID: localID, RemoteID: rc.ID, SourceUpdatedAt: time.Now(), Inbound: true}
	if err := r.m.src.SaveLink(ctx, l); err != nil {
		return err
	}
	r.m.links[KindComment][localID] = l
	r.byRemote[KindComment][rc.ID] = localID
	return nil
}

func (r *pullRun) speaker(ctx context.Context, rc remoteComment) (string, error) {
	var name string
	switch rc.AuthorType {
	case "member":
		members, err := r.targetMemberNames(ctx)
		if err != nil {
			return "", err
		}
		name = members[rc.AuthorID]
	case "agent":
		agents, err := r.targetAgentNames(ctx)
		if err != nil {
			return "", err
		}
		name = agents[rc.AuthorID]
	}
	if name == "" {
		name = "旧办公室"
	}
	return name, nil
}

// mentionParts matches [label](mention://kind/id) with its parts.
var mentionParts = regexp.MustCompile(`\[([^\]]*)\]\(mention://([a-z_]+)/([^)\s]*)\)`)

// localMentions rewrites target mentions for this server: an agent mention
// points at the local agent of the same name, so that agent takes it here;
// every other mention becomes its label.
func (r *pullRun) localMentions(ctx context.Context, s string) (string, error) {
	if !strings.Contains(s, "mention://agent/") {
		return plainMentions(s), nil
	}
	targetAgents, err := r.targetAgentNames(ctx)
	if err != nil {
		return "", err
	}
	localAgents, err := r.localAgentIDs(ctx)
	if err != nil {
		return "", err
	}
	return mentionParts.ReplaceAllStringFunc(s, func(match string) string {
		p := mentionParts.FindStringSubmatch(match)
		label, kind, id := p[1], p[2], p[3]
		if kind == "agent" {
			name := targetAgents[id]
			if name == "" {
				name = strings.TrimPrefix(label, "@")
			}
			if localID, ok := localAgents[name]; ok {
				return "[@" + name + "](mention://agent/" + localID + ")"
			}
		}
		return label
	}), nil
}

type named struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

func (r *pullRun) targetMemberNames(ctx context.Context) (map[string]string, error) {
	if r.targetMembers == nil {
		var list []named
		if err := r.m.remote.Get(ctx, "/api/workspaces/"+r.m.targetWorkspace+"/members", &list); err != nil {
			return nil, err
		}
		r.targetMembers = map[string]string{}
		for _, n := range list {
			r.targetMembers[n.UserID] = n.Name
		}
	}
	return r.targetMembers, nil
}

func (r *pullRun) targetAgentNames(ctx context.Context) (map[string]string, error) {
	if r.targetAgents == nil {
		var list []named
		if err := r.m.remote.Get(ctx, "/api/agents?include_archived=true", &list); err != nil {
			return nil, err
		}
		r.targetAgents = map[string]string{}
		for _, n := range list {
			r.targetAgents[n.ID] = n.Name
		}
	}
	return r.targetAgents, nil
}

func (r *pullRun) localAgentIDs(ctx context.Context) (map[string]string, error) {
	if r.localAgents == nil {
		var list []named
		if err := r.m.local.Get(ctx, "/api/agents", &list); err != nil {
			return nil, err
		}
		r.localAgents = map[string]string{}
		for _, n := range list {
			r.localAgents[n.Name] = n.ID
		}
	}
	return r.localAgents, nil
}

func (r *pullRun) self(ctx context.Context) (string, error) {
	if r.localSelf == "" {
		var me named
		if err := r.m.local.Get(ctx, "/api/me", &me); err != nil {
			return "", err
		}
		if me.ID == "" {
			return "", errors.New("local /api/me has no id")
		}
		r.localSelf = me.ID
	}
	return r.localSelf, nil
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
