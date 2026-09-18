// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  ISSUE_DRAFT_CAPABILITIES,
  encodeIssueDraftCapabilities,
  isIssueDraftCapabilityKey,
  issueDraftEnabledCapabilities,
} from "./capabilities";

describe("isIssueDraftCapabilityKey", () => {
  it("accepts exactly the capabilities this client offers", () => {
    for (const key of ISSUE_DRAFT_CAPABILITIES) {
      expect(isIssueDraftCapabilityKey(key)).toBe(true);
    }
  });

  it("carries the keys the server registers as user-selectable", () => {
    // The picker and the server registry are two lists that have to agree: a
    // key the server accepts but this list omits is a capability the picker
    // never offers, and a key this list has and the server does not is a create
    // that comes back 400. Pinned by name so a change is a deliberate edit.
    expect(ISSUE_DRAFT_CAPABILITIES).toEqual([
      "wayfinder",
      "grill",
      "grill-frontend-look",
    ]);
  });

  it("does not offer `grilling`, which `grill` pulls in on its own", () => {
    // The server registers it and resolves it as a requirement of `grill`.
    // Offering it here would be a second way to ask for the same thing, and a
    // box that cannot be turned off on its own is a lie about what the picker
    // controls.
    expect(isIssueDraftCapabilityKey("grilling")).toBe(false);
  });

  it("refuses anything else, including the empty key an older backend reports", () => {
    expect(isIssueDraftCapabilityKey("")).toBe(false);
    expect(isIssueDraftCapabilityKey("grill-frontend-looks")).toBe(false);
    expect(isIssueDraftCapabilityKey("Wayfinder")).toBe(false);
  });
});

describe("issueDraftEnabledCapabilities", () => {
  it("reads the boxes off the set the draft is actually running", () => {
    expect(
      issueDraftEnabledCapabilities(["grill", "wayfinder"]),
    ).toEqual(["wayfinder", "grill"]);
  });

  it("drops a recorded key this client cannot render", () => {
    // The recorded list is the server's and is authoritative — it may name a
    // capability a later build retired. It is intersected rather than echoed,
    // because a key with no box must not silently become an unticked box the
    // user appears to have turned off.
    expect(
      issueDraftEnabledCapabilities(["wayfinder", "retired-method"]),
    ).toEqual(["wayfinder"]);
  });

  it("treats an empty record as nothing ticked", () => {
    expect(issueDraftEnabledCapabilities([])).toEqual([]);
  });
});

describe("encodeIssueDraftCapabilities", () => {
  it("sends the selection in this client's render order", () => {
    expect(
      encodeIssueDraftCapabilities(["grill-frontend-look", "wayfinder"]),
    ).toEqual(["wayfinder", "grill-frontend-look"]);
  });

  it("sends an empty array for an emptied picker rather than omitting the field", () => {
    // The wire distinguishes them: omitted means the server's built-in default
    // set, and an empty array means none. An emptied picker just asked for
    // none, so sending nothing would give it the opposite of what it asked for.
    expect(encodeIssueDraftCapabilities([])).toEqual([]);
  });
});
