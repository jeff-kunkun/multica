Closes DENE-998

## What changed

- **Server**: `POST /api/workspaces/{id}/github/app` now also mints a single-use launch token (random, stored only as a sha256 hash, 10-minute TTL, new table `github_app_launch_token`, migration 560). `launch_url` becomes `/api/github/app/launch?token=…`, so the URL no longer carries the signed state. `GET /api/github/app/launch` spends the token atomically, then runs the same `VerifyState` + owner check + `githubapp.Build` as before and returns the auto-submitting manifest form. State and callback are unchanged. Reused, expired or unknown tokens get a readable HTML page (410/410/404) instead of JSON.
- **Desktop**: `launchGitHubAppSetup` (shared in `packages/views`) sees `isDesktopShell()` and hands `launch_url` to `openExternal`, then shows a toast: 已在浏览器中打开 GitHub，完成创建后回到这里刷新. The navigation guard is not touched.
- **Web**: still posts the form in the same tab. I kept it that way because it has no extra hop and no regression risk. The token web mints goes unused and is pruned after a day.
- **CLI**: `multica github-app setup-link` already prints this same `launch_url`. Its help now says the link is single-use and expires after 10 minutes. The multica-platform skill reference and `docs/kun/github-app-onboarding.md` were updated to match.

## Verification

- `scripts/test-db.sh -- go test ./internal/handler -run TestGitHubApp`: guards, callback, and the new `TestGitHubAppLaunchLinkIsSingleUse` all pass. That test covers: first open works, a reused link gets 410, an expired link gets 410, an unknown token gets 404, and a non-owner is refused at begin (403) and at launch (403).
- `go test ./internal/githubapp ./cmd/multica ./internal/service` and `make migration-lint` pass.
- `pnpm --filter @multica/views typecheck` passes. `vitest` passes for github-app-panel (the new web-vs-desktop branch tests), git-connections and locales parity.
- The desktop `navigation-guard.test.ts` suite passes (11 tests) and its rules are unchanged.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
