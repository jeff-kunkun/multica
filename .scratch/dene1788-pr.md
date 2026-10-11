## What

A new settings page, **外部 Bot**, visible only to workspace owners and admins. An admin adds a Bot (name, type Grok, key lifetime never/1 year) and gets a key with the admin's own power, plus one block of setup text to paste to the Bot. The setup text covers installing the CLI, `multica login --token`, switching workspace, the common commands, the workspace's domains → agents (for `--domain`), and the rule to draft outward actions and ask first.

The list shows name, type, last used, created, expiry and status. A Bot can be stopped (its key gets 401 on the next call), given a new key (the old key dies at once; a stopped Bot comes back) or deleted after it is stopped.

## Three faces

| Face | Delivered |
| --- | --- |
| Server | Bot keys are rows in `personal_access_token` with `kind='bot'`, scoped to a workspace (migration 659). Routes are `GET/POST /api/workspaces/{id}/bots`, `POST .../bots/{botId}/rotate`, `POST .../bots/{botId}/stop` and `DELETE .../bots/{botId}`, gated by owner/admin and `RequireHumanActor`, so an agent task token cannot mint keys. Stop and rotate invalidate the PAT cache so 401 is immediate. The setup text is rendered by the server, so the page and the CLI hand out the same text. Bot keys are kept out of the personal API Token list. |
| Web/Desktop | The `外部 Bot` tab (connections group) only exists for owners/admins. Members don't see the entry, it isn't in settings search, and the list query never runs for them. |
| CLI | `multica bot list / add / key / stop / delete` call the same routes, with `--output json`. |

`multica-platform` skill is not updated: agents are refused by these routes by design, so there is nothing for an agent to call.

## Mobile

- The 390px web page is verified: the add dialog, key dialog (setup text scrolls inside), action menu (44px trigger) and stop confirmation all fit and close.
- `apps/mobile` is not changed. It has no settings screen for API tokens or channels to sit next to, and adding a Bot is a rare admin task done from the web or desktop.

## Known gap

A Bot logged in with a human key can `chat list / history / send` to an existing chat. It cannot open a new chat: `multica chat open` only works inside an agent's chat run. Left as a follow-up.

## Tests

- `go test ./internal/handler -run 'TestBotKeys|TestRenewPAT|TestRequireHumanActor|TestPersonalAccessToken'` against an isolated DB passes. It covers member 403, the key working, stop → 401, rotate (old 401, new 200), 409 on deleting a running Bot, and Bots not appearing in the API Token list.
- `make migration-lint`, `go build ./...` and `go vet` pass.
- `pnpm --filter @multica/core --filter @multica/views typecheck` passes.
- `bots-tab.test.tsx` (3 tests) and locale parity pass.
- `billing-tab.test.tsx` has 3 date tests that fail locally on Node 26. That file is untouched here.
- Real-browser Playwright run at 1280 and 390: add → key → the Bot calls the API (last used shows) → stop → its key gets 401; a member sees no entry. The screenshots are attached to DENE-1788.

Closes DENE-1788

🤖 Generated with [Claude Code](https://claude.com/claude-code)
