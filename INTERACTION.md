# Interaction graph

> status: active

## Flow: workspace-activity

initial: inbox

The native inbox opens `activity` as the existing formSheet. Web/Desktop open the shared inbox dialog. Both read the same server snapshot; opening, paging and refreshing do not mark chats read. Screen references are the implemented routes below; this change reuses existing surfaces without a separate prototype.

- Native: `apps/mobile/app/(app)/[workspace]/activity.tsx`
- Web/Desktop: `packages/views/inbox/components/fast-access-dialog.tsx`
- Server results: `POST /api/fast-access/v1/read`; `complete` decides whether continuation exists. Permission/reset errors hide previous pages.

### States

| id | rest | available | phase | screen |
|---|---|---|---|---|
| inbox | yes | OPEN | — | — |
| loading | no | CLOSE, CHANGE_SCOPE | — | — |
| partial | yes | MORE, REFRESH, CLOSE, CHANGE_SCOPE | — | — |
| complete | yes | REFRESH, CLOSE, CHANGE_SCOPE | — | — |
| failed | yes | REFRESH, CLOSE, CHANGE_SCOPE | — | — |

### Transitions

| from | event | guard | to |
|---|---|---|---|
| inbox | OPEN | — | loading |
| loading | SUCCEEDED | complete=false | partial |
| loading | SUCCEEDED | complete=true | complete |
| loading | FAILED | API error or invalid response | failed |
| loading | CLOSE | — | inbox |
| loading | CHANGE_SCOPE | — | loading |
| partial | MORE | next_cursor present | loading |
| partial | REFRESH | — | loading |
| partial | CLOSE | — | inbox |
| partial | CHANGE_SCOPE | — | loading |
| complete | REFRESH | — | loading |
| complete | CLOSE | — | inbox |
| complete | CHANGE_SCOPE | — | loading |
| failed | REFRESH | — | loading |
| failed | CLOSE | — | inbox |
| failed | CHANGE_SCOPE | — | loading |

### Walks

| name | path | expect |
|---|---|---|
| happy | inbox -OPEN-> loading -SUCCEEDED[complete=false]-> partial -MORE-> loading -SUCCEEDED[complete=true]-> complete -REFRESH-> loading -SUCCEEDED[complete=false]-> partial -CLOSE-> inbox | Complete batch, fresh snapshot, unchanged unread state |
| recover | inbox -OPEN-> loading -FAILED-> failed -REFRESH-> loading -SUCCEEDED[complete=true]-> complete -CLOSE-> inbox | Old results hidden on failure; refresh recovers |
