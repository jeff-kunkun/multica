# Universal MCP: local implementation evidence

Status: test packages built, **not deployed to Multica test**. The human's
2026-10-13 follow-up replaces the wait for a test host with building and delivering
test packages immediately. The prior test-address blocker no longer applies.
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

The requested deliverable is now an installable test build, not a deployment.
Real external Bot/group delivery, duplicate push and reconnect remain untested
against live receivers. Keep the old webhook in parallel until that
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
contracts remain unchanged. The package delivery supersedes the earlier deployment wait; parent-branch integration follows the issue close contract.

## Test packaging (2026-10-13)

- Source `fd24d9dd67`; local-only tag `v0.6.4-test.dene2002.1`.
  Packaged version `0.6.4-test.dene2002.1-dirty`: the dirty suffix records the
  pre-existing runtime AGENTS.md; no uncommitted application change was built.
- Frozen pnpm install, Web production build (including TypeScript), Electron
  production build and Apple Silicon DMG packaging passed. Mac arm64 and Linux
  amd64 server/migrator/CLI binaries were built with matching version/commit.
- The packaged app passed `codesign --verify --deep --strict`; its plist version
  matches the bundled CLI. The bundled `mcp connections --help` exposes the new
  commands; the packaged renderer contains the new connection API. `hdiutil verify` reports a valid DMG checksum. No install or launch
  against the user's existing login was performed.
- DMG: 241144797 bytes; SHA-256
  `1118a81a521322f54e3ddf3e32a1c6293b22b572bf02fb5d61a8f6621fc1c984`.
  Task attachments enforce 100 MiB, so the DMG is delivered as three ordered
  parts with a checksum file and reconstruction instructions.
- Server archives include migrations, licenses and startup instructions for a
  dedicated test database. No environment files or existing credentials ship.
  Desktop alone does not upgrade the server. No release feed/tag was pushed.

### Verified task attachments

All eight upload responses match local size and SHA-256.

- [multica-mcp-test.dmg.part-00](https://ai.ferryway.cc/api/attachments/01a133c8-d5d1-71dc-ab53-bc0e0268c7e9/download) — 94371840 bytes; SHA256 `35547284a4614f91813a826b07190559fe825446a7cdaed0c4e10840676ce233`.
- [multica-mcp-test.dmg.part-01](https://ai.ferryway.cc/api/attachments/01a133c8-1a2b-75a0-bd09-1bbff8fe1f56/download) — 94371840 bytes; SHA256 `5b3be6890bc9b0d0c668053fcfe67949cf25906b98fd9919ab44e71dd2a686b3`.
- [multica-mcp-test.dmg.part-02](https://ai.ferryway.cc/api/attachments/01a133c7-842f-7065-bf45-2cd67c0de0ef/download) — 52401117 bytes; SHA256 `e0bf5e0ac72b71b58ad5150a3e09ead7a56032cddf6ecfc9d2f721937c8a414f`.
- [desktop-sha256.txt](https://ai.ferryway.cc/api/attachments/01a133c5-c841-7b7b-beef-304e5739c9a2/download) — 87 bytes; SHA256 `26b11461a036bff5ddc6d05afb0d448936013206aa36359a6ecc8061c62c95b8`.
- [安装说明.txt](https://ai.ferryway.cc/api/attachments/01a133c5-c6e4-739d-9c23-ba135a125430/download) — 1251 bytes; SHA256 `78dabc5ba7bcf2ba414a84edd0db6d329ba2367f8fc2f0893c240974479e8e72`.
- [server-darwin-arm64.tar.gz](https://ai.ferryway.cc/api/attachments/01a133c6-4598-7ddc-a1d2-1a89e9550509/download) — 33799312 bytes; SHA256 `b9170a2550081857f1de02d11879be2923caffb42aaf1d433d00dd9759bfa266`.
- [server-linux-amd64.tar.gz](https://ai.ferryway.cc/api/attachments/01a133c6-8e6d-7604-a452-812e39baa352/download) — 35619167 bytes; SHA256 `9be630307a729925b02a3c93c35645efc7401af1ef2d4a96207750ededeec22b`.
- [INSTALL.txt](https://ai.ferryway.cc/api/attachments/01a133c5-e081-70d5-82ba-c59c3b55381a/download) — 2541 bytes; SHA256 `eb91938ce96e2f3d2a8432477d304541fbbd461675231e8625eaefc33a24dc41`.
