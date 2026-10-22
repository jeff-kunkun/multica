import { ApiError, errorCode } from "../api/client";

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
