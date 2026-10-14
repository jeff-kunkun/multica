// Package mirror keeps one workspace and a workspace on another Multica
// server in step through both servers' public APIs. Out: projects, issues and
// comments are copied to the target. Back (pull.go, when enabled): what
// people do on the target — comments on the copies, issues they create or
// hand to Kun there, status changes — lands in the local workspace.
//
// It is a single reconcile loop. Every tick it lists the local objects with
// their updated_at, compares them with what was last copied (mirror_link), and
// creates, updates or retires the remote copies that differ, then pulls what
// changed on the target since the last pass. Startup is the same tick with
// nothing in memory, so a missed change is caught on the next pass and a
// restart does not re-copy what is already there.
//
// Nothing outside this package and its wiring in cmd/server refers to it.
package mirror

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"
)

// Kinds of mirrored objects. They are the mirror_link.kind values.
const (
	KindProject = "project"
	KindIssue   = "issue"
	KindComment = "comment"
)

const defaultInterval = 20 * time.Second

// Config is read from the environment. The first five values are required;
// with any of them missing the mirror is off. LocalToken turns on the pull
// back from the target.
type Config struct {
	Workspace       string   // local workspace slug or id
	TargetURL       string   // e.g. https://ai.ferryway.cc
	TargetToken     string   // personal access token of a target member
	TargetWorkspace string   // target workspace id
	TargetAssignee  string   // target member id that holds every copied issue
	LocalURL        string   // this server's API, default http://127.0.0.1:$PORT
	LocalToken      string   // personal access token of the local member pulls post as
	Projects        []string // local project ids to copy; empty means all
	Interval        time.Duration
}

func ConfigFromEnv() (Config, bool) {
	c := Config{
		Workspace:       strings.TrimSpace(os.Getenv("MULTICA_MIRROR_WORKSPACE")),
		TargetURL:       strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_MIRROR_TARGET_URL")), "/"),
		TargetToken:     strings.TrimSpace(os.Getenv("MULTICA_MIRROR_TARGET_TOKEN")),
		TargetWorkspace: strings.TrimSpace(os.Getenv("MULTICA_MIRROR_TARGET_WORKSPACE")),
		TargetAssignee:  strings.TrimSpace(os.Getenv("MULTICA_MIRROR_TARGET_ASSIGNEE")),
		LocalURL:        strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_MIRROR_LOCAL_URL")), "/"),
		LocalToken:      strings.TrimSpace(os.Getenv("MULTICA_MIRROR_LOCAL_TOKEN")),
		Interval:        defaultInterval,
	}
	if c.LocalURL == "" {
		port := strings.TrimSpace(os.Getenv("PORT"))
		if port == "" {
			port = "8080"
		}
		c.LocalURL = "http://127.0.0.1:" + port
	}
	for _, id := range strings.Split(os.Getenv("MULTICA_MIRROR_PROJECTS"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			c.Projects = append(c.Projects, id)
		}
	}
	if d, err := time.ParseDuration(os.Getenv("MULTICA_MIRROR_INTERVAL")); err == nil && d > 0 {
		c.Interval = d
	}
	ok := c.Workspace != "" && c.TargetURL != "" && c.TargetToken != "" && c.TargetWorkspace != "" && c.TargetAssignee != ""
	return c, ok
}

// TargetKey names one target in mirror_link: the target server's URL and
// workspace id. Anything that writes links for a target uses it.
func TargetKey(targetURL, targetWorkspace string) string {
	return strings.TrimRight(targetURL, "/") + "#" + targetWorkspace
}

// Link is one mirror_link row. Inbound marks a local object pulled from the
// target: the remote one is the original, so it is never written to.
type Link struct {
	Kind            string
	LocalID         string
	RemoteID        string
	SourceUpdatedAt time.Time
	Inbound         bool
}

type Project struct {
	ID          string
	Title       string
	Description string
	Icon        string
	Status      string
	Priority    string
}

type Issue struct {
	ID          string
	Identifier  string // e.g. DENE-12
	Title       string
	Description string
	Status      string
	Priority    string
	ParentID    string
	ProjectID   string
	Assignee    string // display name; empty when unassigned
}

type Comment struct {
	ID        string
	IssueID   string
	ParentID  string
	Author    string // display name
	Engine    string // runtime provider when the author is an agent
	Content   string
	CreatedAt time.Time
}

// Source is the local side: what exists now, and the copy bookkeeping.
type Source interface {
	// Versions returns updated_at for every live object of the workspace,
	// keyed by kind and local id.
	Versions(ctx context.Context) (map[string]map[string]time.Time, error)
	Project(ctx context.Context, id string) (Project, error)
	Issue(ctx context.Context, id string) (Issue, error)
	Comment(ctx context.Context, id string) (Comment, error)
	Links(ctx context.Context) ([]Link, error)
	SaveLink(ctx context.Context, l Link) error
	DeleteLink(ctx context.Context, kind, localID string) error
}

