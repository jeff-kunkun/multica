// @vitest-environment node

import { describe, expect, it } from "vitest";
import { ISSUE_DRAFT_POLICIES, isIssueDraftPolicyKey } from "./policy";

describe("isIssueDraftPolicyKey", () => {
  it("accepts exactly the policies this client can switch to", () => {
    for (const key of ISSUE_DRAFT_POLICIES) {
      expect(isIssueDraftPolicyKey(key)).toBe(true);
    }
  });

  it("refuses anything else, including the empty key an older backend reports", () => {
    expect(isIssueDraftPolicyKey("")).toBe(false);
    expect(isIssueDraftPolicyKey("interrogation")).toBe(false);
    expect(isIssueDraftPolicyKey("QUESTION")).toBe(false);
  });
});
