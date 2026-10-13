# Universal MCP: local implementation evidence

Status: local candidate, **not deployed to Multica test**. The human approved
direct test deployment, but this checkout provides no identified test host or
deployment entry. An asynchronous request for that location remains unanswered.
Production, existing credentials/logins and external Bot routines were not changed.

Implementation contract: [remote MCP](../../kun/remote-mcp.md).
[Mobile/desktop screenshots and interaction report](https://ai.ferryway.cc/api/attachments/01a13360-7c97-7120-ab18-dc4904306325/download)
is a standalone HTML attachment, with real local screenshots and no active code.

## Observed validation

- Disposable PostgreSQL tests: single-use/concurrent exchange, expiry/revoke,
  stopped Bots, legacy credential compatibility, MCP discovery/read/write and
  authorship, workspace/attachment isolation, event routing, exact-target
  receipts, durable gap recovery and concurrent restore serialization.
- Real MCP SDK: local and remote discovery, bounded event schemas, receipt
  errors, write dispatch, file chunk reconstruction/range/hash/redirect checks.
- CLI: connection and route command tests; compiled binary exposes all seven
  new commands in `capabilities`, and `events receipt --help` describes `--target`.
- Middleware/service: MCP workspace clamp, reads excluded from write audit,
  receipts excluded from workspace-write notification loops.
- Shared settings: 22 UI tests and five API-schema tests; views typecheck and
  targeted lint passed. Real browser created/closed/revoked a connection and
  saved/edited/removed a default notification route. Screenshots at
  375/390/768/1280 CSS pixels were opened and inspected.
- Migration allocation and `make migration-lint` passed; generated sqlc updated.
- `git diff --check` excludes the pre-existing runtime additions to AGENTS.md;
  those additions are not part of this delivery.

Final integration self-check found overview inventory differed between a legacy
Bot and its owner. Inventory reads now retain the existing owner-equivalence
contract while authorization/route mutations remain human-only; the existing
real-auth regression is the gate for that correction.
The final disposable-database run passed: handler tests 2.109s, server/router
tests 0.950s (`TestMCP|TestOfficeBotRoute|TestOfficeEvent|TestOverview|TestBotWriteAuditCoversEveryWriteRoute`).

## Remaining acceptance

Identify the test target and its deployment mechanism, deploy the additive
migrations and server/web build there, connect the actual external Bots, and
exercise real group delivery, duplicate push and reconnect with those receivers.
Only then notify test is online. Keep the old webhook in parallel until that
comparison proves no missed notifications. This change does not deploy or
claim OAuth-only client compatibility; it implements the approved single-use
authorization alternative. Native mobile has no Bot-management screen and was
not expanded. Standalone Electron/native-device execution and full repository
test suites were not run.

## Knowledge audit

`docs/README.md` and `docs/evidence/INDEX.md` link the new contract and evidence.
The built-in `multica-platform` external-Bot reference documents onboarding,
routes, event receipts and remote attachments; no live skill distribution ran.
No new shared domain term or durable cross-project architectural decision was
introduced, so CONTEXT.md/ADRs were not changed. Shared design/navigation
contracts remain unchanged. Preserve this task branch for deployment resumption.
