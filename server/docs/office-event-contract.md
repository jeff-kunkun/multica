# Office event stream (DENE-1963)

One durable stream of what happened in a workspace: chat replies, run endings,
ticket status, delivered files, and settings / member / routing / config
changes. Every event has an `event_id`, a gap-free `stream_seq`, the real actor,
and, when a Bot acted for a person, both of them. A Bot can pull it with a
cursor, and gets the events meant for its owner pushed one by one until it
posts a receipt.

It sits next to the older lanes ([bot-wake-contract.md](bot-wake-contract.md),
[bot-activity-contract.md](bot-activity-contract.md)); those keep working. The
same business fact carries the same `business_key` on every lane, so a Bot that
gets it twice shows it once.

## Capture

Database triggers write the event **in the same transaction** as the business
write: if the write committed, the event is there, even if the server died a
moment later. Nothing is captured on the read replica, and nothing during a
workspace teardown.

| Family | `event_type` | Source |
|---|---|---|
| `chat` | `chat.replied` | a message an agent or Bot posted in a chat |
| `chat` or `issue` | `run.completed`, `run.failed`, `run.cancelled` | an agent run ended (family follows where it ran) |
| `issue` | `issue.status_changed` | ticket status moved |
| `artifact` | `artifact.attached` | a file landed on a ticket, a comment or a chat message: on upload or bound later; once per file, so a new version under the same name is a new event |
| `config` | `config.<table>.<insert\|update\|delete>` | workspace settings, members, agents, squads, projects, Bot bindings and webhooks, tokens, MCP servers, skills, statuses, properties, runtime profiles, shares, links, GitHub installations |

The actor comes from the request that made the write (person, agent, Bot key
acting for its owner, or a named system job such as `routing`). Config events
list each changed field as `{field, old, new}`; secrets, tokens, keys and
passwords are `[已打码]` with `masked: true`, never the value.

A background worker seals new rows: it assigns `stream_seq` under one lock (no
gaps, no reordering) and decides who gets a push, in the same transaction.

## What an event says

```json
{
  "schema": "multica.office_event/v1",
  "event_id": "…", "stream_seq": 1042,
  "family": "issue", "event_type": "issue.status_changed",
  "meaning": "task_done",
  "summary": "…",
  "must_relay": true, "bypass_semantic_dedupe": true,
  "pending_approval": false,
  "business_key": "issue-status:<id>:in_review>done:<µs>",
  "actor": {"type": "agent", "id": "…", "name": "…", "source": "",
            "via_bot_id": "…", "on_behalf_of_user_id": "…", "task_id": "…"},
  "changes": [{"field": "status", "old": "in_review", "new": "done"}],
  "issue": {"id": "…", "key": "DENE-1", "title": "…", "status": "done"},
  "web_url": "…", "event_url": "…",
  "artifacts": []
}
```

`meaning` separates what people confuse:

| `meaning` | Means |
|---|---|
| `round_done` | one run finished; the ticket may go on |
| `handoff` | handed to review or to someone else |
| `task_done` | the ticket is finished |
| `reply` | an answer arrived in a chat |
| `blocked` / `failed` | stopped and needs someone / a run failed or was cancelled |
| `artifact` | a file was delivered |
| `release_pending_approval` | see below |
| `change` | anything else that moved |

`must_relay: true` marks a result the person has to hear about: chat replies,
handoffs, finished or blocked tickets, failed chat runs, files an agent
delivered, pending releases. It always comes
with `bypass_semantic_dedupe: true`: do not fold it into a similar earlier
message.

### Release work finished by routing

When routing (not a person) moves a ticket whose title or description is about
a production or formal release to `done`, the event is
`meaning: release_pending_approval`, `pending_approval: true`, `must_relay:
true`, and carries `notice: "系统改成完成，正式发布仍待批准。这不是发布许可。"`.
It grants nothing: do not deploy on it.

### Files

A chat reply lists the files on its message, a run the files it uploaded, a
file event its own file, and a ticket that finished, was handed over or waits
for release approval every file on the ticket. Push and pull build the list
the same way.

Each entry in `artifacts`: `id`, `filename`, `content_type`, `size`, `sha256`,
`status` (`ready`, `unverifiable` when no checksum was recorded, `deleted`),
`download_url` (needs the same credentials as the API; never anonymous),
`metadata_url`, and the `cli` line that downloads it
(`multica events download <attachment-id>`).

## Who gets a push

- A ticket's or chat's owners (creator, assignee, the chat it came from) get
  its results and run endings through their own verified Bot.
- A ticket that names no person (an agent created and runs it) belongs to the
  owners of the nearest parent ticket that names one, up to 10 levels; only
  those who can open this ticket count, so a private sub-ticket stays a gap.
  A ticket with its own owner is never handed to the parent's. A missing
  parent, another workspace or a loop ends the walk.
