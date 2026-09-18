import type { ChatSession } from "../types";

/**
 * The chat's project set in selection order.
 *
 * `project_ids` is authoritative (DENE-522). A server predating it sends only
 * the singular `project_id`, which names the same single project, so it is
 * normalised into a one-entry set here rather than at every call site — the
 * same rule the server applies when it hydrates a row written before the set
 * existed. Always returns a fresh array, so callers may not use it inside a
 * Zustand selector without a shallow comparison.
 */
export function chatSessionProjectIds(
  session: Pick<ChatSession, "project_id" | "project_ids"> | null | undefined,
): string[] {
  if (!session) return [];
  if (session.project_ids && session.project_ids.length > 0) {
    return [...session.project_ids];
  }
  // An explicit empty `project_ids` means "no project context" and must NOT
  // fall back: the mirrored `project_id` can still be set in a response that
  // crossed a clear, and reviving it would re-attach a project the user just
  // removed.
  if (session.project_ids) return [];
  return session.project_id ? [session.project_id] : [];
}

/**
 * Toggle one project in a set, preserving the order of the projects that stay.
 * A newly checked project is appended, so the first-checked project keeps
 * being the primary one the server mirrors onto `project_id`.
 */
export function toggleProjectId(projectIds: string[], projectId: string): string[] {
  return projectIds.includes(projectId)
    ? projectIds.filter((id) => id !== projectId)
    : [...projectIds, projectId];
}

/**
 * Swap one member of the set for another, in place — what clicking an attached
 * project's chip and picking a different project means. Replacing a project
 * with one already in the set just drops the replaced entry, so the set can
 * never hold a duplicate (the server's unique index would silently collapse
 * it, leaving the UI claiming a position the set does not have).
 */
export function replaceProjectId(
  projectIds: string[],
  fromProjectId: string,
  toProjectId: string,
): string[] {
  if (!projectIds.includes(fromProjectId)) return projectIds;
  if (fromProjectId === toProjectId) return projectIds;
  if (projectIds.includes(toProjectId)) {
    return projectIds.filter((id) => id !== fromProjectId);
  }
  return projectIds.map((id) => (id === fromProjectId ? toProjectId : id));
}

/** Set equality that ignores neither order nor duplicates — the set IS ordered
 *  (position 0 is the primary project), so a reorder is a real change. */
export function sameProjectIds(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((id, i) => id === b[i]);
}
