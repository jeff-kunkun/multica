// @vitest-environment node

import { describe, expect, it } from "vitest";
import { ISSUE_DRAFT_POLICIES, isIssueDraftPolicyKey } from "./policy";

describe("isIssueDraftPolicyKey", () => {
  it("accepts exactly the policies this client can switch to", () => {
    for (const key of ISSUE_DRAFT_POLICIES) {
      expect(isIssueDraftPolicyKey(key)).toBe(true);
    }
  });

  it("carries every policy the server registers", () => {
    // The whitelist and the server registry are two lists that have to agree:
    // a key the server accepts but this list omits is a policy the menu never
    // offers, and a key this list has and the server does not is a switch that
    // comes back 400. Pinned by name so a fourth entry is a deliberate edit.
    expect(ISSUE_DRAFT_POLICIES).toEqual(["question", "conversation", "frontend"]);
    // `frontend` is spelled out because it is the one whose name is guessable
    // in several ways ("grill", "front-end", "frontend-look") and only the
    // exact key is accepted.
    expect(isIssueDraftPolicyKey("frontend")).toBe(true);
    expect(isIssueDraftPolicyKey("front-end")).toBe(false);
    expect(isIssueDraftPolicyKey("grill")).toBe(false);
  });

  it("refuses anything else, including the empty key an older backend reports", () => {
    expect(isIssueDraftPolicyKey("")).toBe(false);
    expect(isIssueDraftPolicyKey("interrogation")).toBe(false);
    expect(isIssueDraftPolicyKey("QUESTION")).toBe(false);
  });
});
