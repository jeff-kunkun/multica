// @vitest-environment node
import { describe, expect, it } from "vitest";
import { issueAlignmentOrigin } from "./alignment-origin";

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
