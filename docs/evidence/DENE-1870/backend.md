# Compact read verification

Local PostgreSQL fixture, 2026-10-11. This is not a production measurement.

Reproduce from the repository root:

```sh
bash scripts/test-db.sh --quiet -- bash -c 'cd server && go test ./internal/handler -run "^(TestFastAccess|TestListComments_RootsOnly|TestOverview)" -count=1 -v'
```

The fixture uses two member workspaces and initially 17 active runs, including a chat run, a waiting-local-directory run and a deferred wakeup. It adds inaccessible private sources to check exclusion, and exercises linked-project-only visibility and originator identity separately. It checks unread state stays unchanged, old roots with new replies, UTF-8 truncation, cursor access revocation, request bounds, exhausted quota, and independent autopilot execution records.

Observed on the isolated database: all selected tests passed. Initial complete snapshot: 17 active runs, 2 changes, 10198 UTF-8 JSON bytes. With limit 5 and max_bytes 3000: 5 pages, constant as_of, no duplicates or lost rows; a newly inserted run stayed outside the original batch. The terminal page alone had checkpoint and complete=true. Thirty actual handler/database/serialization reads: p50 152.638334 ms; p95 1.042919 s. The quota was reset between samples to measure reads rather than throttled responses. After adding the indirect private autopilot-source regression, the final selected suite also passed (6.741 s): 10202 bytes, 5 pages, 30 samples p50 22.003917 ms and p95 43.851708 ms. Both measurements are retained because local cache/load conditions differ. Each sample was complete and at most 50000 bytes. These small fixtures do not establish production latency or large-workspace scalability.

`ListRootCommentsSinceForIssue` also ran against the same fixed old-root/new-reply input using pristine, modified and restored SQL: row counts 0 / 1 / 0, all exit 0; restored SHA-256 matched pristine. The unified patch reconstructed the modified SQL byte for byte. Transaction artifacts are attached/referenced in the task evidence.

No full backend suite or production workspace test was run. No schema migration was added. `make sqlc` and task-scoped `git diff --check` passed. Native mobile and browser coverage are documented in [frontend.md](frontend.md).
