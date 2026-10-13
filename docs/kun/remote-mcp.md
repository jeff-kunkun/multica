# Remote MCP connections and office notification routes

DENE-2002 adds an out-of-band, single-use authorization flow to the existing
MCP tools. It is **not an OAuth implementation**. A client must support a
configured HTTP Bearer token or an onboarding helper that exchanges the code.
OAuth-only connector UIs cannot complete this flow automatically.

## Scope and acceptance

The approved implementation scope is remote MCP access, office-event catch-up
and receipts, project notification routing with a fallback, Web/Desktop and
CLI controls, then deployment to an explicitly identified **test** environment.
Production, existing keys, logins and webhook routines must remain unchanged.
A successful local run is not evidence of a test deployment or a real external
Bot forwarding to a group.

Acceptance requires single-use/expiring authorization, workspace/Bot isolation,
revocation without affecting old credentials, real tool discovery and issue
read/write, reconnect using durable event IDs/cursors, duplicate receipts without
new comments, and exactly one responsible notification Bot. External Bot receipt
and real group delivery require a test receiver and its real group reference.

## Connect once

In Web/Desktop **Settings → Bots → MCP connections**, select an active owned
Bot and give the connection a name. The response shows an endpoint, exchange
URL and a code valid for five minutes. The code exists in temporary component
state only; it is not cached with server data. Closing the dialog clears it.
The issuing owner/admin can list or revoke the connection; a legacy Bot retains
its owner's read-only inventory for overview compatibility. A Bot, task
or other machine credential cannot issue a connection.

CLI uses the same APIs and validation:

```sh
multica bot list --output json
multica mcp connections create --bot <bot-uuid> --name 'Test connector' --output json
multica mcp connections list --output json
multica mcp connect --exchange-url https://<test-server>/api/mcp/exchange --code-stdin
multica mcp connections revoke <connection-uuid> --output json
```

Supply the code through protected stdin. `connect` prints client configuration
containing a **new connection credential**; keep that output private. It does
not save, replace or rotate an existing Multica login. A code is consumed in
one database update; a replay or simultaneous second exchange fails. If an
exchange response is lost, create a new connection and revoke the old one;
do not retry an ambiguous exchange as though it had not committed.

Codes and credentials are stored only as SHA-256 hashes. Connection credentials
expire after 90 days. Revocation, stopped/deleted Bots, membership removal and
loss of owner/admin role are checked against the database on every request.
A connection is bound to one workspace and one Bot identity; authorship remains
that Bot acting for its owner, so its receipts are not treated as human replies.
No legacy Bot key is handed out or changed by this flow.

## Transport and tools

`POST /api/mcp/exchange` exchanges `{code}` for
`{access_token,token_type:"Bearer",expires_in,endpoint}`. It is rate-limited and
returns `Cache-Control: no-store`. Management endpoints live at
`/api/workspaces/{id}/mcp-connections` (GET/POST), with DELETE on `/{connectionId}`.
Management GET never returns codes, hashes or credentials.

The returned endpoint is `/api/workspaces/{id}/mcp`. It serves Streamable HTTP
using the official MCP Go SDK in stateless JSON mode. MCP authorization is
restricted to this resource; the same credential cannot call REST over the
network. Tool calls dispatch **in-process**, through the normal REST auth,
membership, visibility, write-audit and mutation guards, with the fixed workspace
and Bot identity. No upstream network request or client-supplied destination is
used. Fast-access scopes cannot escape the authorized workspace through links.
Each HTTP request gets its own MCP server instance; sessions cannot share caller
identity. Browser Origin requests are refused; this endpoint is for connector
backends. HTTPS is required outside localhost.

The existing read/message tools are available. `attachment_download` remains
local-stdio-only, because a server filesystem path is not an external artifact.
The remote `attachment_read` tool returns a bounded base64 chunk (up to 16 KiB),
`next_offset`, `complete`, whole-file size/SHA256 and a chunk SHA256. Assemble
contiguous chunks and verify the complete file before sending it. Each chunk
rechecks access; URLs from metadata are never followed. It accepts only files
up to 100 MiB with a recorded digest and exact range responses. No server-local
path is exposed. Four tools
cover the office stream:

| Tool | Contract |
| --- | --- |
| `events_list` | One bounded page after a nonnegative stream sequence; persist each event before saving its next cursor. |
| `event_get` | One visible event and its authorized delivery records. |
| `events_deliveries` | One Bot's durable delivery ledger; recover unfinished work after a disconnect. |
| `events_receipt` | `received`, `relayed`, `handled`, or `relay_failed`; uses the existing server receipt validation. |

Deduplicate by `event_id`, and inspect `business_key` for legacy-lane overlap.
Do not suppress a `must_relay` event using semantic similarity. Record receipts
through `events_receipt`, never through an issue comment. Unknown writes are
not retried automatically; reconcile using `event_get`. The server enforces
relay proof and artifact receipts. HTTP success on a push is not a handled
receipt. Existing webhook pushes remain the outbound wake mechanism; MCP
stateless JSON itself does not create an unsolicited event notification channel.
Do not turn off the old webhook routine until a real receiver comparison proves
that no results are missing.

## Notification responsibility

Settings → Bots also manages per-project notification routes and a default
fallback. Each owned route selects an owned Bot and the real target group
reference. An empty project selects the fallback. The existing personal Bot
binding remains a legacy fallback when no explicit default route exists.

```sh
multica project list --output json
multica mcp routes list --output json
multica mcp routes set --project <project-uuid> --bot <bot-uuid> --channel-ref <group-reference> --output json
multica mcp routes set --bot <fallback-bot-uuid> --channel-ref <group-reference> --output json
multica mcp routes remove --project <project-uuid> --output json
```

GET/PUT/DELETE `/api/workspaces/{id}/office-bot-routes` is the shared API. PUT
uses `{project_id?:null|UUID,bot_id,channel_ref}`; DELETE uses the optional
`project_id` query parameter. The selected target is frozen as `relay_target` in the delivery record and
delivered in the event payload; the external Bot performs the actual group send and reports its real
message reference. A routed `relayed` receipt must also include `target` equal
to that frozen group reference; `channel` still names the transport. CLI uses
`events receipt --stage relayed --target <group-reference> --channel <transport> --ref <message-reference>`. Saving a route does not prove that a group exists or that
an external Bot can post there. No external group write is performed by settings.

Project comes from the issue first, otherwise the chat primary project; a chat
with several project associations uses that primary project only. The server
selects an eligible project route, then an eligible explicit fallback, then the
legacy binding. It does not guess Bot identities from names.

Keep a single responsible delivery. An uncertain send must be reconciled, not
silently transferred to another Bot that could repeat it. Changing a route
blocks old deliveries rather than redirecting them; restoring the exact Bot
and group permits recovery. New routes can recover historical recipient gaps.
Gap recovery and legacy recovery serialize with event sealing in a transaction.
A missing webhook is reported and can still be recovered through the delivery
ledger; timeout/uncertain external sends do not transfer automatically. Pending approval or
an automatically completed issue does not authorize a production deployment.

## Surfaces and compatibility

Web and Desktop use the same settings components and Query/API layer. Mobile
web uses the same dialogs; native mobile has no Bot-management surface and is
not given a new one in this change. Old stdio configuration remains valid and
now includes event tools. New tables are additive; no token or session row is
rewritten. `multica capabilities --output json` discovers the registered MCP
commands; overview includes permission-filtered connection and route inventory.

Authorization boundary reference: [MCP authorization specification](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2025-11-25/basic/authorization.mdx).
This custom code flow does not claim OAuth discovery or OAuth-only client compatibility.
