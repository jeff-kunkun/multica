# Bot activity stream (DENE-1935)

Follow-up A to the human-needed channel ([bot-wake-contract.md](bot-wake-contract.md#human-needed-events-dene-1933)).
A person who connected their own Bot and switched a scope on gets what happens
in their tickets and chats pushed to that Bot as it happens. The Bot decides
what reaches the person; the server only tags each event and keeps secrets out.

## Who gets it

Opt-in per person, on top of a verified Bot binding. `activity_scope` on the
binding is `[]` (off, the default), `["owned"]`, `["followed"]` or both. People
without a verified Bot, or with the scope off, see no change at all.

- `owned`: tickets the person created or is assigned (directly, or through a
  squad they created, or the chat the ticket came from), and chats they own.
- `followed`: tickets they actively subscribe to. Chats are owner-only.

Set it with `multica bot bind --activity owned|followed|owned,followed|off` or
the account settings row「把所有动态推给我的 Bot」. `PUT .../me/bot-binding`
takes `activity_scope`; omitting it keeps the current value.

## What is pushed

| `activity_type` | Source |
|---|---|
| `comment` | a comment on a ticket |
| `chat_message` | a message in a chat session |
| `status_changed` | ticket status moved (`from_status`, `to_status`) |
| `assignee_changed` | ticket assignee changed |

Never pushed: the person's own actions, anything their own Bot wrote, a comment
that was already pushed as `human_needed`, chat delivery notices.

Webhook body: the usual envelope (`event_id`, `sequence`, `woken_url`,
`handled_url`, `summary`, `author`, `issue`/`chat`) with `type: "activity"`,
`schema: "multica.activity/v1"` and an `activity` block:

```json
{
  "activity_type": "comment",
  "importance": "high",
  "categories": ["risk"],
  "excerpt": "…first 1000 chars, secrets masked…",
  "source_kind": "issue",
  "source_title": "…",
  "source_url": "…",
  "source_id": "<comment or message id>",
  "from_status": "", "to_status": "",
  "occurred_at": "2026-10-12T06:30:00Z",
  "target_user_id": "<the person>"
}
```

`importance`: `critical` (reserved for `human_needed`, never used by activity),
`high` (blocked, risk/decision wording, sensitive actions), `normal`, `low`
(started, backlog/todo, assignee changes). `categories` (one or more):
`needs_human`, `risk`, `decision`, `result`, `progress`, `completion`,
`failure`, `info`. The tags are a hint for filtering, not a judgment.

## No secrets

Every free-text field passes `MaskSecrets` before it leaves: PEM blocks, known
key prefixes, bearer tokens, `password=`/`token=` assignments, passwords in
URLs and long random runs are replaced; commit SHAs, UUIDs, URLs and paths are
kept. Attachments and raw message bodies are not pushed, only the excerpt.

## Delivery

Rides the human-needed outbox, so delivery, backoff and expiry are shared, but
nobody chases an activity: HTTP 200 means delivered (`sent`) and it is never
resent, escalated or nudged. At send time the server re-checks that the scope
is still on, the person can still see the ticket, and still owns the chat;
otherwise the event is cancelled. `human_needed` is always claimed first.
Dedupe is per source (`act:comment:<id>`, `act:chat:<id>`, status and assignee
changes by ticket and timestamp), so one thing is one event.

Retention: unsent rows expire after 72 h; finished rows are purged after 14
days. Low-importance events stop queuing when a Bot already has 2000 waiting
(higher importance is never dropped). No 30-second grouping and no hourly limit
apply to the stream; batching is deferred.

## Receipts and catch-up

Optional: `POST /api/workspaces/{id}/bots/{botId}/wake/events/{eventId}/handled`
`{reason}` closes an activity the Bot looked at and decided nobody needs telling
about. `GET .../bots/{botId}/activity?status=&importance=&limit=&before=` lists
the rows still held (default `queued,sent,woken,relayed`), newest first, for
catching up after downtime.

```sh
multica bot bind --activity owned,followed
multica bot wake activity <bot-id> --importance high
multica bot wake handled <bot-id> <event-id> --reason "routine progress"
```

## Not in this step

PRs, runs, projects, settings and workspace members/agents, and a Bot acting
with the owner's full permission (audited as on their behalf) are step B.
