# Office issue watchdog

The server-side watchdog checks one configured workspace. It is disabled unless
`MULTICA_ISSUE_WATCHDOG_ENABLED=true`. It is an internal scheduler job. Its
only user-facing surface is the optional `watchdog_reminder` field on the issue
detail response (see "Project-group reminders" below).

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
- An `in_review` issue, or a started issue put back in `todo`, with no
  progress and no live run for its wait alerts the responsible manager (see
  "Office timeouts" below).
- If the responsible manager does not add a signed substantive reply to the
  issue within the manager wait, the watchdog sends one escalation to the COO.
  A status change also resolves the wait. A bare "收到" does not.
- The watchdog never changes an issue's status; only the responsible manager
  does.

Manager replies start with the signature for the issue's "负责经理" property:
`【负责经理·X】`, where X is the part of the option after its last `·`, so
`CTO·扳手` signs `【负责经理·扳手】` and `基建管家·扫帚` signs `【负责经理·扫帚】`.
`COO·Grok` signs `【COO·Grok】`. The equivalent ASCII square brackets and
unbracketed prefixes (`负责经理·扳手`, `负责经理扳手`) are accepted, so a new
manager option needs no code change. A missing property value falls back to
`CTO·扳手`.

Every COO webhook event includes the project name, issue number, title, current
status, employee, responsible manager, wake target, and reason. The receiver
can use `project_name` to route the notification to the corresponding project
group; that external routing must be verified before production enablement. The
watchdog makes at most one HTTP attempt for each event; it does not retry a
failed webhook.

## Office timeouts (Kun, 2026-10-28)

The defaults wake whoever an issue waits on: an unstarted `todo` after 6
hours, an idle `in_progress` issue every 2 hours, a hold nobody answered after
2 hours (with `HOLD_WAKE` on), and the responsible manager after 2 hours in
`in_review`. Each threshold is an environment variable below.

- **`in_review`.** When neither progress nor a run has happened for
  `IN_REVIEW_WAIT`, the manager gets one `in_review_overdue` webhook event
  (`wake_target=manager`) asking them to accept and close the issue, or to put
  the 「等 Kun 拍板」 label on it. A running task on the issue counts as
  movement. The event is sent once per stall and starts the ordinary manager
  wait, so no signed reply in 15 minutes escalates to the COO. No comment is
  posted and no employee is woken.
- **Paused `todo`.** A `todo` issue that has run before (it was started, then
  put back) is a pause, not a new assignment. Its assignee is not woken;
  after `TODO_DELAY` with no progress the manager gets one `todo_overdue`
  event, asking whether to resume it.

## 「等 Kun 拍板」 label

Kun's decisions are a label, not a board column or status. The responsible
manager labels an issue 「等 Kun 拍板」 (`KUN_LABEL`, matched case-insensitively)
when it waits on Kun, including every red line: touching production,
spending money, posting to social media, or changing the architecture. The
label comes and goes through the ordinary label commands (`multica issue
label add|remove` and the web UI); the workspace label must exist first
(`multica label create`).

- **Push.** When the label goes on, the watchdog sends one `kun_decision`
  webhook event with `wake_target=office_group` and `group` set to
  `KUN_GROUP` (「Multica 办公室」), for the receiver to post into that group.
  It reacts to the `issue_labels:changed` event at once; the scan catches a
  label put on at issue creation, which publishes no event. Each time the
  label goes on is one push; done and cancelled issues are not pushed. The
  push starts no manager wait, so it does not escalate to the COO.
- **Quiet.** While the label is on, the watchdog wakes nobody on the issue:
  no idle, hold, review, stuck, `in_review`, or paused-`todo` wake. A stalled
  sub-issue of a labelled parent sends the manager a `child_unattended` event
  instead of waking the parent's owner, as under a round stop.
- **Lift.** Taking the label off closes the decision in the attempt log and
  restarts the issue's idle clock, so Kun's answer is not followed at once by
  a wake for the hours spent waiting.

The pushes are rows in `office_issue_watchdog_attempt` with event type
`kun_decision`; `manager_resolved_at` records when the label came off. No
migration is needed.

## Project-group reminders for live runs

