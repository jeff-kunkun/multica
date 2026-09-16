// @vitest-environment node
import { describe, expect, it } from "vitest";
import { canManageIssueView } from "./queries";

const OWNER = "user-owner";
const OTHER = "user-other";

describe("canManageIssueView", () => {
  it.each(["private", "workspace", "project"] as const)(
    "returns true for the creator at visibility=%s regardless of role",
    (visibility) => {
      expect(
        canManageIssueView({ owner_id: OWNER, visibility }, OWNER, "member"),
      ).toBe(true);
    },
  );

  it.each(["owner", "admin"] as const)(
    "returns true for workspace %s on a workspace-shared view they did not create",
    (role) => {
      expect(
        canManageIssueView({ owner_id: OTHER, visibility: "workspace" }, OWNER, role),
      ).toBe(true);
    },
  );

  it.each(["owner", "admin"] as const)(
    "returns true for workspace %s on a project-shared view they did not create",
    (role) => {
      expect(
        canManageIssueView({ owner_id: OTHER, visibility: "project" }, OWNER, role),
      ).toBe(true);
    },
  );

  it.each(["owner", "admin"] as const)(
    "returns false for workspace %s on a private view they did not create",
    (role) => {
      expect(
        canManageIssueView({ owner_id: OTHER, visibility: "private" }, OWNER, role),
      ).toBe(false);
    },
  );

  it.each(["workspace", "project"] as const)(
    "returns false for a regular member on a %s view they did not create",
    (visibility) => {
      expect(
        canManageIssueView({ owner_id: OTHER, visibility }, OWNER, "member"),
      ).toBe(false);
    },
  );

  it("returns false when userId is empty", () => {
    expect(
      canManageIssueView({ owner_id: OWNER, visibility: "workspace" }, null, "owner"),
    ).toBe(false);
    expect(
      canManageIssueView({ owner_id: OWNER, visibility: "project" }, "", "admin"),
    ).toBe(false);
  });
});
