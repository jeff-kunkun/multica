-- Per-agent platform auto-retry switch (DENE-217).
-- DEFAULT TRUE keeps existing FailTask / MaybeRetryFailedTask behaviour:
-- retryableReasons still re-run unless the owner turns this off.
-- No index: the column is loaded with the agent row by primary key, never
-- filtered, so the repo's CREATE INDEX CONCURRENTLY rule does not apply.
-- Single-statement ALTER; no transaction split needed.
ALTER TABLE agent ADD COLUMN auto_retry_enabled BOOLEAN NOT NULL DEFAULT TRUE;
