# Run control

## Stop a piece of work

```bash
multica issue halt <issue-id> --output json
multica issue halt <issue-id> --related --output json
multica issue status <issue-id> cancelled --related --output json
multica chat stop <chat-id|url> --related --output json
multica issue resume <issue-id> --output json
```

`halt` stops queued, dispatched, running, waiting-for-directory and deferred
runs, then blocks agent triggers on each stopped issue. `chat stop` stops every
active or queued turn in the selected chat, including retry backoff. Without
`--related`, each action stays on its named object. A plain cancelled status
still leaves existing runs alone; `status cancelled --related` explicitly
combines the status and stop in one transaction.

`--related` stays in the current workspace and follows the named root:

- Issue root: the issue and its descendant issues, plus each selected issue's
  source chat and visible chat ticket bindings.
- Chat root: the named chat's direct parent/children and sessions sharing its
  exact external thread binding key; issues opened from or visibly bound to
  the named chat and their descendants; those issues' source/bound chats.

Only child-issue links recurse. Reached chats are endpoints: they never expand
again to sibling issues, other followed tickets, more spawned chats or further
thread bindings. Stopping issue A from chat C does not stop another issue B
opened from C. Stopping C itself includes both directly linked issues A and B.
The scope never matches titles, projects or all chats in a channel.
The server checks every issue, chat owner and private agent before
writing. A hidden or unauthorized target rejects the entire operation, with
no partial stop. Only the person who started a chat may interrupt its work.
External Bots use the same endpoint and their owner's ordinary permissions.
The Web/Desktop stop dialog has the same optional related-work checkbox;
the native phone chat stop alert offers a separate related-work action.

The stop response reports `cancelled_count`, `issue_ids` and
`chat_session_ids`. Already stopped runs are not counted again. A later human
message may start a chat again; the stop never archives or deletes it.

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

## Message a reply that is still running

A chat message or an issue comment can reach an agent mid-reply in three ways
(a chat has a fourth, `replace`, below). `multica chat send` and
`multica issue comment add` both take `--mode`:

| Mode | What happens | CLI process | Session |
| --- | --- | --- | --- |
| `steer` | Read after the current step; the original work continues | Same process | Same session |
| `queue` | Explicitly handled once this reply / run finishes | New process | Resumes the same session |
| `restart` | Stops the reply now and starts over from this message; the half-done step is dropped | New process | Resumes the same session |

```bash
multica chat send --session <chat-id> --content-file ./msg.md --mode steer
multica issue comment add <issue-id> --content-file ./msg.md --mode restart
multica chat send --session <chat-id> --content-file ./msg.md --mode replace --wait --output json
```

- `replace` (chat only) is for a changed mind: **意图改变用 `--mode replace`**。
  One transaction cancels every queued follow-up of the chat, marks those
  messages and the running turn's trigger message superseded (the chat shows
  "已被新指令替换"), stops the running reply and starts a new run from the new
  message at the head of the queue. With nothing running it still voids the
  queued old messages and runs the new one. The new run is told the earlier
  instructions are void. Only the chat creator or an agent run may use it; a
  personal token works. Managed/linked chats refuse it with 400.
  `--output json` returns `new_task_id`, `cancelled_task_ids`,
  `superseded_message_ids` (and `stop_failed_task_ids` if a stop did not land);
  `--wait` waits for the new reply. Plain `restart` keeps queued messages;
  `replace` drops them.
- `multica chat clear-queue --session <id>` only empties the queued follow-ups
  (the head already claimed and the running reply stay); `--output json` gives
  `{"chat_session_id":…, "cleared":true}`.
