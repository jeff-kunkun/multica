# Interaction graph

> status: active

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
