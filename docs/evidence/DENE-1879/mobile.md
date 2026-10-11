# Native activity entry

The native inbox has a suitable entry (`apps/mobile/app/(app)/[workspace]/(tabs)/inbox.tsx`); the former scope-only exception is superseded. Its Activity action opens the workspace `activity` formSheet registered with existing `SHEET_OPTIONS`. The body uses mobile primitives, safe bottom inset, scrollable content, Close/back with an inbox fallback, and 44px minimum actions. English and Simplified Chinese reuse the shared snapshot-view copy, adding a permission-denied explanation.

The mobile-owned Query options call the mobile API wrapper, forwarding AbortSignal through `fetchValidatedWith`. The exported core FastAccessSchema is reused without importing web hooks/stores. Identity includes account, request workspace, scope and observation window. Paging sends only cursor and budgets; refresh starts a new 30-minute window. Server authorization stays authoritative. Errors hide previous pages, 403 explains the unavailable scope, and no read-state mutation is used. No server, heartbeat, offline or credential code changed.

## Checks and limits

- Deterministic dependency install passed.
- Mobile typecheck and lint passed.
- Mobile test: 39 files / 254 tests passed; ios-run shell assertions passed.
- Copy scan: 0 blocks, 0 warnings after replacing a boolean syntax false positive.
- Source transaction baseline: entry/route absent; modified: entry/route present; rollback copy: absent again, original hashes restored. Patch reconstructed modified source. These checks are source evidence, not native UI execution.
- The assigned seat has no `computer-use` tools. Per jev-ego desktop-cua routing, native simulator/device and Electron GUI execution are unavailable; no alternative GUI driver or browser-rendered RN substitute was used. Native themes, scrolling, sheet gestures and deep-link return remain unverified on-device.
- This is the timestamp snapshot phase, not production cross-workspace performance acceptance. The event-journal sibling was notified to update the shared schema/mobile consumer when changing mode.

Unverified native flow: open Inbox → Activity, switch scope, Load more, Refresh, Close in both themes. Expected: safe-area scrolling, no clipping, return to Inbox and unchanged unread badges. If different, capture screen, app build, selected scope and failing action. No human review or production claim is implied.

## Durable protocol follow-up (DENE-1872)

The later durable implementation supersedes the 30-minute request above: a fresh read omits `since` and rebuilds current state; the timestamp in the query key is only a refresh identity. Queries refresh every 30 seconds/on focus and clear prior pages on failure, while retaining cancellation forwarding. The shared schema accepts `durable_delta`. Mobile typecheck/lint, 40 files / 255 tests, and ios-run assertions passed. This is query-layer evidence; the on-device visual limitation above remains. See [journal evidence](../DENE-1872/backend.md).