- `steer` reaches the run in one of two ways, decided by its CLI:
  - Claude, Codex, Grok, OpenCode 1.x and Pi read it in the running process
    after the current step.
  - One-shot CLIs (Cursor, Copilot, CodeArts, DevEco, Antigravity, OpenClaw)
    have no input channel while they work, so the daemon stops the CLI and
    resumes the same session at once with the message; the step in flight is
    cut off. The run, its receipt and the session ID stay the same.
    Antigravity can only do this on a run that resumed a session.
  - ACP CLIs (Hermes, Kimi, Kiro, Qoder, QwenPaw, Reasonix, Trae, ZeroClaw,
    Devin, DIM, MCode) cancel the current step and prompt the same session in
    the same process with the message; the step in flight is cut off.
  The chat pending task reports this as `steer_mode` and an issue task as
  `supplement_steer_mode` (`same`, `restart` or `handoff`).
- Any other CLI, or one too old to negotiate `steer`, is refused with exit status 1, the reason, and
  `available_modes: [queue, restart]` in the JSON body; nothing is posted, so
  resend with one of those.
- `steer` on a comment is for people only and is text-only (no attachments).
- With nothing running, every mode simply sends/posts and starts a run.
- `chat send` needs an explicit `--session`; it never falls back to the chat
  the current run belongs to, so a run cannot queue a turn for itself.
- Without `--mode`, a chat message or same-agent issue comment merges into the
  existing queued or active run, including issue comments in another thread.
  A queued chat collects one input batch; a running chat receives a supplement
  in that same run. No extra run is created for repeated wakeups or a restart
  retry while the same object already has an active run.
- A running chat whose CLI cannot accept live input rejects default merging
  with an explicit reason and `available_modes: [queue, restart]`; choose one
  deliberately. The rejection does not persist the message.
- After converting a chat into an issue, the issue is the execution place.
  The source chat stops its remaining runs, refuses new sends and shows a link
  to the issue. Repeating conversion returns that same issue.

## Urgent delivery (`chat send --urgent`)

`--urgent` is for a message that is worthless late: a verification code, an
SMS to relay, a "stop now". It changes three things and nothing else:

- **Deliver now or fail now.** If the agent's runtime cannot claim (offline, or
  no heartbeat in the last 150 s) and the agent has no online standby runtime,
  the server refuses with HTTP 409 before anything is saved. The body carries
  `code: "runtime_offline"`, a `reason` such as
  `runtime_offline: Claude (MacBook-Pro.local) last_seen 2026-10-11T09:00:10Z`,
  the `runtime` snapshot and `next_steps`. The CLI prints that JSON and exits
  nonzero; there is nothing to resend.
- **Head of the queue, 30 s to start.** An accepted urgent turn is created at
  priority 4 with restart semantics (a reply in progress is stopped, the same
  session resumes from this message) and a `pickup_deadline_at` 30 s out. If
  no daemon has started it by then — whether it is still queued, dispatched,
  or parked in `waiting_local_directory` behind another run — the server
  fails it with `failure_reason: pickup_timeout`, posts one system note in
  the chat saying which machine did not pick it up, and does not retry. A
  daemon that asks to start an expired turn is refused in the same
  transaction (HTTP 409, `code: pickup_timeout`): an expired code never runs.
- **Standby runtime.** When the primary runtime is offline but the agent has
  `fallback_runtime_id` set and that runtime is online, the task is dispatched
  there instead; the response says `fallback_runtime: true`. A standby must
  belong to the same owner and the same provider as the primary (server
  enforced; see `agents.md`). The standby's daemon claims the turn through
  the ordinary poll (single or batch) and starts it like any other task; the
  same eligibility rule — the agent's bound runtime or its configured
  standby — is applied at claim, at response build and in the delivery
  transaction, so clearing the standby or rebinding the agent while the turn
  is in flight fails it with `invalid_task_identity` instead of handing it
  to a machine the agent no longer names.

```bash
multica chat send --session <id> --urgent "验证码 482913，请立刻填入" --output json
```

- `--urgent` implies `--mode restart` (an explicit `--mode replace` is kept);
  combining it with `--mode steer` is a
  400. Only the person who started the chat (or an agent run) may restart a
  reply, so a shared-chat guest gets 403 as with any restart.
