package mirror

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGSource reads one workspace from the local database and keeps the
// mirror_link rows for one target.
type PGSource struct {
	pool        *pgxpool.Pool
	workspaceID string
	target      string
	projects    []string // project ids to copy; empty means all
}

// NewPGSource resolves workspace (slug or id) and returns a source for it.
// target names the destination, so links for another target never mix in.
// With projects set, only those projects, their issues and the issues'
// comments are copied; anything else counts as gone.
func NewPGSource(ctx context.Context, pool *pgxpool.Pool, workspace, target string, projects []string) (*PGSource, error) {
	var id string
	err := pool.QueryRow(ctx,
		`SELECT id::text FROM workspace WHERE slug = $1 OR id::text = $1`, workspace).Scan(&id)
	if err != nil {
		return nil, err
	}
	return &PGSource{pool: pool, workspaceID: id, target: target, projects: projects}, nil
}

// WorkspaceID is the resolved local workspace id.
func (s *PGSource) WorkspaceID() string { return s.workspaceID }

func (s *PGSource) Versions(ctx context.Context) (map[string]map[string]time.Time, error) {
	queries := map[string]string{
		KindProject: `SELECT id::text, updated_at FROM project WHERE workspace_id = $1`,
		KindIssue:   `SELECT id::text, updated_at FROM issue WHERE workspace_id = $1`,
		KindComment: `SELECT id::text, updated_at FROM comment WHERE workspace_id = $1 AND deleted_at IS NULL`,
	}
	args := []any{s.workspaceID}
	if len(s.projects) > 0 {
		queries[KindProject] += ` AND id::text = ANY($2)`
		queries[KindIssue] += ` AND project_id::text = ANY($2)`
		queries[KindComment] += ` AND issue_id IN (SELECT id FROM issue WHERE workspace_id = $1 AND project_id::text = ANY($2))`
		args = append(args, s.projects)
	}
	out := map[string]map[string]time.Time{}
	for kind, q := range queries {
		rows, err := s.pool.Query(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		m := map[string]time.Time{}
		for rows.Next() {
			var id string
			var at time.Time
			if err := rows.Scan(&id, &at); err != nil {
				rows.Close()
				return nil, err
			}
			m[id] = at
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		out[kind] = m
	}
	return out, nil
}

func (s *PGSource) Project(ctx context.Context, id string) (Project, error) {
	p := Project{ID: id}
	err := s.pool.QueryRow(ctx, `
		SELECT title, COALESCE(description, ''), COALESCE(icon, ''), status, priority
		FROM project WHERE id = $1 AND workspace_id = $2`, id, s.workspaceID,
	).Scan(&p.Title, &p.Description, &p.Icon, &p.Status, &p.Priority)
	return p, err
}

func (s *PGSource) Issue(ctx context.Context, id string) (Issue, error) {
	is := Issue{ID: id}
	err := s.pool.QueryRow(ctx, `
		SELECT w.issue_prefix || '-' || i.number, i.title, COALESCE(i.description, ''),
		       i.status, i.priority,
		       COALESCE(i.parent_issue_id::text, ''), COALESCE(i.project_id::text, ''),
		       COALESCE(CASE i.assignee_type
		           WHEN 'member' THEN (SELECT name FROM "user" WHERE id = i.assignee_id)
		           WHEN 'agent' THEN (SELECT name FROM agent WHERE id = i.assignee_id)
		           WHEN 'squad' THEN (SELECT name FROM squad WHERE id = i.assignee_id)
		       END, '')
		FROM issue i JOIN workspace w ON w.id = i.workspace_id
		WHERE i.id = $1 AND i.workspace_id = $2`, id, s.workspaceID,
	).Scan(&is.Identifier, &is.Title, &is.Description, &is.Status, &is.Priority,
		&is.ParentID, &is.ProjectID, &is.Assignee)
	return is, err
}

func (s *PGSource) Comment(ctx context.Context, id string) (Comment, error) {
	c := Comment{ID: id}
	err := s.pool.QueryRow(ctx, `
		SELECT c.issue_id::text, COALESCE(c.parent_id::text, ''), c.content, c.created_at,
		       COALESCE(CASE c.author_type
		           WHEN 'member' THEN (SELECT name FROM "user" WHERE id = c.author_id)
		           WHEN 'agent' THEN (SELECT name FROM agent WHERE id = c.author_id)
		       END, ''),
		       COALESCE(CASE c.author_type
		           WHEN 'agent' THEN (SELECT r.provider FROM agent a JOIN agent_runtime r ON r.id = a.runtime_id
		                              WHERE a.id = c.author_id)
		       END, '')
		FROM comment c
		WHERE c.id = $1 AND c.workspace_id = $2 AND c.deleted_at IS NULL`, id, s.workspaceID,
	).Scan(&c.IssueID, &c.ParentID, &c.Content, &c.CreatedAt, &c.Author, &c.Engine)
	return c, err
}

func (s *PGSource) Links(ctx context.Context) ([]Link, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT kind, local_id::text, remote_id, source_updated_at, inbound
		FROM mirror_link WHERE target = $1`, s.target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Kind, &l.LocalID, &l.RemoteID, &l.SourceUpdatedAt, &l.Inbound); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *PGSource) SaveLink(ctx context.Context, l Link) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO mirror_link (target, kind, local_id, remote_id, source_updated_at, inbound)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (target, kind, local_id)
		DO UPDATE SET remote_id = EXCLUDED.remote_id, source_updated_at = EXCLUDED.source_updated_at,
		              inbound = EXCLUDED.inbound`,
		s.target, l.Kind, l.LocalID, l.RemoteID, l.SourceUpdatedAt, l.Inbound)
	return err
}

func (s *PGSource) DeleteLink(ctx context.Context, kind, localID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM mirror_link WHERE target = $1 AND kind = $2 AND local_id = $3`,
		s.target, kind, localID)
	return err
}