With `MULTICA_ISSUE_WATCHDOG_RUN_AWARE=true`, an `in_progress` or started
`todo` issue whose agent assignee has a task in `running` status is no longer
woken with a "please continue" comment: the employee is already working.
Instead, when that run has not reported in the issue for the silent interval,
the watchdog sends one `employee_running_silent` webhook event with
`wake_target=project_group`, the `task_id`, and `silent_minutes`. It repeats at
most once per silent interval per run and never comments on the issue.
When the run's daemon reported a command in flight within the last 2 minutes
([live status](office-live-status.md)), the event also carries
`current_command` and its reason ends with 「；当前命令：…」.

A report is the run starting, the assignee's substantive comment (a bare
"收到" does not count), the assignee's latest progress line in the
`issue_progress` history (so a later summary by someone else does not hide
it), or a status change; whichever is newest counts.
Issues with no running task (assigned but not started, queued, dispatched,
deferred, or member assignees) keep the comment wake described above.

Delivered reminders are read back from `office_issue_watchdog_attempt`. Issue
detail responses then carry an optional `watchdog_reminder` object
(`group_reminded_at`, `group_reminder_count`, `task_id`); older clients ignore
it. `multica issue get` prints `Group reminded: <time> (<n> total)`, and
clients built with the `office_watchdog_group_reminder` flag show a "Group
reminded" chip in the issue header. Client builds turn flags on with
`VITE_MULTICA_FEATURE_FLAGS` (desktop) or `NEXT_PUBLIC_MULTICA_FEATURE_FLAGS`
(web), a comma list read at build time; release builds leave both unset.

## Review wakes

Four switches, all off by default, wake whoever a review loop is waiting on.
Each wake is a watchdog comment that @mentions the person; for an agent it
starts or joins a run through the normal mention path.

**Recognizing reviews.** A verdict is a standalone `verdict: hold` or
`verdict: pass` line (`multica issue comment add --verdict`), which wins when
present. Office reviews are mostly prose, so a comment whose first line names
a conclusion (`审查结论`, `复审结论`, `审核结论`, `审查意见`, `复审意见`) or opens
with `审查`/`复审`/`审核` also counts: hold words (`不通过`, `需要修改`,
`需要修正`, `暂不交`, `打回`, `退回`, `驳回`, `hold`, ...) are checked before
`通过`/`pass`. Only comments by someone other than the developer count. A
review request is the developer's comment that @mentions someone else and
says `审查`, `复审`, `审核`, or `review`.

**Who is who.** The developer is the assignee. The reviewer is the issue's
reviewer field when set, else the person the request @mentions. When the issue
has no assignee, or the assignee is the reviewer, the parent issue's assignee
stands in for the developer. The responsible manager is the "负责经理" property
as above.

| Switch | When | Who is woken | How often |
| --- | --- | --- | --- |
| `HOLD_WAKE` | A hold is posted that does not @mention the developer, and the developer has no live task on the issue | Developer, at once | Once per verdict |
| `HOLD_WAKE` | The last substantive comment is a hold and no run has started since, for `HOLD_WAIT` | Developer, else the parent's owner | Once per verdict |
| `REVIEW_WAKE` | A review request has no verdict after `REVIEW_WAIT`, and the reviewer has no live task on the issue | Reviewer | Once per request |
| `STUCK_WAKE` | An `in_progress` issue with no queued, dispatched, running, or deferred task for `NO_RUN_WAIT` | The reviewer while a request is open; the responsible manager (webhook `awaiting_signoff`) after a pass with no signed manager reply or status change since; otherwise the developer | Once per stall; the developer's wake is the ordinary idle round moved earlier, so its two-round manager escalation still applies |
| `CHILD_WAKE` | A `todo`, `in_progress`, or `blocked` sub-issue with no progress and no run for `CHILD_WAIT`, which has no assignee, or whose parent's owner has neither commented on nor run either issue for `CHILD_WAIT` | The parent's owner, by a comment on the parent naming the sub-issue and how long it stalled; the responsible manager when the parent has no owner | Once per stall |

