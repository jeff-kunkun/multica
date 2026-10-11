# Local MCP adapter

`multica mcp serve` is the stdio adapter in the existing Multica binary. It calls
existing HTTP APIs through `cli.APIClient`; it does not invoke CLI subprocesses,
implement authorization, issue credentials, or change daemon liveness.

## Connect

Use an existing login on the **runtime host**, or the identity injected into a
managed task. Never copy the host's broader identity into a task. Configure a local
MCP client with:

```json
{"mcpServers":{"multica":{"command":"multica","args":["mcp","serve"]}}}
```

Web/Desktop: Settings → MCP supports the existing stdio command/arguments fields.
Add the entry to the workspace library, then assign it in the agent's MCP tab.
A library entry alone reaches no agent. CLI configuration uses the existing
`workspace mcp add` and `agent mcp add` commands (`--help` describes their flags).
There is no new key screen or authentication flow. Existing task/runtime access
restrictions remain authoritative, including read-only credentials that cannot
POST to the fast-access read endpoint.

The native mobile app has no matching local MCP process/configuration entry; it
cannot launch a host stdio executable. Its existing Activity screen still uses
the same fast-access service. This change adds no visible UI or navigation, so
there is no new responsive screen to verify. Remote MCP serving is out of scope.

## Tools and bounds

| Tool | Input and output |
| --- | --- |
| `changes_read` | Optional scope/since or opaque cursor, limit 1–200 (100 default). Returns unchanged v1 fast-access envelope. |
| `runs_list` | Optional scope or cursor, limit 1–200. Active runs only, same envelope. |
| `chats_list` | Optional workspace/since or cursor, limit 1–200. Same read with `include=["chats"]`. |
| `issue_get` | Issue UUID, optional workspace and full. Summary plus attachment references; description capped at 200 characters unless full. |
| `comments_list` | Issue UUID, optional thread UUID, limit 1–50 (20 default). Root summary directory by default; thread includes root plus at most limit replies. Full requires a thread. |
| `chat_history` | Session UUID, before, limit 1–50 (20 default), optional full. Summary and one message page with next_cursor. |
| `comment_create` | Issue UUID, content, optional parent UUID. Accepted comment ID; existing server rules can notify/wake agents. |
| `chat_send` | Session UUID, content, explicit steer/queue/restart. Accepted message ID, actual mode and queue state. |
| `attachment_download` | Attachment UUID, optional workspace. Verified private local path, size and SHA256. |

Single-object tools default to the configured workspace. Scope mode is
`all_visible` or `workspace_ids` with UUIDs. Obtain IDs from changes/runs/chat
results and attachment references, never from fabricated examples. Server
permissions are checked on every request, including writes; MCP annotations do
not grant authority. Unknown tool arguments are rejected by JSON Schema.

No read marks chats read. No automatic pagination or polling occurs. Fast-access
cursors retain their server scope and completion semantics: store checkpoint
only after complete; clear cached summaries and positions on reset/scope changes.

Every JSON API body and logical result payload is bounded to 50000 bytes (MCP
wraps the payload as both text and structured content). A too-large
response fails explicitly instead of emitting partial JSON. Historical reads
use the existing API's page limits. Root directories use that API's existing
hard cap and propagate `comments_truncated`; `offset` / `next_offset` slice its
live root window, **not** a durable cursor or a guarantee of complete history.
A root directory exceeding the byte cap must be narrowed to a known thread.
Thread cursors preserve both `before` and `before_id`. Default previews retain
`content_truncated`. Explicit full text is still subject to the byte bound;
reduce the page size when needed. This adapter does not add a second history
index or fetch an unbounded transcript.

Writes accept up to 32000 UTF-8 bytes. Each is sent once, with redirects disabled.
Permission refusals and 429 are tool errors (`isError=true`); retry_after is
preserved. Transport failures, timeout, invalid successful responses and 5xx
return `ambiguous_write`: the server may have committed. The adapter performs a
bounded read with a fresh 10-second context and returns it as `reconciliation`,
alongside the checking tool/arguments. Inspect it and follow history pages before
deciding to send again; absence from one page is not proof of failure. There is
no invented idempotency guarantee. Server-generated canonical_url is retained
when present; legacy endpoints do not supply it, and the adapter does not invent
workspace-slug URLs.

Downloads reuse `DownloadFile` and the CLI's shared size/SHA256 verifier (100 MiB
maximum). Only verified bytes are written, into a unique 0700 directory and 0600
file beneath the operator's `--download-dir` (default `./attachments`). The tool
cannot choose an arbitrary destination. Signed URLs, credential fields and raw
transport/server diagnostic bodies are excluded from tool results. These are
local files; the operator owns their retention.

## Protocol and verification

Uses the official [Go SDK](https://github.com/modelcontextprotocol/go-sdk), v1.8.0.
The current [2026-07-28 revision](https://modelcontextprotocol.io/specification/2026-07-28)
uses discovery and per-request metadata; the SDK also supports the legacy
[2025-11-25 initialization](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle).
Stdio stdout contains only protocol messages. There is no HTTP listener, OAuth
implementation or confusion between application SSE and MCP transports.

`go test ./internal/fastmcp -run TestMCPStdioAcceptance -v` launches a real SDK
client and stdio server process. To verify the shipped command itself:

```sh
go build -o /tmp/multica-mcp ./cmd/multica
MCP_TEST_BINARY=/tmp/multica-mcp go test ./internal/fastmcp -run TestMCPStdioAcceptance -v -count=1
```

The fixture checks both protocol revisions, nine discovered schemas, reads,
explicit full text, page/truncation signals, writes, permission refusal, 429,
write timeout and exactly one reconciliation read, input rejection before API
access, response budgets, and verified/corrupt attachments. It uses synthetic
identities and never writes production messages. Production deployment and
workspace-specific permissions remain the parent delivery's integration scope.

The adapter belongs in Multica because its authenticated client and integrity
checks already live here. Public protocol/example material can be distributed
separately without copying authentication code or private workspace metadata.
