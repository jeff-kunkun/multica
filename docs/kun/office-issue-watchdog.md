# Office issue watchdog

The server-side watchdog checks one configured workspace. It is disabled unless
`MULTICA_ISSUE_WATCHDOG_ENABLED=true`. It is an internal scheduler job, so it
does not add a user-facing API, CLI command, or client setting.

## What it does

- A newly assigned `todo` issue with no first run wakes its assignee after the
  configured start delay.
- An `in_progress` issue with no substantive comment or status change wakes its
  assignee at the configured idle interval, then repeats at that interval.
- After the configured number of employee wake attempts, it sends one manager
  alert. An employee's new substantive comment or a status change resets the
  idle rounds.
- An assignee's substantive comment and transitions into `in_review`, `blocked`,
  or `done` immediately alert the responsible manager through the COO webhook.
- If the responsible manager does not add a signed substantive reply to the
  issue within the manager wait, the watchdog sends one escalation to the COO.
  A status change also resolves the wait. A bare "收到" does not.

Manager replies use the prefix matching the issue's "负责经理" property. The
recognized labels are `【负责经理·扳手】`, `【负责经理·小喇叭】`,
`【负责经理·放大镜】`, `【负责经理·学霸】`, and `【COO·Grok】`. The equivalent
ASCII square brackets and unbracketed prefixes are accepted. A missing property
value falls back to `CTO·扳手`.

Every COO webhook event includes the project name, issue number, title, current
status, employee, responsible manager, wake target, and reason. The receiver
can use `project_name` to route the notification to the corresponding project
group; that external routing must be verified before production enablement. The
watchdog makes at most one HTTP attempt for each event; it does not retry a
failed webhook.

## Configuration

Set these in the backend process environment. The watchdog secret values must
come from the server deployment's private secret configuration, outside the
repository and outside Kun's Mac `~/.secrets`. A recommended host-side source is
`/etc/multica/issue-watchdog.env`, readable only by the deployment service and
injected into the backend process. Do not put the values in a committed
`.env`, deployment values file, log, or issue comment. Production's actual
deployment entry point must be checked read-only before enabling the feature.

| Variable | Default | Meaning |
| --- | --- | --- |
| `MULTICA_ISSUE_WATCHDOG_ENABLED` | `false` | Registers the scheduler and enables event handling |
| `MULTICA_ISSUE_WATCHDOG_WORKSPACE_ID` | empty | The one workspace to monitor; required when enabled |
| `MULTICA_ISSUE_WATCHDOG_WEBHOOK_URL` | empty | COO webhook URL; HTTPS required except loopback test receivers |
| `MULTICA_ISSUE_WATCHDOG_WEBHOOK_KEY` | empty | Bearer key sent to the webhook |
| `MULTICA_ISSUE_WATCHDOG_TICK` | `1m` | Full issue scan cadence; independent of the reminder thresholds below |
| `MULTICA_ISSUE_WATCHDOG_TODO_DELAY` | `10m` | Time assigned `todo` issues can wait before the first wake |
| `MULTICA_ISSUE_WATCHDOG_IDLE_INTERVAL` | `30m` | Idle interval between employee wake attempts |
| `MULTICA_ISSUE_WATCHDOG_MANAGER_WAIT` | `15m` | Time to wait for the responsible manager before COO escalation |
| `MULTICA_ISSUE_WATCHDOG_EMPLOYEE_ROUNDS` | `2` | Employee wake attempts before manager escalation |
| `MULTICA_ISSUE_WATCHDOG_HEARTBEAT_FILE` | `/var/lib/multica/issue-watchdog/last-run` | File atomically replaced with the last run start time in UTC |

Webhook URL and key are never included in request logs or error messages. Do not
enable the feature until the deployment supplies both secrets and a workspace
UUID.

## Stop, backup, and rollback

- Stop the watchdog by setting `MULTICA_ISSUE_WATCHDOG_ENABLED=false` in the
  private backend configuration and restarting the backend. The scheduler job
  is then not registered and event handling becomes a no-op.
- Before applying migration `767_office_issue_watchdog`, take the deployment's
  normal database backup and record its location outside the repository.
- To roll back application code, disable the watchdog and roll back the backend
  image. The two watchdog tables are additive and older code can leave them
  unused; keep them during a code rollback.
- The down migration drops the watchdog tables and their deduplication history.
  Run it only for a disposable environment or after the database backup and an
  explicitly approved data-removal step.

The heartbeat is a local file on the backend host. Its parent directory is
created with mode `0750`; the file is atomically replaced with mode `0640`.
Keep `/var/lib/multica/issue-watchdog` on persistent host storage and make it
writable by the backend service. The path is configurable for deployments with
a different persistent data directory.

## Verification

`server/internal/issuewatchdog/watchdog_integration_test.go` uses a disposable
database issue, the real issue-comment and agent-wakeup path, and an in-process
fake COO receiver. It advances a test clock through the configured 15-minute
manager wait, so the full escalation cycle is verified without waiting in real
time or contacting production. The fake receiver checks the bearer header and
that the project, manager, employee, and issue fields arrive. The external
receiver's project-group routing is outside this test.
