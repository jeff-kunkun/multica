import type { SourceContextPreview } from "../types";

/**
 * The request an alignment started FROM a comment opens on (DENE-452).
 *
 * "Start aligning from this comment" has to seed the conversation with
 * something, and the something is the comment the person clicked: an alignment
 * that opens empty asks them to retype what is already on screen a few pixels
 * away. The carrier is sent one turn — the same envelope every later turn uses
 * — so this text becomes part of the transcript, and the person can edit it
 * before pressing start.
 *
 * The comment is QUOTED rather than paraphrased. Summarizing a discussion is a
 * judgement the conversation itself is about to make, and a client-side guess
 * at it would be the first thing the carrier reads. The issue's identifier goes
 * in front because a quoted sentence with no provenance is exactly the kind of
 * ambiguity alignment exists to remove.
 *
 * Only the anchor comment is included, not the whole captured thread: an
 * alignment opened from one comment is asking about THAT comment, and the
 * replies under it are what the conversation is about to go and ask for.
 *
 * Null whenever there is nothing safe to seed with — no preview (still loading,
 * failed, or a backend that predates it), a deleted anchor, an anchor id the
 * thread does not contain. The panel then opens empty, which is the behaviour
 * it had before this entry existed, rather than opening on a bare citation.
 */
export function issueDraftCommentSeed(
  preview: SourceContextPreview | null | undefined,
): string | null {
  if (!preview) return null;
  const anchor = preview.comment_thread.find(
    (comment) => comment.id === preview.anchor_comment_id,
  );
  // A comment deleted while it had replies is kept as an empty row so its
  // replies keep their parent (see `SourceContextCommentSnapshot`). There is
  // nothing to quote from one.
  if (!anchor || anchor.deleted === true) return null;
  const quoted = quoteComment(anchor.content);
  if (quoted.length === 0) return null;
  return `${preview.source_issue.identifier.trim()}\n\n${quoted}`;
}

/** One comment as a Markdown quote. An empty body contributes nothing. */
function quoteComment(content: string): string {
  const trimmed = content.trim();
  if (trimmed.length === 0) return "";
  return trimmed
    .split("\n")
    .map((line) => (line.length > 0 ? `> ${line}` : ">"))
    .join("\n");
}
