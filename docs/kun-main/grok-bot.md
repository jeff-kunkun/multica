# Connecting Grok (or any Bot) as project manager

A Bot is a workspace member with its own token. It works through the remote
MCP endpoint and learns what happened through a webhook or by catching up on
the event feed. A workspace can have any number of Bots.

## 1. Create the Bot

The owner creates a Bot in **Settings → Bots**, or with the CLI:

```bash
multica bot create --name Grok --can-release \
  --webhook-url https://grok.example.com/multica --webhook-key <receiver key>
```

Webhook URL and Webhook key are the Bot's own receiving address and key
(Grok shows them in its routine's push settings). The office sends the key as
`Authorization: Bearer <key>` with every push, so the Bot does not need to
call `set_webhook`.

The reply shows the three credentials the Bot needs: the office token
(`mul_…`, **shown once**), the Webhook URL and the Webhook key. It also carries
a setup prompt (`setup_prompt` in JSON, **Copy prompt** in Settings) that
lists all three up front, then the MCP URL, a `whoami` / `list_members` /
`list_projects` self-check, how to receive events and the work loop. Forward
it to the Bot as is and it configures itself. The server renders it in
`server/internal/handler/bot_prompt.go`; the URL comes from
`MULTICA_PUBLIC_URL`, then `MULTICA_APP_URL`, then the request's own origin.
`multica bot token` returns a fresh prompt with the new token and the stored
Webhook URL and key. `multica bot token <bot>` rotates the token, `multica bot revoke <bot>`
cuts it off, and `multica bot delete <bot>` removes the Bot and its membership.

`--can-release` lets the Bot approve plans on the owner's behalf. Without it,
the Bot asks for approval and the owner, or a releasing Bot, approves.

## 2. Point the MCP client at the office

| Setting | Value |
| --- | --- |
| Transport | Streamable HTTP (stateless, POST only) |
| URL | `https://office.example.com/api/mcp` |
| Header | `Authorization: Bearer <bot token>` |

The workspace comes from the token, so no workspace header is needed. Only Bot
tokens are accepted: a person's token gets 403.

## 3. Tools

| Step | Tool |
| --- | --- |
| Orient | `whoami`, `list_members`, `list_projects`, `list_issues`, `get_issue`, `list_comments` |
| Create a project | `create_project` |
| Open a topic (an issue that asks an agent for a plan) | `open_topic` |
| Read the plan | `read_plan` |
| Ask the owner to approve | `request_approval` |
| Approve the version you read (Bots with `--can-release` only) | `approve_plan` |
| Add work outside the plan | `create_issue`, `assign_issue` |
| Track progress | `set_status`, `add_comment`, `get_events`, `set_webhook`, `set_manager_webhook` |
| Close | `close_issue` |
| Anything else the CLI can do | `api` (`method`, `path`, optional `body`) |

A typical loop:

1. `create_project`.
2. `open_topic` assigned to a planning agent.
3. Wait for the plan event, then call `read_plan`.
4. Call `request_approval`, or `approve_plan` with the version it read if the Bot may release.
5. Approval creates one ticket per plan step and starts it. Do not create or
   assign them again; follow them through `read_plan` (each step's
   `issue_id`) and `get_events`. Use `create_issue` only for work the plan
   does not cover.
6. Close each ticket with `close_issue` when it is done.

API errors come back as tool errors with the HTTP status, for example
`HTTP 403: ...`. A Bot can do anything a regular member can do, and no more.

### Standing in for its creator

A Bot reads and answers what its creator would otherwise have to: on the chat
(`/api/chat/sessions…`, `/api/chat/pending-tasks…`) and inbox (`/api/inbox…`)
routes the office treats the Bot as the person who created it
(`ActAsBotCreator`). It lists and reads their chats with agents, replies in
them or starts new ones, and reads and clears their inbox, all through `api`.
Its chat messages are prefixed with `[<Bot name>]`. This works only in the
Bot's own workspace and only while the creator is still a member.

## 4. Events

### Webhook

The Bot registers its own receiving address once, so the owner does not
configure one per Bot:

- MCP: `set_webhook` with `{"url": "https://…"}` (an empty `url` stops pushing)
- HTTP: `PUT /api/bot/webhook` with `{"url": "…"}` and the Bot token

Both return `webhook_url` and `webhook_secret`. An address set by the owner
(`--webhook-url` or the Settings page) works the same way.

If the receiver requires its own key, pass `headers` too, for example
`{"url": "…", "headers": {"Authorization": "Bearer …"}}`. They are sent with
every delivery and replaced on every call; they never override
`Content-Type` or `X-Multica-Signature`, and no response echoes them.

The office POSTs batches of up to 50 events, in order:

```json
{
  "bot_id": "…",
  "workspace_id": "…",
  "events": [
    {"id": 12, "type": "issue:updated", "actor_type": "member", "actor_id": "…",
     "payload": {}, "created_at": "2026-10-13T08:00:00Z"}
  ],
  "next_cursor": 12
}
```

### One Bot, several managers

When several managers share one Bot's token, each registers its own receiver
under a name instead of calling `set_webhook`, which would replace the Bot's
main address:

- MCP: `set_manager_webhook` with `{"name": "media", "url": "https://…"}`
  (optional `headers` as above; an empty `url` removes that name)
- HTTP: `PUT /api/bot/webhooks` with the same body and the Bot token;
  `GET /api/bot/webhooks` lists them with their delivery state

Every receiver gets every event the Bot gets, from the moment it is
registered, with its own cursor and retries, so a broken receiver holds up
nobody else. The body is the same as below plus `"receiver": "<name>"`, signed
with the same webhook secret. Filtering is up to the receiver. A Bot holds at
most 20 receivers; deleting the Bot removes them.

Verify every request. `X-Multica-Signature` is `sha256=` followed by the hex
HMAC-SHA256 of the raw body, keyed with the webhook secret:

```python
import hashlib, hmac

def verify(secret: str, raw: bytes, header: str) -> bool:
    want = "sha256=" + hmac.new(secret.encode(), raw, hashlib.sha256).hexdigest()
    return hmac.compare_digest(want, header)
```

Any non-2xx reply is retried with exponential backoff, capped at 5 minutes. A
failed batch is resent before anything newer, so events never skip ahead.

### Catch-up

After a disconnect, read from the last `next_cursor` you processed:

- MCP: `get_events` with `{"after": <cursor>}`
- HTTP: `GET /api/bot/events?after=<cursor>&limit=200` with the Bot token

Keep reading while `has_more` is true. Events are kept for 7 days.

Event types:

- `issue:*`, `comment:*`, `project:*`, `plan:*`, `member:*`
- `task:completed`, `task:failed`, `task:cancelled`
- `inbox:new` for items addressed to the Bot (for example an @mention) or to
  its creator
- `chat:done` when an agent replies in the creator's chats; the reply is in
  `payload.content`

Events addressed to one person carry `payload.for_user_id` and reach only the
Bots that are, or stand in for, that person.
