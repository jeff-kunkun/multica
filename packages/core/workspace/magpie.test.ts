// @vitest-environment node
import { describe, expect, it } from "vitest";
import { isWorkspaceMagpieEnabled } from "./magpie";

describe("isWorkspaceMagpieEnabled", () => {
  it("is on only for an explicit true", () => {
    expect(isWorkspaceMagpieEnabled({ magpie: { enabled: true } })).toBe(true);
  });

  it("is off for anything else", () => {
    for (const settings of [
      undefined,
      null,
      "magpie",
      {},
      { magpie: true },
      { magpie: null },
      { magpie: {} },
      { magpie: { enabled: "true" } },
      { magpie: { enabled: false } },
    ]) {
      expect(isWorkspaceMagpieEnabled(settings)).toBe(false);
    }
  });
});
