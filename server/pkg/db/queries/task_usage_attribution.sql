-- name: ListIssueUsageAttribution :many
SELECT
    COALESCE(NULLIF(atq.originator_source, ''), 'unattributed')::text AS attribution_source,
    COALESCE(NULLIF(atq.trigger_evidence_kind, ''), 'unknown')::text AS trigger_evidence_kind,
    COUNT(DISTINCT atq.id)::int AS task_count
FROM agent_task_queue atq
WHERE atq.issue_id = $1
GROUP BY 1, 2
ORDER BY 1, 2;
