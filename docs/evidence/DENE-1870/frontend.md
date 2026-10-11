# Shared compact activity view

The existing inbox header now opens Workspace activity. Web and Desktop both render `packages/views/inbox/components/inbox-page.tsx`; no parallel route or platform implementation is needed. The shared dialog uses the existing mobile overlay history, safe-area bound, and 44px action targets.

The view reads `POST /api/fast-access/v1/read` through the shared API client. It requests active runs and chats changed in the last 30 minutes, with all visible workspaces or the current workspace as scope. Refresh starts a new snapshot. Load more sends only the cursor and page budgets. Runs appear before chat activity; `+` marks an incomplete run count and the footer distinguishes partial results from a completed snapshot. Chat unread counts are read-only and no mark-read mutation is called.

The API boundary validates envelopes and continuation consistency. An invalid response becomes a recoverable error instead of an empty successful result. Any continuation error hides the old partial snapshot because its authorization may have changed. The query cache key includes caller and request workspace identity. Server state remains in TanStack Query, with no persistence.

## Native mobile exception (superseded by DENE-1879)

Update: DENE-1879 implements the native entry; see [mobile follow-up](../DENE-1879/mobile.md) for verification limits. The following records the original delivery boundary.

`apps/mobile/app/(app)/[workspace]/(tabs)/inbox.tsx` is an independent native inbox. The new cross-workspace activity dialog is not wired there. This child task explicitly restricts writes to server/core/views and necessary Web/Desktop wiring; native parity must be scheduled by the parent. Mobile web uses the same shared dialog. No claim of native feature parity is made.

## Checks

- `pnpm install --frozen-lockfile`: passed.
- `pnpm --filter @multica/core exec vitest run api/fast-access.test.ts`: 2 tests passed (invalid envelope, unread/UTF-8 preview and cursor mapping).
- `pnpm --filter @multica/views exec eslint inbox/components/fast-access-dialog.tsx inbox/components/inbox-page.tsx`: passed.
- Copy scan: no blocks; two warnings concern TypeScript boolean syntax and repeated translation selectors, not visible copy.
- Browser and typecheck results are recorded below when completed; no screenshot or live performance result is implied by source inspection.

Additional checks: core typecheck passed; combined API/query tests passed (3 tests); existing inbox-page regression suite passed (53 tests). Narrow core lint passed. The final component and English/Chinese locale copy scan returned 0 blocks and 0 warnings. Scoped `git diff --check` passed.

`pnpm --filter @multica/views typecheck` completed successfully (exit 0).

## Real browser verification

Ego Lite Default profile, 2026-10-11, actual local `/dev/inbox` route and shared component; no mocked API responses. Initial compilation delay was resolved. Normal login used the development email/code form. Two local workspaces contained 17 active runs and 105 chats, including Chinese/emoji preview and unread messages.

- Screenshots at actual `innerWidth` 375 / 390 / 768 / 1280, height 900, page scrollY=0; all four PNGs reopened and visually inspected. Dialog and document have no horizontal overflow. The 1280 screenshot was recaptured after the entrance transition settled.
- Mobile entry: Inbox → all activity tab → Activity. Existing overlay history/safe-area component is reused. Actual 375px Close click succeeded; after transition the dialog was absent. Mobile Close/Refresh/Load more controls measured 44px high.
- The first snapshot showed 17 active runs, workspace names/source titles and a Load more control. Browser continuation success and refresh success were **not** obtained: a concurrent local task replaced shared localhost cookies in the Default browser profile. The API log confirmed the other fixture identity; the request still sent `X-Workspace-Slug: dev`. It returned 404 even though this fixture and workspace membership remained intact. The UI displayed its recoverable error and hid stale results. No authentication code, permission records or API responses were changed to work around this.
- Before/after: 62 chat read records, 0 session read timestamps later than fixture creation; after the checks, 0 per-user read records later than fixture creation. The fixture's unread state was not advanced by compact reading.
- Backend pagination and the core query's cursor-only continuation have separate passing tests. Do not equate those with successful browser Load more/Refresh interactions.
- No separate Electron/native app run, production benchmark or actual phone-device session was performed.

Artifacts: `.scratch/anti-slop-frontend/DENE-1870/evidence/activity-{375,390,768,1280}.png`, `viewport-checks.json`, `unread-before.txt`, `unread-after.txt`. The disposable guarded SQL fixture is `.scratch/anti-slop-frontend/DENE-1870/seed-ui.sql`. Its first transaction was rolled back by the required-runtime constraint; the corrected fixture transaction succeeded. An inline-image HTML report is delivered separately from a system temporary directory.

The inline-image HTML report itself was opened in the same owned Task Space at 375 and 1280 CSS pixels with network disabled: all four inline PNGs loaded, all five details sections opened/closed, no horizontal overflow. Both report screenshots were reopened. Owned Task Space 25 was then closed successfully; the managed web/API services were left for the parent to stop.
