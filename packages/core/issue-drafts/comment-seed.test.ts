// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { SourceContextPreview } from "../types";
import { issueDraftCommentSeed } from "./comment-seed";

/**
 * The request an alignment started from a comment opens on.
 *
 * It is pure and lives here rather than in the dialog because it is the only
 * thing standing between "start aligning from this comment" and a conversation
 * that opens empty: what the carrier reads first is this string, and a seed that
 * quotes the wrong row (a reply instead of the anchor, a deleted comment, a
 * citation with nothing under it) is worse than no seed at all.
 */

function preview(overrides: Partial<SourceContextPreview> = {}): SourceContextPreview {
  return {
    capture_token: "token",
    limits: { comment_count: 0, text_bytes: 0, attachment_count: 0, attachment_bytes: 0 },
    anchor_comment_id: "c1",
    source_issue: {
      id: "i1",
      identifier: "MUL-9",
      number: 9,
      title: "Dark mode",
      description: null,
      created_at: "",
      updated_at: "",
      revision: 1,
      attachments: [],
    },
    comment_thread: [
      {
        id: "c1",
        parent_id: null,
        type: "comment",
        content: "the toggle flickers",
        author: { type: "member", id: "u1", name: "kk" },
        created_at: "",
        updated_at: "",
        revision: 1,
        attachments: [],
      },
      {
        id: "c2",
        parent_id: "c1",
        type: "comment",
        content: "only on Safari",
        author: { type: "member", id: "u2", name: "sam" },
        created_at: "",
        updated_at: "",
        revision: 1,
        attachments: [],
      },
    ],
    ...overrides,
  };
}

describe("issueDraftCommentSeed", () => {
  it("quotes the anchor comment under the issue it came from", () => {
    expect(issueDraftCommentSeed(preview())).toBe("MUL-9\n\n> the toggle flickers");
  });

  it("quotes every line of a multi-line comment as one block", () => {
    const seeded = issueDraftCommentSeed(
      preview({
        comment_thread: [
          {
            id: "c1",
            parent_id: null,
            type: "comment",
            content: "first line\n\nsecond line",
            author: { type: "member", id: "u1", name: "kk" },
            created_at: "",
            updated_at: "",
            revision: 1,
            attachments: [],
          },
        ],
      }),
    );
    expect(seeded).toBe("MUL-9\n\n> first line\n>\n> second line");
  });

  it("seeds nothing without a preview", () => {
    // Not loaded yet, failed to load, or a backend that predates the endpoint:
    // the panel opens empty rather than on a citation with nothing under it.
    expect(issueDraftCommentSeed(null)).toBeNull();
    expect(issueDraftCommentSeed(undefined)).toBeNull();
  });

  it("seeds nothing when the anchor is not in the thread", () => {
    expect(issueDraftCommentSeed(preview({ anchor_comment_id: "gone" }))).toBeNull();
  });

  it("seeds nothing from a deleted anchor", () => {
    // A comment deleted while it had replies is kept as an empty row so its
    // replies keep their parent — there is nothing to quote from one.
    const deleted = preview();
    expect(
      issueDraftCommentSeed({
        ...deleted,
        comment_thread: [{ ...deleted.comment_thread[0]!, deleted: true, content: "" }],
      }),
    ).toBeNull();
  });
});