- People who stream everything (`multica bot bind --activity everything`) also
  get every event they can see.
- Settings and members: owners and admins only. A ticket: whoever can open it.
  A chat: its opener. Re-checked at every attempt; a recipient who lost access
  is blocked, never sent.
- Nobody is told about their own action. An agent's result is not the
  person's own action, even when their Bot started the run.
- An owner whose Bot is connected but not verified still gets a row, blocked
  with the reason: it reaches their inbox and goes out by itself once the
  Bot is verified.
- An owner of a must-relay result with no Bot (never connected, or
  disconnected) gets a gap row of their own, so another owner's Bot never
  hides it. Someone who has used a Bot here gets one in-app reminder; when
  they connect and verify a Bot again (no age limit), the result is
  delivered to it. A row blocked because the connection went away is
  requeued on reconnect, or moved to the new Bot if they switched.
- A must-relay result carries `recipient_gap` when it has no owner at all,
  or an owner without a Bot.
- If any of these lookups fails, the event is not sealed; it is tried again,
  never sealed with recipients missing.

## Push and receipts

The push is a webhook POST to the Bot's wake URL (same signing as wake), body
= the event plus a `delivery` block: `attempt`, `redelivery`, `bot_id`,
`reason` (`owner` or `stream`), `must_relay`, `target_user`, `receipt_url`.

**HTTP 200 is not a receipt.** Until the Bot posts one, the same `event_id` is
sent again: 2, 5, 15, then every 30 minutes after a 200; 15 s, 1, 3, 10, then
every 30 minutes after a failure. It never expires. Dedupe on `event_id`.

When a request went out and its answer was lost (timeout after sending, some
5xx), the Bot may already have it. That is not treated as a failure: the next
push waits like after a 200, and carries `delivery.reconcile: true`, so the Bot
looks the `event_id` up in its inbox, re-sends the receipt it has, and does
only what is still missing. `uncertain_at` on the delivery records it. The
same holds for a claim left in `sending` by a server restart, and for any
resend after a 200: every push of something already sent carries
`reconcile: true`.

The Bot is expected to store the event in its own durable inbox before it
answers `received`, and after a restart to list its open deliveries and
finish them from that inbox.

```text
pending → sending → sent (200) → received → relayed | handled
                         ↘ blocked (reason, attempts, last error, next attempt)
```

`POST /api/workspaces/{ws}/bots/{bot}/office-events/{event}/receipt`

| `stage` | Means |
|---|---|
| `received` | the Bot has it; stops resending |
| `relayed` | the Bot told the person. `channel` and `message_ref` required (400 `relay_proof_required`). The server records the Bot's claim; it cannot see the chat app |
| `relay_failed` | could not tell them (`error`, required); counts as received. Never undoes a `relayed` |
| `handled` | nobody needs telling (`reason`). **Refused with 409 `must_relay`** on a must-relay event that was not relayed first |

`artifacts: [{id, status: sent|failed|skipped, ref, error}]` records each file
separately: only files in this event's `artifacts`, `sent` needs `ref`,
`failed` needs `error`, each file once per receipt (else 400
`invalid_artifact_receipt`). A file once `sent` stays sent: receipts merge
under the row lock, so a late, repeated or concurrent receipt cannot take it
back.

A must-relay result still not `relayed` or `handled` five minutes after it was
queued, or blocked, goes to the person's inbox once, if they can still see
it.

Blocked deliveries keep their reason (Bot disconnected, key revoked, no or
unusable webhook, recipient lost access, stream switched off, Bot rejected),
survive restarts, and come back with a retry once fixed.

## Pull

| Call | |
|---|---|
| `GET /api/workspaces/{ws}/office-events?after=<seq>&limit=&family=&issue_id=&chat_session_id=` | events you can see, oldest first; `next_cursor` |
| `GET /api/workspaces/{ws}/office-events/{event}` | one event; deliveries (all for admins, else yours) |
| `GET /api/workspaces/{ws}/bots/{bot}/office-deliveries?status=&before=` | a Bot's ledger |
| `POST /api/workspaces/{ws}/bots/{bot}/office-deliveries/retry` | requeue blocked/pending (`event_id` for one) |
| `GET /api/workspaces/{ws}/me/office-deliveries` | your own Bot's ledger (people only) |
| `POST /api/workspaces/{ws}/me/office-deliveries/retry` | same, for you |

CLI: `multica events list|get|download|receipt|deliveries|retry`
(`--help` on each, `--output json`). Settings → Notifications →
「要我决定的事」→「送达记录」 shows your ledger with retry.
