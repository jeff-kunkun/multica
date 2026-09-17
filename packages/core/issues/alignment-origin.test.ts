// @vitest-environment node
import { describe, expect, it } from "vitest";
import { issueAlignmentDraftId, issueAlignmentOrigin } from "./alignment-origin";

/**
 * The issue → alignment link is one decision, and it is made here so the entry
 * cannot appear for an issue that merely happens to carry half of it.
 */
describe("issueAlignmentOrigin", () => {
  it("returns the conversation id an alignment-created issue points at", () => {
    expect(
      issueAlignmentOrigin({ origin_type: "issue_draft", origin_id: "sess-42" }),
    ).toBe("sess-42");
  });

  it("refuses every other origin type", () => {
    // autopilot and quick_create write the same column pair; their origin_id is
    // a run or a task id, and navigating to it as a draft would be a 404.
    for (const origin_type of ["autopilot", "quick_create", "lark_chat"]) {
      expect(issueAlignmentOrigin({ origin_type, origin_id: "sess-42" })).toBeNull();
    }
  });

  it("refuses half a pair and an absent issue", () => {
    expect(issueAlignmentOrigin({ origin_type: "issue_draft" })).toBeNull();
    expect(issueAlignmentOrigin({ origin_id: "sess-42" })).toBeNull();
    expect(issueAlignmentOrigin(null)).toBeNull();
    expect(issueAlignmentOrigin(undefined)).toBeNull();
  });

  it("refuses an empty or blank id rather than navigating to its parent route", () => {
    expect(issueAlignmentOrigin({ origin_type: "issue_draft", origin_id: "" })).toBeNull();
    expect(issueAlignmentOrigin({ origin_type: "issue_draft", origin_id: "  " })).toBeNull();
  });
});

describe("issueAlignmentDraftId", () => {
  const root = { origin_type: "issue_draft", origin_id: "sess-42" } as const;

  it("reads the conversation off a root issue's own stamp", () => {
    expect(issueAlignmentDraftId({ ...root, parent_issue_id: null }, null)).toBe(
      "sess-42",
    );
  });

  it("resolves a sub-issue through its parent, never through its own stamp", () => {
    // A sub-issue is stamped with its own node id — a UUIDv5 the server derives
    // from the session and the node's key — so its own stamp names a draft
    // route that cannot open. The conversation only lives on the group's root,
    // which is its parent. (DENE-415)
    expect(
      issueAlignmentDraftId(
        {
          origin_type: "issue_draft",
          origin_id: "3d56681b-224b-5d51-abc1-cc1f5016091e",
          parent_issue_id: "issue-root",
        },
        root,
      ),
    ).toBe("sess-42");
  });

  it("offers nothing for a sub-issue whose parent is not on this alignment", () => {
    expect(
      issueAlignmentDraftId(
        {
          origin_type: "issue_draft",
          origin_id: "3d56681b-224b-5d51-abc1-cc1f5016091e",
          parent_issue_id: "issue-root",
        },
        { origin_type: "issue_draft" },
      ),
    ).toBeNull();
    expect(
      issueAlignmentDraftId(
        {
          origin_type: "issue_draft",
          origin_id: "3d56681b-224b-5d51-abc1-cc1f5016091e",
          parent_issue_id: "issue-root",
        },
        undefined,
      ),
    ).toBeNull();
  });

  it("offers nothing for an issue that did not come from an alignment", () => {
    expect(
      issueAlignmentDraftId(
        { origin_type: "autopilot", origin_id: "run-7", parent_issue_id: null },
        null,
      ),
    ).toBeNull();
  });
});
