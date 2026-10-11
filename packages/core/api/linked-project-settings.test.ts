import { describe, expect, it } from "vitest";
import { LinkedProjectSettingsSchema } from "./schemas";

describe("linked project settings response", () => {
  it("preserves inherited and explicit empty selections as distinct states", () => {
    expect(LinkedProjectSettingsSchema.parse({ linked_projects: null, effective_linked_projects: [], inherited: true }).linked_projects).toBeNull();
    expect(LinkedProjectSettingsSchema.parse({ linked_projects: [], effective_linked_projects: [], inherited: false }).linked_projects).toEqual([]);
  });
  it("rejects malformed responses instead of treating them as an empty selection", () => {
    expect(LinkedProjectSettingsSchema.safeParse({}).success).toBe(false);
    expect(LinkedProjectSettingsSchema.safeParse({ linked_projects: [], effective_linked_projects: "hidden", inherited: false }).success).toBe(false);
  });
});
