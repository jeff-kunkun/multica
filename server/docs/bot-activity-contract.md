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
| `comment.created` | a comment on a ticket |
| `comment.edited` | a comment's text was edited (once per edit; attachment, resolve and delete changes are not edits) |
| `chat.message` | a message in a chat session |
| `status.changed` | ticket status moved (`from_status`, `to_status`), whoever moved it: a person, an agent, or a background job |
| `assignee.changed` | ticket assignee changed |

Never pushed: the person's own actions, anything their own Bot wrote, a comment
that was already pushed as `human_needed`, chat delivery notices.

Webhook body: the usual envelope (`event_id`, `sequence`, `woken_url`,
`handled_url`, `summary`, `author`, `issue`/`chat`) with `type: "activity"`,
`schema: "multica.activity/v1"` and an `activity` block:

```json
{
  "activity_type": "comment.created",
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

The finished body is masked as a whole just before it is sent: every string in
it (summary, author name, ticket and chat titles, excerpt, the human_needed
question, option ids and labels) goes through `MaskSecrets`, not only the
excerpt. What is kept verbatim is kept by its shape, never by its key name: a
whole-string UUID or RFC 3339 time, and an `http(s)` link without
`user:password@`, which is masked piece by piece between the slashes so receipt
links keep working. An option id is whatever the asker typed, so one that holds
a credential is refused when the question is asked (HTTP 400) and dropped if it
still reaches the outbox. The same pass covers the two pull lists
(`.../activity` and `.../human-needed`).

`MaskSecrets` replaces PEM blocks, known key prefixes, bearer tokens, passwords
in URLs and long random runs. For a credential field (`password`, `passwd`,
`secret`, `token`, `api_key`, `密码`, `口令`, …) the value is masked from its
first character and a quoted value is masked whole, spaces, newlines and
escaped quotes or backslashes included (the way `json.Marshal` writes them), so
`{"password":"ab"}`, `{"password":"two words"}` and `{"password":"a\"b\\c"}` all
lose the value. It errs
toward masking; commit SHAs, UUIDs, URLs and paths are kept. Attachments and raw
message bodies are not pushed, only the excerpt (1000 characters at most).

## Delivery

Rides the human-needed outbox, so delivery, backoff and expiry are shared, but
nobody chases an activity: HTTP 200 means delivered (`sent`) and it is never
resent, escalated or nudged. At send time the server decides again, against
the stream as it stands now: the binding is still verified and not revoked, the
person can still see the ticket, and the current scope still covers the source
(`owned`: still creator, assignee, squad lead or chat opener; `followed`: still
subscribed; a chat is only its opener's). Otherwise the event is cancelled and
not sent. `human_needed` is always claimed first.
Dedupe is per source (`act:comment:<id>`, `act:comment-edit:<id>:<revision>`,
`act:chat:<id>`, status and assignee changes by ticket and timestamp), so one
thing is one event.

Retention: unsent rows expire after 72 h; finished rows are purged after 14
days. Low-importance events stop queuing when a Bot already has 2000 waiting
(higher importance is never dropped). No 30-second grouping and no hourly limit
apply to the stream; batching is deferred.

## Receipts and catch-up

Optional: `POST /api/workspaces/{id}/bots/{botId}/wake/events/{eventId}/handled`
`{reason}` closes an activity the Bot looked at and decided nobody needs telling
about. `GET .../bots/{botId}/activity?status=&importance=&limit=&before=` lists
the rows still held (default `queued,sent,woken,relayed`), newest first, for
catching up after downtime. The list is filtered by the same send-time decision:
a row whose ticket the person can no longer see, or that the current scope no
longer covers, is not returned. A page that filtering empties is followed by the
next one (up to ten pages per request), so `next_cursor` always resumes where
the scan stopped.

```sh
multica bot bind --activity owned,followed
multica bot wake activity <bot-id> --importance high
multica bot wake handled <bot-id> <event-id> --reason "routine progress"
```

## Not in this step

PRs, runs, projects, settings and workspace members/agents, and a Bot acting
with the owner's full permission (audited as on their behalf) are step B.
