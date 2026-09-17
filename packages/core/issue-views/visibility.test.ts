// @vitest-environment node
import { describe, expect, it } from "vitest";
import { issueViewSharingChoices, parseIssueViewVisibility } from "./visibility";

describe("parseIssueViewVisibility", () => {
  it("keeps the three known values", () => {
    expect(parseIssueViewVisibility("private")).toBe("private");
    expect(parseIssueViewVisibility("workspace")).toBe("workspace");
    expect(parseIssueViewVisibility("project")).toBe("project");
  });

  it("falls back to private for missing or unknown values", () => {
    expect(parseIssueViewVisibility(undefined)).toBe("private");
    expect(parseIssueViewVisibility(null)).toBe("private");
    expect(parseIssueViewVisibility("public")).toBe("private");
  });
});

describe("issueViewSharingChoices", () => {
  it("offers three choices only for project scope", () => {
    expect(issueViewSharingChoices("project")).toEqual([
      "private",
      "workspace",
      "project",
    ]);
  });

  it("offers two choices for workspace and my scopes", () => {
    expect(issueViewSharingChoices("workspace")).toEqual(["private", "workspace"]);
    expect(issueViewSharingChoices("my")).toEqual(["private", "workspace"]);
  });
});