A person with a live task on the issue is never woken, since the comment would
be steered into that run. A sub-issue owned by the parent's owner is left to
the ordinary idle wake. One scan sends at most one review wake per issue, in
the order hold, review request, stuck; while a stuck issue waits on its
reviewer or manager, the developer's idle wake stays quiet, and every review
wake restarts the idle clock. Every wake is claimed in
`office_issue_watchdog_attempt` under a key naming the rule and the verdict,
request, or stall, so repeated scans and concurrent scanners send it once. No
migration is needed.

**Stopping a failing loop (`ROUND_STOP`, off by default).** A review round
ends in hold when a hold arrives after the developer has commented since the
previous round's hold (the first hold always opens a round; a reviewer's
second hold with no developer comment in between stays in the same round). A
pass starts the count over. When `ROUND_LIMIT` rounds in a row (default 2) have
ended in hold, the watchdog stops instead of waking the developer:

- it leaves one comment on the issue that @mentions nobody, so no run starts.
  The comment lists each round's hold line and four options for Kun: one more
  round, merge now with the rest as follow-up issues, the responsible manager
  arbitrates or the reviewer changes, or drop the change;
- it sends the responsible manager one `review_round_limit` webhook event to
  take to Kun; the ordinary 15-minute COO escalation applies to it;
- until the developer posts a new review request, or a pass lands, no review
  wake, stuck wake, or idle wake fires for the issue, and no sub-issue wake
  reports it as stalled. A stalled sub-issue of a stopped issue does not wake
  the stopped issue's owner either; the responsible manager gets a
  `child_unattended` webhook event for the sub-issue instead.

Each further failed round after a stop stops again. Wakes outside the
watchdog, such as a hold that @mentions the developer, still start runs as
usual.

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
| `MULTICA_ISSUE_WATCHDOG_TODO_DELAY` | `6h` | Time assigned `todo` issues can wait before the first wake, and a paused `todo` before the manager alert |
| `MULTICA_ISSUE_WATCHDOG_IDLE_INTERVAL` | `2h` | Idle interval between employee wake attempts |
| `MULTICA_ISSUE_WATCHDOG_IN_REVIEW_WAIT` | `2h` | Time an `in_review` issue can sit quiet before the manager alert |
| `MULTICA_ISSUE_WATCHDOG_KUN_LABEL` | `等 Kun 拍板` | Label that pushes an issue to Kun's group and quiets its wakes; unset uses the default, set to empty turns both off |
| `MULTICA_ISSUE_WATCHDOG_KUN_GROUP` | `Multica 办公室` | Group named in the `kun_decision` event |
| `MULTICA_ISSUE_WATCHDOG_MANAGER_WAIT` | `15m` | Time to wait for the responsible manager before COO escalation |
| `MULTICA_ISSUE_WATCHDOG_EMPLOYEE_ROUNDS` | `2` | Employee wake attempts before manager escalation |
| `MULTICA_ISSUE_WATCHDOG_RUN_AWARE` | `false` | Replaces comment wakes for live runs with project-group reminders |
| `MULTICA_ISSUE_WATCHDOG_SILENT_INTERVAL` | `20m` | Silence before a live run's project-group reminder, and the gap between them |
| `MULTICA_ISSUE_WATCHDOG_HOLD_WAKE` | `false` | Wakes the developer on a hold that did not @mention them |
| `MULTICA_ISSUE_WATCHDOG_HOLD_WAIT` | `2h` | Time after a hold with no run before the scan wakes the developer |
| `MULTICA_ISSUE_WATCHDOG_REVIEW_WAKE` | `false` | Wakes the reviewer once when a review request has no verdict |
| `MULTICA_ISSUE_WATCHDOG_REVIEW_WAIT` | `20m` | Time a review request can wait for a verdict |
| `MULTICA_ISSUE_WATCHDOG_STUCK_WAKE` | `false` | Routes an `in_progress` issue with no run to whoever it waits on |
| `MULTICA_ISSUE_WATCHDOG_NO_RUN_WAIT` | `15m` | Time an `in_progress` issue can go without any run |
| `MULTICA_ISSUE_WATCHDOG_CHILD_WAKE` | `false` | Wakes the parent's owner about a stalled sub-issue nobody looks after |
| `MULTICA_ISSUE_WATCHDOG_CHILD_WAIT` | `30m` | Time a sub-issue can stall before the parent's owner is woken |
| `MULTICA_ISSUE_WATCHDOG_ROUND_STOP` | `false` | Stops the loop and lays out options for Kun after `ROUND_LIMIT` failed review rounds in a row |
| `MULTICA_ISSUE_WATCHDOG_ROUND_LIMIT` | `2` | Failed review rounds in a row before the stop |
| `MULTICA_ISSUE_WATCHDOG_HEARTBEAT_FILE` | `/var/lib/multica/issue-watchdog/last-run` | File atomically replaced with the last run start time in UTC |

