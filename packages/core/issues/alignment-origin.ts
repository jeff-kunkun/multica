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
 * That is exactly true for the group's ROOT. A sub-issue carries its own node
 * id instead — see `issueAlignmentDraftId`, which is what a surface rendering
 * an arbitrary member of a group should call.
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

/**
 * The alignment CONVERSATION an issue belongs to, for any member of a group
 * (DENE-415).
 *
 * `issueAlignmentOrigin` answers "what is stamped on this issue" — which is the
 * conversation only for the group's root. A sub-issue is stamped with its own
 * node id: a UUIDv5 derived from the session and the node's key, deliberately
 * so that each node owns at most one issue. Opening the draft route with it
 * would land on a conversation that does not exist.
 *
 * So a sub-issue resolves through its PARENT, which is the group's root and
 * whose stamp is the session id. That one hop is why the design kept the root's
 * origin equal to the conversation rather than moving every node onto a
 * separate origin sequence (docs/design/issue-draft-group-finalize.md §3.2).
 *
 * A node whose parent is gone — detached to the top level, or re-parented under
 * an unrelated issue — falls back to its own derived stamp, which names a draft
 * route that cannot open. The caller must treat that as "no alignment to return
 * to" and let the missing draft surface, not assume the id is a conversation.
 */
export function issueAlignmentDraftId(
  issue: Pick<Issue, "origin_type" | "origin_id" | "parent_issue_id"> | null | undefined,
  parent: Pick<Issue, "origin_type" | "origin_id"> | null | undefined,
): string | null {
  if (!issue) return null;
  // A sub-issue carries its own node id; the conversation is only on its
  // parent. Returning nothing while that parent is still loading is the
  // deliberate half of the rule: the sub-issue's own id is a route that cannot
  // open, and showing it for a frame is worse than showing the entry a beat
  // late.
  if (issue.parent_issue_id) return issueAlignmentOrigin(parent);
  return issueAlignmentOrigin(issue);
}