// ErrRemoteNotFound means the remote copy is gone; the mirror recreates it.
var ErrRemoteNotFound = errors.New("remote object not found")

// Remote is a Multica server's API, reduced to what the mirror reads and
// writes.
type Remote interface {
	Get(ctx context.Context, path string, out any) error
	Create(ctx context.Context, path string, body map[string]any) (string, error)
	Update(ctx context.Context, path string, body map[string]any) error
	Delete(ctx context.Context, path string) error
}

type Mirror struct {
	src      Source
	remote   Remote
	assignee string
	interval time.Duration
	log      *slog.Logger

	// Set by EnablePull.
	local           Remote
	targetWorkspace string

	links  map[string]map[string]Link // kind -> local id -> link
	cursor *Link                      // pull position on the target, nil before the first pull
}

// New copies into the target as assignee, a target member id. Copies held by
// a member stay out of the target's routing, so they never start a run there.
func New(src Source, remote Remote, assignee string, interval time.Duration, log *slog.Logger) *Mirror {
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Mirror{src: src, remote: remote, assignee: assignee, interval: interval, log: log}
}

func (m *Mirror) Run(ctx context.Context) {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		if err := m.Sync(ctx); err != nil && ctx.Err() == nil {
			m.log.Warn("mirror: sync pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sync runs one reconcile pass. A failure on one object is logged and left
// for the next pass; only failures to read local state abort the pass.
func (m *Mirror) Sync(ctx context.Context) error {
	if m.links == nil {
		links, err := m.src.Links(ctx)
		if err != nil {
			return fmt.Errorf("load links: %w", err)
		}
		m.links = map[string]map[string]Link{KindProject: {}, KindIssue: {}, KindComment: {}}
		for _, l := range links {
			if l.Kind == kindCursor {
				c := l
				m.cursor = &c
			} else if m.links[l.Kind] != nil {
				m.links[l.Kind][l.LocalID] = l
			}
		}
	}
	versions, err := m.src.Versions(ctx)
	if err != nil {
		return fmt.Errorf("list versions: %w", err)
	}
	p := pass{m: m, versions: versions, tried: map[string]error{}}
	// Parents before children: projects, then issues, then comments. Each
	// ensure also pulls in the parent it references, so order inside a kind
	// does not matter.
	for _, kind := range []string{KindProject, KindIssue, KindComment} {
		for id := range versions[kind] {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if _, err := p.ensure(ctx, kind, id); err != nil {
				m.log.Warn("mirror: copy failed", "kind", kind, "id", id, "error", err)
			}
		}
	}
	// Comments are retired before issues: a removed issue takes its comments
	// with it locally, and the remote issue only becomes cancelled.
	for _, kind := range []string{KindComment, KindIssue, KindProject} {
		for id, l := range m.links[kind] {
			if _, live := versions[kind][id]; live {
				continue
			}
			if err := m.retire(ctx, l); err != nil {
				m.log.Warn("mirror: retire failed", "kind", kind, "id", id, "error", err)
			}
		}
	}
	if m.local != nil {
		if err := m.pull(ctx, &p); err != nil {
			m.log.Warn("mirror: pull failed", "error", err)
		}
	}
	return nil
}

type pass struct {
	m        *Mirror
	versions map[string]map[string]time.Time
	tried    map[string]error
}

// ensure brings one local object's remote copy up to date and returns the
// remote id. An object that is not live locally has no copy to point at.
func (p *pass) ensure(ctx context.Context, kind, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	m := p.m
	link, linked := m.links[kind][id]
	version, live := p.versions[kind][id]
	if !live {
		return "", nil
	}
	if linked && (link.Inbound || link.SourceUpdatedAt.Equal(version)) {
		return link.RemoteID, nil
	}
	key := kind + "/" + id
	if err, seen := p.tried[key]; seen {
		// Already failed this pass, or still in progress further up a
		// parent chain that loops back here.
		if err == nil {
			err = errors.New("reference cycle")
		}
		return "", err
	}
	p.tried[key] = nil
	remoteID, err := p.copy(ctx, kind, id, link, linked, version)
	if err != nil {
		p.tried[key] = err
	}
	return remoteID, err
}

func (p *pass) copy(ctx context.Context, kind, id string, link Link, linked bool, version time.Time) (string, error) {
	m := p.m

	var create, update string
	var body map[string]any
	switch kind {
	case KindProject:
		pr, err := m.src.Project(ctx, id)
		if err != nil {
			return "", err
		}
		create, body = "/api/projects", projectBody(pr)
		update = "/api/projects/" + link.RemoteID
	case KindIssue:
		is, err := m.src.Issue(ctx, id)
		if err != nil {
			return "", err
		}
		parent, err := p.ensure(ctx, KindIssue, is.ParentID)
		if err != nil {
			return "", fmt.Errorf("parent issue: %w", err)
		}
		project, err := p.ensure(ctx, KindProject, is.ProjectID)
		if err != nil {
			return "", fmt.Errorf("project: %w", err)
		}
		create, body = "/api/issues", issueBody(is, m.assignee, parent, project)
		update = "/api/issues/" + link.RemoteID
	case KindComment:
		c, err := m.src.Comment(ctx, id)
		if err != nil {
			return "", err
		}
		issue, err := p.ensure(ctx, KindIssue, c.IssueID)
		if err != nil {
			return "", fmt.Errorf("issue of comment: %w", err)
		}
		if issue == "" {
			return "", errors.New("issue of comment is not live")
		}
		parent, err := p.ensure(ctx, KindComment, c.ParentID)
		if err != nil {
			return "", fmt.Errorf("parent comment: %w", err)
		}
		body = commentBody(c)
		create = "/api/issues/" + issue + "/comments"
		update = "/api/comments/" + link.RemoteID
		if parent != "" {
			body["parent_id"] = parent
		}
	default:
		return "", fmt.Errorf("unknown kind %q", kind)
	}

	remoteID := link.RemoteID
	if linked {
		err := m.remote.Update(ctx, update, body)
		if errors.Is(err, ErrRemoteNotFound) {
			linked = false
		} else if err != nil {
			return "", err
		}
	}
	if !linked {
		if kind == KindComment {
			// A comment's parent and type are fixed at creation.
			body["type"] = "comment"
		}
		payload := body
		if kind == KindIssue {
			// Create parked in backlog, which the target never routes, and
			// set the real status and reviewer with the update right after.
			payload = map[string]any{"allow_duplicate": true}
			for k, v := range body {
				payload[k] = v
			}
			payload["status"] = "backlog"
		}
		created, err := m.remote.Create(ctx, create, payload)
		if err != nil {
			return "", err
		}
		remoteID = created
		if kind == KindIssue {
			if err := m.remote.Update(ctx, "/api/issues/"+created, body); err != nil {
				return "", err
			}
		}
	}
	l := Link{Kind: kind, LocalID: id, RemoteID: remoteID, SourceUpdatedAt: version}
	if err := m.src.SaveLink(ctx, l); err != nil {
		return "", err
	}
	m.links[kind][id] = l
	return remoteID, nil
}

// retire handles a local object that no longer exists. Removed issues and
// projects stay visible on the target as cancelled; removed comments go.
func (m *Mirror) retire(ctx context.Context, l Link) error {
	if l.Inbound {
		// The original belongs to someone on the target: leave it, and keep
		// the link so the pull does not bring it back.
		return nil
	}
	var err error
	switch l.Kind {
	case KindComment:
		err = m.remote.Delete(ctx, "/api/comments/"+l.RemoteID)
	case KindIssue:
		err = m.remote.Update(ctx, "/api/issues/"+l.RemoteID, map[string]any{"status": "cancelled"})
	case KindProject:
		err = m.remote.Update(ctx, "/api/projects/"+l.RemoteID, map[string]any{"status": "cancelled"})
	}
	if err != nil && !errors.Is(err, ErrRemoteNotFound) {
		return err
	}
	if err := m.src.DeleteLink(ctx, l.Kind, l.LocalID); err != nil {
		return err
	}
	delete(m.links[l.Kind], l.LocalID)
	return nil
}

func projectBody(p Project) map[string]any {
	b := map[string]any{
		"title":       p.Title,
		"description": p.Description,
		"status":      p.Status,
		"priority":    p.Priority,
	}
	if p.Icon != "" {
		b["icon"] = p.Icon
	}
	return b
}

// issueBody gives every copy to the target assignee with no reviewer: the
// target routes a member-held issue only when it waits in review with the
// reviewer undecided, and "none" decides it.
func issueBody(is Issue, assignee, parentRemote, projectRemote string) map[string]any {
	return map[string]any{
		"title":           is.Title,
		"description":     plainMentions(is.Description),
		"status":          is.Status,
		"priority":        is.Priority,
		"assignee_type":   "member",
		"assignee_id":     assignee,
		"reviewer_type":   "none",
		"reviewer_id":     nil,
		"parent_issue_id": nullable(parentRemote),
		"project_id":      nullable(projectRemote),
	}
}

// commentBody posts as the token's member, so the content names the speaker:
// a person by name, an agent by its coding CLI.
func commentBody(c Comment) map[string]any {
	author := c.Author
	if c.Engine != "" {
		author = cliName(c.Engine)
	}
	if author == "" {
		author = "系统"
	}
	return map[string]any{"content": author + "：" + plainMentions(c.Content)}
}

func cliName(provider string) string {
	switch provider {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "opencode":
		return "OpenCode"
	case "antigravity":
		return "Antigravity"
	}
	return provider
}

// mentionLink matches [label](mention://kind/id). Local ids mean nothing on
// the target, and an agent mention there could start a run, so mentions are
// copied as their label only.
var mentionLink = regexp.MustCompile(`\[([^\]]*)\]\(mention://[a-z_]+/[^)\s]*\)`)

func plainMentions(s string) string {
	return mentionLink.ReplaceAllString(s, "$1")
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