Webhook URL and key are never included in request logs or error messages. Do not
enable the feature until the deployment supplies both secrets and a workspace
UUID.

## Stop, backup, and rollback

- Stop the watchdog by setting `MULTICA_ISSUE_WATCHDOG_ENABLED=false` in the
  private backend configuration and restarting the backend. The scheduler job
  is then not registered and event handling becomes a no-op.
- To stop a review wake, set its switch (`HOLD_WAKE`, `REVIEW_WAKE`,
  `STUCK_WAKE`, `CHILD_WAKE`, `ROUND_STOP`) to `false` and restart the backend. Its past
  wakes stay in the attempt log and as comments on the issues.
- To stop the Kun-decision push and quieting, set
  `MULTICA_ISSUE_WATCHDOG_KUN_LABEL=` (empty) and restart the backend; the
  label itself stays on the issues.
- To return to comment wakes for live runs, set
  `MULTICA_ISSUE_WATCHDOG_RUN_AWARE=false` and restart the backend. Recorded
  reminders stay in the attempt log and remain visible on the issue.
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

`TestRunAwareWatchdogRemindsGroupInsteadOfCommentingOnLiveRuns` covers the
run-aware mode with three issues: a live run that reported five minutes ago
gets nothing, a live run silent for 25 minutes gets one project-group event
and no comment, and an assigned issue that never started gets the comment
wake. It then checks the 20-minute spacing between reminders.

`review_integration_test.go` covers the review wakes on the same disposable
database, with the real wake comment and agent-trigger path:

- a hold with no @developer wakes the developer once, at once;
- a hold that names the developer adds nothing;
- a hold followed by 11 minutes with no run wakes the developer once, or the
  parent's owner when the issue has no assignee;
- a run started after the hold suppresses the wake;
- a re-review left 25 minutes without a verdict wakes the reviewer exactly
  once, while one answered within 10 minutes does not;
- an `in_progress` issue with no run for 16 minutes wakes the author, the
  reviewer, or the responsible manager, according to where it waits; a live
  run gets nothing;
- a stalled sub-issue with no assignee, or with an idle parent owner, puts one
  comment on the parent waking its owner; one whose owner is moving gets none;
- a second failed round leaves one options comment with no mention and one
  `review_round_limit` event, starts no run, keeps scans at +11, +16 and +45
  minutes quiet, and lifts once the developer asks for another review; with
  `ROUND_STOP` off the same hold wakes the developer as usual;
- a stalled, unassigned sub-issue under a parent stopped for Kun wakes nobody
  and starts no run on the parent, and sends the manager one
  `child_unattended` event; with `ROUND_STOP` off it wakes the parent's owner
  as before.

`office_timeouts_integration_test.go` covers the office timeouts and the
label on the same database:

- an `in_review` issue quiet for 1h59m gets nothing, and at 2h one
  `in_review_overdue` manager event, no comment, and after 15 more minutes
  one COO escalation; one with a running task gets nothing;
- a paused `todo` quiet for 6h gets one `todo_overdue` manager event;
- a `【负责经理·扫帚】` reply ends 基建管家·扫帚's wait with no COO escalation;
- putting the label on sends one `office_group` push to 「Multica 办公室」
  with no manager wait, even for a repeated event; while it is on, three
  hours idle and a hold wake nobody; taking it off waits a fresh idle
  interval before the ordinary wake; putting it on again with no event is
  pushed by the scan;
- a stalled sub-issue under a labelled parent wakes nobody on the parent and
  sends the manager one `child_unattended` event.

`review_test.go` checks verdict recognition against the review wording used on
2026-10-27, which had no `verdict:` lines, and the round counting: same-round
holds, a bare `收到`, a pass resetting the count, a new request lifting the
stop, a third round stopping again, and a configurable limit.
