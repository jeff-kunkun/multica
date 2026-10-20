import { ApiError, errorCode } from "../api/client";

/**
 * How a new project's directory is shared, before anyone overrides it.
 *
 * A directory this flow creates and `git init`s is one repository, so each
 * task gets its own worktree. A container of repositories — or a folder that
 * was deliberately left without git — is shared: tasks run in the folder
 * itself, side by side. `in_place` is never inferred; someone has to pick it.
 */
export function inferNewProjectExecutionMode(input: {
  gitInit: boolean;
  container: boolean;
}): "worktree" | "shared" {
  if (input.container || !input.gitInit) return "shared";
  return "worktree";
}

/** A directory name taken from a project title: one path segment, nothing else. */
export function sanitizeProjectDirectoryName(name: string): string {
  const cleaned = name
    .trim()
    .replace(/[\\/:\0]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  if (cleaned === "." || cleaned === "..") return "";
  return cleaned.slice(0, 80);
}

/**
 * Whether a failed project create should delete the directory it just created.
 *
 * A 4xx, and a create the server explicitly rolled back, left no project, so
 * the directory would be an orphan. A failure after the project committed
 * (`committed`) must keep the directory: deleting it would leave a project
 * with nowhere to work. A network error is the same — the commit may have
 * landed and the response been lost.
 */
export function provisionedDirectoryShouldBeRemoved(err: unknown): boolean {
  const code = errorCode(err);
  if (code === "committed") return false;
  if (code === "rolled_back") return true;
  return err instanceof ApiError && err.status >= 400 && err.status < 500;
}