- `--urgent` always waits for the **pickup** (task_status `running`), not the
  full reply: exit 0 with `pickup_elapsed_ms` once a daemon starts the turn;
  nonzero with `task_status`, `reason` (`pickup_timeout`) and `runtime` when
  nobody starts it by `pickup_deadline_at`. The wait ends at that server
  deadline and never later: every progress request and pause runs under it,
  so a slow request or a late server sweep cannot stretch the command past
  the 30 s. `--wait-timeout` does not apply; `--wait` adds nothing. When the
  server has not judged the turn by the deadline, `task_status` is whatever
  it last reported (it may still be `dispatched`) and `reason_source: sender`
  says the sender stopped waiting; `reason_source: server` marks a verdict the
  server wrote (`failed`/`cancelled`). Only the server decides the task's
  fate. A zero exit means the message is being read now. Read the reply later
  with `multica chat history`.
- `--urgent` is not available with `--linked` (linked-workspace sends) and is
  not a flag of `chat new`; open the chat first, then send urgent.
- `multica chat progress --session <id> --history --output json` shows
  `pickup_timeout` as the task reason once the deadline has passed.

## Hand work to another agent

When agent A is running on an issue and the comment @-mentions a different
agent B, `comment add` takes two more modes:

| Mode | What happens |
| --- | --- |
| `handoff` | Every run on the issue whose agent the comment does not wake stops (your own run is spared). The comment becomes the state card's handoff note, and B starts a new session that opens with that card. |
| `parallel` | A keeps going; B starts its own run alongside it. |

```bash
multica issue comment add <issue-id> --content-file ./take-over.md --mode handoff
```

`handoff` without an @agent is refused. To give a chat to another agent, run
`multica chat handoff --to <agent> [--session <id|url>]`: it opens a new chat
with that agent whose first message summarises this one, and leaves the old
chat as it is. Only the chat's owner can hand it over.

## Chat runtime status and reply timeouts

`multica chat send --session <id> "Hello" --output json` returns `runtime`:
`{status, name, last_seen_at}`. `last_seen_at` is an RFC3339 timestamp or null
when no heartbeat is known. Offline sends are accepted with `warning` in JSON;
the same warning goes to stderr. Do not send the message again because of this
warning: the message and its queued task already exist.

`multica chat send --session <id> "Hello" --wait --wait-timeout 20s --output json`
exits nonzero on timeout and preserves the send result plus `task_status`,
`reason`, `queue_ahead` and `runtime`. Diagnostics refer to this send's task,
not a later message in the session. `task_status` is the stored task state;
`reason` can be `runtime_offline`, `running_no_logs`, `running`,
`queued_behind_N` (`queue_ahead` is N preceding turns in this chat),
`awaiting_dispatch`, `waiting_local_directory`, `retry_scheduled`, or the
terminal failure reason. A completed task reports `completed`, even when the
reply has not yet appeared. No ETA is implied by queue position.

Read the latest turn again with:

```bash
multica chat progress --session <id> --history --output json
```

This returns progress history plus the same current task diagnostics and
runtime last-seen time. An empty chat has no `task_status` or `reason` yet.

## Unanswered Bot wakes

Separate from the agent handoff budget above, `bot_wake_chain_limit` caps
unanswered automatic Bot wakes across a trusted root (default 4, range 1–100).
Queued wakes reserve a slot; terminal tasks that never ran release it. An actual
agent reply or completed running task clears the count. Human sends bypass it;
`chat send` and `issue comment add --per-quote` also bypass when the quote exactly
matches the caller's original human message in that same chat/issue.

A capped `chat send` retains the message and receipt, reports `mode=blocked`
and exits nonzero. Do not duplicate that message. A saved issue comment warns
with each target's `bot_wake_chain_limit` outcome. Wait for a reply/completion or
a human message; a workspace administrator can change the limit using
`multica settings set bot_wake_chain_limit --value-json '6'`.
`multica overview --output json` and routing health expose the current limit
and visibility-scoped recent blocks for patrol/daily reports. The initiating
Bot also receives a `wake_blocked` operational webhook event.
