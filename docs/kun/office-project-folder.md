# Project folder: one folder per project, Git left to the CLI (DENE-2667 PR 12)

A project names the folder its agents start in, one per machine. The daemon
runs the agent with that folder as its working directory. Whether the folder
is a Git repository is the CLI's to notice: the office binds no repository
and runs no `git init`. A project without a folder runs in the machine's
default working directory (PR 8; set on the Runtimes page, PR 11), or in a
fresh temporary folder when the machine has none.

## How to turn it on

- Desktop: a build with `office_project_folder` in `VITE_MULTICA_FEATURE_FLAGS`.
  Web: `office_project_folder` in `NEXT_PUBLIC_MULTICA_FEATURE_FLAGS`.
- Builds without the flag show the old repository UI unchanged.
- Server and daemon need nothing new: the folder is the existing
  `local_directory` project resource.

## What the flag changes

- Project page: the "Code" repository list becomes "Folder". On the desktop it
  offers Choose folder, or Change folder for this machine's row. Changing
  updates the row in place, so run decisions that name it stay valid, and
  sends `real_path`, `repo_key` and `is_git_repo` explicitly, null where the
  machine could not tell, because the server keeps omitted identity fields
  from the stored row. Folders on other machines can only be removed. The
  web shows the folders and points to the desktop app or the CLI.
- Each row shows the folder's name (its label, else the path's last segment)
  and, under it, the path with the home folder shown as `~` (`/Users/<name>`,
  `/home/<name>`, `/root`, `C:\Users\<name>`; not `/Users/Shared`), so a
  screenshot does not carry the account name. Its Copy button puts the
  absolute path on the clipboard and reads "Copied" for 1.5 s. The issue
  page's shared-folder warning still names the folder only.
- New project dialog and the inline new-project form: folder only. No
  repository tab, no "create local Git" offer, no git-init checkbox. The web
  hides the source pill.
- Shared-folder warning: when two or more dispatched or running tasks are in
  one folder on one machine, the project's folder row and each such issue's
  page list them (agent, then issue link or "Chat"; the issue itself is
  marked "This issue"). Nothing waits: the daemon lets runs share a folder and
  already names the others in each agent's brief (`folderRunRegistry`). The
  warning reads the agent task snapshot and matches by daemon and folder: the
  run's reported `work_dir`, or before that its `local_in_place` decision.
  See `packages/core/projects/folder-runs.ts`.

## CLI

`multica project resource add <project> --type local_directory --local-path
<abs path> --daemon-id <id>` binds a folder; `multica project resource update
… --local-path <new path>` moves it. A moved path that this machine cannot
measure now sends the identity fields as null instead of inheriting the old
folder's.

## Not in this change

- Mobile: no native screen picks a folder (it has no local daemon). Mobile web
  at 390px uses the same views.
- The old repository UI (`ProjectCodeSection`, `AddRepoDialog`, the create
  dialog's repository mode) is still live without the flag, so it stays until
  the flag is on for everyone. The repository-binding APIs and daemon checkout
  stay for old clients and for `multica repo checkout`.
- Removed as unused: the client's worktree-capability probe
  (`runtimeAdvertisesLocalWorktree`, `localWorktreeSupported`, the
  `local_worktree_supported` config field). The server still sends the field.

## Rollback

Remove `office_project_folder` from the build flags, or revert the merge
commit. No migration, no new route, and no stored data depends on it.
