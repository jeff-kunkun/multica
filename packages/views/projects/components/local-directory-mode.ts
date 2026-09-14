import type { LocalDirectoryExecutionMode } from "@multica/core/types";

/**
 * Why the worktree option may be unavailable.
 *
 * Two reasons, and neither is a guess about the machine. `not_git` the client
 * establishes by itself — the folder either has a repository to branch from or
 * it does not. `server_outdated` is what the SERVER says about itself: whether
 * it understands `execution_mode` at all.
 *
 * Shared mode is not blocked by `not_git` (an umbrella directory of several
 * repos is the reason the mode exists) but shares the server-outdated gate:
 * an older server drops an unknown `execution_mode` and answers 201.
 *
 * `undefined` means available.
 */
export type WorktreeUnavailableReason = "not_git" | "server_outdated";

/**
 * Reads the execution mode off a stored ref. An absent or unrecognised value is
 * reported as in_place, matching the server: the field is optional, and a mode
 * written by a newer client must not render as anything other than the
 * conservative default here.
 */
export function executionModeOf(ref: {
  execution_mode?: string | null;
}): LocalDirectoryExecutionMode {
  switch (ref.execution_mode) {
    case "worktree":
      return "worktree";
    case "shared":
      return "shared";
    case "in_place":
    default:
      return "in_place";
  }
}

/**
 * Which blocker (if any) applies to the worktree option.
 *
 * `isGitRepo === false` is a hard no — the daemon would fail every task on that
 * folder. `undefined` means we could not check (an older desktop build, or an
 * existing row whose path was validated at pick time), and is deliberately
 * permissive: the daemon re-checks authoritatively, so guessing "not a repo"
 * here would block a perfectly valid setup.
 *
 * Daemon capability is deliberately absent. It is the server's question, asked
 * on save; predicting it here is what produced an unfixable blocker for a user
 * already on the newest release (#7113). Deferring to the server does require
 * knowing it will answer, though — `serverValidates` is the server saying so.
 */
export function worktreeUnavailableReason(
  isGitRepo: boolean | undefined,
  serverValidates: boolean,
): WorktreeUnavailableReason | undefined {
  if (isGitRepo === false) return "not_git";
  if (!serverValidates) return "server_outdated";
  return undefined;
}

/** Shared mode is blocked only when the server would silently drop the field. */
export function sharedModeUnavailable(serverValidates: boolean): boolean {
  return !serverValidates;
}

/**
 * Never submit a mode the picker would have blocked. The folder can change
 * after a mode was chosen (pick a git repo, choose worktree, then pick a
 * plain folder), and the stale choice would fail at task time.
 *
 * Shared stays selected on a non-git folder: that is a valid (and typical)
 * setup. Worktree does not.
 */
export function coerceLocalDirectoryMode(
  mode: LocalDirectoryExecutionMode,
  opts: {
    worktreeUnavailable?: WorktreeUnavailableReason;
    sharedUnavailable?: boolean;
  } = {},
): LocalDirectoryExecutionMode {
  switch (mode) {
    case "worktree":
      return opts.worktreeUnavailable ? "in_place" : "worktree";
    case "shared":
      return opts.sharedUnavailable ? "in_place" : "shared";
    case "in_place":
    default:
      return "in_place";
  }
}
