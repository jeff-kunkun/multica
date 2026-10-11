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

## Flow: mobile-workspace-links

initial: settings

Sources: `apps/mobile/app/(app)/[workspace]/workspace-links/`,
`apps/mobile/app/(app)/[workspace]/chat-references.tsx`, and the existing
`/api/workspace-links` and `/api/chat/sessions/:id` contracts.
The preview uses example data. Native and cross-client verification are recorded
in the delivery evidence, separately from this interaction specification.

### States

| id | rest | available | phase | screen |
|---|---|---|---|---|
| settings | yes | OPEN, NOTICE, CHAT | — | docs/design/prototypes/mobile-workspace-links.html |
| links | yes | EDIT, READ, MANAGE, ACCEPT, REVOKE, BACK | — | docs/design/prototypes/mobile-workspace-links.html |
| projects | yes | SAVE, CANCEL | — | docs/design/prototypes/mobile-workspace-links.html |
| saving | no | — | — | — |
| failed | yes | RETRY, CANCEL | — | docs/design/prototypes/mobile-workspace-links.html |
| reading | yes | FILTER, MORE, BACK | — | docs/design/prototypes/mobile-workspace-links.html |
| chat | yes | REFERENCES | — | docs/design/prototypes/mobile-workspace-links.html |
| references | yes | TOGGLE, CLOSE | — | docs/design/prototypes/mobile-workspace-links.html |

### Transitions

| from | event | guard | to |
|---|---|---|---|
| settings | CHAT | return to chat tab | chat |
| settings | OPEN | current workspace | links |
| settings | NOTICE | workspace_link_* notification | links |
| links | EDIT | server can_update_projects | projects |
| links | READ | active viewer link | reading |
| links | MANAGE | server can_set_managed | saving |
| links | ACCEPT | pending viewer and server can.accept | saving |
| links | REVOKE | server ability and native confirmation | saving |
| links | BACK | — | settings |
| projects | SAVE | nonempty shareable selection and server ability | saving |
| projects | CANCEL | — | links |
| saving | SUCCEEDED | PATCH/POST/DELETE succeeded | links |
| saving | FAILED | server rejection or network error | failed |
| failed | RETRY | same mutation | saving |
| failed | CANCEL | — | links |
| reading | FILTER | shared project | reading |
| reading | MORE | server next_cursor | reading |
| reading | BACK | — | links |
| chat | REFERENCES | — | references |
| references | TOGGLE | draft or chat creator; saved selection awaits PATCH | references |
| references | CLOSE | — | chat |

### Walks

| name | path | expect |
|---|---|---|
| settings-edit | settings -OPEN-> links -EDIT-> projects -SAVE-> saving -SUCCEEDED-> links | Server-saved project selection |
| read | settings -NOTICE-> links -READ-> reading -BACK-> links | Read-only project and issue progress; returns to settings |
| recover | settings -OPEN-> links -EDIT-> projects -SAVE-> saving -FAILED-> failed -RETRY-> saving -SUCCEEDED-> links | Error remains visible until retry succeeds |
| references | settings -CHAT-> chat -REFERENCES-> references -TOGGLE-> references -CLOSE-> chat | Attach/detach references; stale entries remain removable |
