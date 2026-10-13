// Package mirror copies one workspace's projects, issues and comments, one
// way, into a workspace on another Multica server through that server's
// public API. The target is read-only by convention: nothing flows back.
//
// It is a single reconcile loop. Every tick it lists the local objects with
// their updated_at, compares them with what was last copied (mirror_link), and
// creates, updates or retires the remote copies that differ. Startup is the
// same tick with nothing in memory, so a missed change is caught on the next
// pass and a restart does not re-copy what is already there.
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

// Config is read from the environment. All four values are required; with any
// of them missing the mirror is off.
type Config struct {
	Workspace       string // local workspace slug or id
	TargetURL       string // e.g. https://ai.ferryway.cc
	TargetToken     string // personal access token of a target member
	TargetWorkspace string // target workspace id
	Interval        time.Duration
}

func ConfigFromEnv() (Config, bool) {
	c := Config{
		Workspace:       strings.TrimSpace(os.Getenv("MULTICA_MIRROR_WORKSPACE")),
		TargetURL:       strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_MIRROR_TARGET_URL")), "/"),
		TargetToken:     strings.TrimSpace(os.Getenv("MULTICA_MIRROR_TARGET_TOKEN")),
		TargetWorkspace: strings.TrimSpace(os.Getenv("MULTICA_MIRROR_TARGET_WORKSPACE")),
		Interval:        defaultInterval,
	}
	if d, err := time.ParseDuration(os.Getenv("MULTICA_MIRROR_INTERVAL")); err == nil && d > 0 {
		c.Interval = d
	}
	ok := c.Workspace != "" && c.TargetURL != "" && c.TargetToken != "" && c.TargetWorkspace != ""
	return c, ok
}

// Link is one mirror_link row.
type Link struct {
	Kind            string
	LocalID         string
	RemoteID        string
	SourceUpdatedAt time.Time
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
	Author    string
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

// Remote is the target server's API, reduced to what the mirror writes.
type Remote interface {
	Create(ctx context.Context, path string, body map[string]any) (string, error)
	Update(ctx context.Context, path string, body map[string]any) error
	Delete(ctx context.Context, path string) error
}

type Mirror struct {
	src      Source
	remote   Remote
	interval time.Duration
	log      *slog.Logger

	links map[string]map[string]Link // kind -> local id -> link
}

func New(src Source, remote Remote, interval time.Duration, log *slog.Logger) *Mirror {
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Mirror{src: src, remote: remote, interval: interval, log: log}
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
			if m.links[l.Kind] != nil {
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
	if linked && link.SourceUpdatedAt.Equal(version) {
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
		create, body = "/api/issues", issueBody(is, parent, project)
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
		if kind == KindIssue {
			// Mirrored titles repeat often; the target must not refuse them.
			body["allow_duplicate"] = true
		}
		created, err := m.remote.Create(ctx, create, body)
		if err != nil {
			return "", err
		}
		remoteID = created
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

func issueBody(is Issue, parentRemote, projectRemote string) map[string]any {
	assignee := is.Assignee
	if assignee == "" {
		assignee = "无"
	}
	desc := plainMentions(is.Description)
	footer := fmt.Sprintf("新办公室 %s · 执行人：%s", is.Identifier, assignee)
	if strings.TrimSpace(desc) != "" {
		desc = strings.TrimRight(desc, "\n") + "\n\n---\n" + footer
	} else {
		desc = footer
	}
	return map[string]any{
		"title":           is.Title,
		"description":     desc,
		"status":          is.Status,
		"priority":        is.Priority,
		"parent_issue_id": nullable(parentRemote),
		"project_id":      nullable(projectRemote),
	}
}

var shanghai = time.FixedZone("CST", 8*3600)

func commentBody(c Comment) map[string]any {
	author := c.Author
	if author == "" {
		author = "系统"
	}
	head := fmt.Sprintf("**%s** · %s", author, c.CreatedAt.In(shanghai).Format("01-02 15:04"))
	return map[string]any{"content": head + "\n\n" + plainMentions(c.Content)}
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
