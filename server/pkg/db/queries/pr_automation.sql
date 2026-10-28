-- =====================
-- PR auto-complete (MUL-7429)
-- =====================

-- name: GetIssuePRAutoCompleteDisabled :one
SELECT EXISTS (
    SELECT 1 FROM issue_pr_automation
    WHERE issue_id = $1 AND auto_complete_disabled
)::boolean AS disabled;

-- name: SetIssuePRAutoCompleteDisabled :exec
INSERT INTO issue_pr_automation (
    issue_id, workspace_id, auto_complete_disabled, updated_by_type, updated_by_id
) VALUES (
    $1, $2, $3, sqlc.narg('updated_by_type'), sqlc.narg('updated_by_id')
)
ON CONFLICT (issue_id) DO UPDATE SET
    auto_complete_disabled = EXCLUDED.auto_complete_disabled,
    updated_by_type = EXCLUDED.updated_by_type,
    updated_by_id = EXCLUDED.updated_by_id,
    updated_at = now();

-- name: IsPullRequestExcludedFromIssue :one
SELECT EXISTS (
    SELECT 1 FROM issue_pull_request_exclusion
    WHERE issue_id = $1 AND pull_request_id = $2
)::boolean AS excluded;

-- name: ExcludePullRequestFromIssue :exec
INSERT INTO issue_pull_request_exclusion (
    issue_id, pull_request_id, workspace_id, excluded_by_type, excluded_by_id
) VALUES (
    $1, $2, $3, sqlc.narg('excluded_by_type'), sqlc.narg('excluded_by_id')
)
ON CONFLICT (issue_id, pull_request_id) DO UPDATE SET
    excluded_by_type = EXCLUDED.excluded_by_type,
    excluded_by_id = EXCLUDED.excluded_by_id,
    created_at = now();

-- name: DeletePullRequestExclusion :exec
DELETE FROM issue_pull_request_exclusion
WHERE issue_id = $1 AND pull_request_id = $2;

-- name: ListIssueLinkedPullRequestStates :many
-- Every PR linked to the issue across GitHub and self-hosted providers, for
-- the merge decision. Ordered by number so reasons read stably.
SELECT pr.id, 'github'::text AS provider, pr.pr_number, pr.state
FROM github_pull_request pr
JOIN issue_pull_request ipr ON ipr.pull_request_id = pr.id
WHERE ipr.issue_id = $1
UNION ALL
SELECT pr.id, pr.provider AS provider, pr.pr_number, pr.state
FROM vcs_pull_request pr
JOIN issue_vcs_pull_request ipr ON ipr.pull_request_id = pr.id
WHERE ipr.issue_id = $1
ORDER BY pr_number;
