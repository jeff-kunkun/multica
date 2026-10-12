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

## The whole workspace (DENE-1936, step B)

`owner_visible_all` is a third scope value: everything the person can see in the
workspace, with their permissions. Opt-in like the others
(`multica bot bind --activity everything`, or the settings choice「我能看到的一切」),
and it includes the ticket and chat stream, so nothing else needs switching on.
Anyone who did not choose it sees no change.

### What arrives

Same envelope and the same outbox as above, with an extra `activity.workspace`
block (ids, names, field names only):

```json
{
  "family": "agent", "action": "update", "route": "PATCH /api/agents/{id}",
  "fields": ["name", "instructions"], "entity_id": "<uuid>",
  "actor_kind": "member|agent|bot|system", "actor_id": "…", "actor_name": "…",
  "via_bot_id": "…", "via_bot_name": "…",
  "on_behalf_of_user_id": "…", "on_behalf_of_name": "…",
  "dangerous": "", "attrs": {}
}
```

`activity_type`: `workspace.write` (any successful write), `pr.updated|linked|unlinked`,
`run.completed|failed|cancelled`, `autopilot.run`, and `bot.write_blocked`
(a Bot's dangerous call was stopped; always `high`, tagged `risk`, `needs_human`).
`family`: `pr run project settings member agent autopilot skill runtime
integration squad routing billing access issue other`.

Writes come from one interceptor in front of every authenticated route
(`middleware.BotWriteAudit`), so a route added tomorrow is covered the day it is
added; `TestBotWriteAuditCoversEveryWriteRoute` walks the router and
fails on a route that is not. Ticket writes the ticket stream already reports
(comments, chat messages, status and assignee changes) are not said twice.
Run endings and PR mirror updates come from the event bus. Not pushed: a
runtime flipping online/offline in the sweeper (no write, no bus event), a
chat's own runs, and retry attempts.

Roles: management families (`billing member access integration settings`) go
only to owners and admins; other families go to any member; guests get nothing.
The row is re-checked when sent and when listed: person still a member whose
role may see the family, binding still `owner_visible_all`.

**Field names only.** A settings change carries which fields changed, never the
values; names that look like secrets are dropped, other strings are masked as
above. Payload-scan tests cover the push, the pull list and the audit endpoint.

### Bot permission = the owner's, and every write is labelled

A Bot key acts as its owner, nothing more. Every write made with it is recorded
in `bot_write_audit` as「Bot〈名〉代表〈主人〉」: bot id and name, owner, route
pattern, family, entity id, field names, status, outcome. Rows that Bots author
also carry `on_behalf_of_user_id` next to `via_bot_id` (comment, chat message,
activity log, run, issue). Read it with `multica bot writes [--mine] [--limit N]
[--before CURSOR]` or `GET /api/workspaces/{id}/bot-writes`; owners and admins
see the whole workspace, a member sees their own. The row survives deletion of
the workspace (workspace id is cleared).

### The four that need the owner first

Delete the workspace, change members or roles, reset keys (tokens, rotate,
reset password), change quota or billing. The server stops a Bot's call with
`403 owner_confirmation_required` unless it carries `X-Owner-Confirmation: <short
reference to the owner's message in the Bot chat>` (CLI: set
`MULTICA_OWNER_CONFIRMATION` for that one command). A stopped call is itself
audited and pushed as `bot.write_blocked`. Honest limit: the server cannot read
the Bot chat, so the header is the Bot's attestation, recorded next to the write
it licensed; it is a gate and a paper trail, not proof.

### Not covered

Unverified in production until the release is deployed and a real Grok Bot
receives it. `apps/mobile` has no Bot-binding screen, so there is no native
change; the mobile web page was checked at 390px.
