# Default working directory (DENE-2667 PR 8 and PR 11)

Each machine can have one default working directory. A run whose project binds
no folder and names no repository starts there instead of a fresh scratch
folder. Its instruction files stay in a separate directory beside it. Squad
leaders, runs with a repository, and runtimes that cannot keep their
instructions outside the work directory ignore it. A missing folder, or one a
run may not use (the home directory, `/`, other protected folders), sends the
run to scratch as before.

## Where to set it

Both surfaces save through `PATCH /api/runtimes/{id}` with `default_work_dir`
(`""` clears it). The server accepts it only from the machine's owner and only
as an absolute path (`invalid_default_work_dir` otherwise). The setting is per
machine, so every runtime on it shares one value.

- CLI: `multica runtime update <runtime-id> --default-dir /Users/me/work`.
  `runtime list --output json` shows `default_work_dir.path` and `exists`.
- Web/Desktop: the runtimes page shows a 「默认工作目录」 field under each of
  your own local machines, in the list and on the machine's page. It refuses a
  relative path before saving and shows what the machine last reported:
  found, not found (runs use scratch), or not checked yet.

Only the owner sees the path; the server sends it to nobody else.

## How to turn the field on

The field appears only in builds with `office_default_dir` in
`VITE_MULTICA_FEATURE_FLAGS` (desktop) or `NEXT_PUBLIC_MULTICA_FEATURE_FLAGS`
(web). The server side and the CLI need no flag. The daemon checks the folder
once a minute and right after the setting changes, so a new path shows its
state within about a minute while the machine is online.

## Rollback

Remove `office_default_dir` from the client build flags to hide the field. A
saved path keeps working; clear it with `--default-dir ""` to stop using it.
