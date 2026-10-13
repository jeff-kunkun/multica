The issue_status category migrations as they were before the schema was
compressed into `server/migrations/001_baseline.up.sql`. The status-category
maintenance tests replay this history to exercise the backfill job.
