# Run control

## Stop every run on one issue

Use the issue-level guard when an agent chain must stop immediately:

```bash
multica issue halt <issue-id>    # cancel queued/dispatched/running runs and block agent triggers
multica issue resume <issue-id>  # clear the halt guard; a human comment is still needed to reset a chain budget
```

The guard is issue-scoped. A human comment clears it and resets the
delegation-chain budget; `resume` only clears an explicit halt and does not
reset an already-exceeded budget. Direct human-triggered runs are never
consumed by that budget. The default chain limit is thirty runs; a workspace
admin changes it under Settings → General → Agent run limits (stored as
`agent_chain_budget` in the workspace `settings` JSON, `0` = unlimited). Hitting
the limit posts a system comment in the triggering thread instead of stopping
silently.

The same settings section holds a run time limit
(`agent_task_timeout_minutes`, `0`/absent = none). A run that outlives it fails with reason `task_time_limit`:
a round boundary, not a wrong result. The platform continues the same CLI session and working directory
until the attempt budget is spent, and the continuation is told to close out finished work and split what remains.
When the budget is spent the issue becomes `blocked` with a comment instead of sitting in `todo`; a sub-issue also leaves a short note on its parent.
