# Snapshot activity browser verification

2026-10-11, Ego Lite Default profile, one owned Task Space (27), real local Next.js page and Go API at task commit baseline `1caafac227`. No mocked responses, credential code edits, alternate identities or new browser profiles. Native changes are documented in [mobile.md](mobile.md).

## Successful path

At 375px: Inbox → All activity → Activity → scroll → Load more. Initial response 200 delivered 17 active runs + 55 chats (`complete=false`, `active_runs_complete=true`); continuation response 200 delivered 50 chats (`complete=true`, `active_runs_complete=true`) with the same `as_of`. The browser request sent only cursor and budgets for continuation. All 122 composite `(workspace_id,kind,id)` identities exactly matched the disposable database, no omissions or duplicates; summed unread count was 105.

Refresh buttons succeeded at 1280px and 375px: 200, fresh `as_of`, a new initial request without a cursor. Close clicks removed the dialog at both sizes; 375px returned to the inbox. Six observed fast-access responses were 200.

375 / 390 / 768 / 1280 × 900 screenshots were reopened and inspected. Document `scrollY=0`, no document/dialog horizontal overflow, dialog top/bottom 16/884, and Close/Refresh/Load more/select were all 44px high. Long Chinese/emoji previews wrapped inside the scrolling dialog. Resizing across the desktop layout boundary remounts the inbox and closes the dialog; it was reopened at 1280 for that viewport's check. This is not evidence of preserving modal state across breakpoints.

Before opening Activity and after closing it: 105 chats/messages, 53 per-user read rows, and both maximum read timestamps remained `2026-10-11T17:32:24.351Z`. The regular inbox/chat shell had previously backfilled 53 read rows at the original timestamp; opening and paging Activity did not change them. Per-item responses retained unread=1 for every chat.

## Environment and limitations

The worktree's managed environment `dene_1879_8deeb1ab85f0-302` owns database/ports 18382 and 13302. Shared localhost session interference reproduced during setup, before the successful path. A spare loopback IP did not connect on this Mac. The permitted isolation was a dedicated local hostname, `dene1879.localhost`, used by both frontend and backend, in the existing Default profile and the same development account. Only ignored worktree environment URLs/CORS origins changed. No cookies/tokens were copied or exposed.

Cold compilation produced navigation/wait timeouts; the same page was observed until it rendered. A hidden chat-minimize button was intercepted by the mobile tab; no forced click was used. The actual mobile All activity tab was then clicked. An initial viewport collection encountered a null dialog after the desktop remount; the completed per-viewport collection and screenshots record the actual reopened state.

The default `make up --ephemeral` also ran its built-in stale-environment collection before starting this worktree; no manual cleanup of another live environment was invoked. This worktree's services are stopped at handoff.

No native simulator/device or Electron GUI run, production benchmark, two-production-workspace test, journal/durable-mode acceptance, permission-revocation runtime test or native theme verification is claimed.

Artifacts in the task attachment: inline-image HTML; raw screenshot/JSON/source-transaction bundle. Local evidence directory: `.scratch/anti-slop-frontend/DENE-1879/evidence/`. The source transaction's baseline/modified/rollback states concern native entry registration only, not native rendering.

## Knowledge audit

Updated the protocol's native entry description and superseded the earlier scope-only mobile exception. Added the implemented interaction path in root `INTERACTION.md` and its linked flow document, plus an evidence-index pointer to the task attachment. No new domain terms, architecture decisions, credential rules or runtime instructions. Existing runtime-injected `AGENTS.md` trailing whitespace is untouched and excluded from the commit; scoped/staged diff whitespace checks pass.

Report QA: the inline-image HTML was opened offline at 375 and 1280px; all six images loaded, no horizontal overflow. Its details opened by click and closed by Enter. Both report screenshots were reopened. Task Space 27 was closed with `keep: []`.
