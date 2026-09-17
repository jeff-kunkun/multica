import type { Issue } from "../types";

/**
 * The requirement-alignment conversation that produced this issue, or null.
 *
 * An issue created by confirming an alignment draft is stamped with
 * `origin_type='issue_draft'` and `origin_id` = the draft's chat_session_id —
 * which is also the alignment conversation's id, and therefore its route. That
 * pair is the ONLY link back: alignment conversations carry a hidden
 * `kind='system'` agent, so no chat list will ever show one.
 *
 * Both halves are required. `origin_type` alone is shared with autopilot runs
 * and quick-create tasks, and `origin_id` alone means nothing without knowing
 * which table it points into. A missing pair is "no known provenance", which is
 * also what a list response looks like — those rows do not select the columns —
 * so a caller must not read a list row's absence as "this issue came from
 * somewhere else".
 *
 * `origin_id` is returned trimmed and non-empty or not at all: it becomes a URL
 * segment, and an empty one would navigate to the draft route's parent.
 */
export function issueAlignmentOrigin(
  issue: Pick<Issue, "origin_type" | "origin_id"> | null | undefined,
): string | null {
  if (!issue) return null;
  if (issue.origin_type !== "issue_draft") return null;
  const id = issue.origin_id?.trim();
  return id ? id : null;
}
