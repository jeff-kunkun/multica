# Migration runner operations

`server/migrations/001_baseline.up.sql` holds the whole schema as of
`585_issue_origin_meeting_validate`. The runner treats it as follows:

| Database | What `migrate up` does |
| --- | --- |
| Empty | Applies the baseline, then any later migrations. |
| Already ran `001_init` … `585` | Records `001_baseline` without running it, then applies later migrations. |
| Stopped before `585` | Refuses. Upgrade it with a build from before the baseline first, or start from an empty database. |

The baseline was cut from a database without the optional `pg_bigm` and
`pg_cron` extensions, so it creates neither them nor the bigram search indexes;
search runs on the `pg_trgm` indexes. The baseline cannot be rolled back. Add schema changes as new numbered
migrations after it; never edit the baseline by hand.

Every new index still uses `CREATE [UNIQUE] INDEX CONCURRENTLY`, one per file,
and registers its index name in `concurrentIndexCleanups` (or
`concurrentDownIndexCleanups` for a down file) in `main.go` so an interrupted
build is dropped and retried. `migrate_concurrent_cleanup_test.go` fails when a
registration is missing.
