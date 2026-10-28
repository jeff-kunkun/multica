import { describe, expect, it } from "vitest";
import { commentSubIssueTitle } from "./comment-sub-issue-title";

describe("commentSubIssueTitle", () => {
  it("takes the first line of text without markdown", () => {
    expect(commentSubIssueTitle("\n## Add a **CSV** export\nso people can share it")).toBe("Add a CSV export");
    expect(commentSubIssueTitle("- ask [@Kun](mention://member/1) about `limits`")).toBe("ask @Kun about limits");
  });

  it("caps long lines at 80 characters", () => {
    const title = commentSubIssueTitle("拆".repeat(100));
    expect(Array.from(title)).toHaveLength(80);
    expect(title.endsWith("…")).toBe(true);
  });

  it("is empty for a comment with no text", () => {
    expect(commentSubIssueTitle("  \n![](/api/attachments/1)\n")).toBe("");
  });
});
