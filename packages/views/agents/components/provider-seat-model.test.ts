// @vitest-environment node

// Canonical suite for the seat model string's codec and its display form. Every
// surface that shows a seat's model — both seat-side model pickers — renders
// through this module, so the "no `%2F` on screen" rule is proven here once
// rather than re-run through each DOM mount.

import { describe, expect, it } from "vitest";
import {
  parseProviderSeatModelString,
  providerSeatModelDisplay,
  providerSeatModelString,
  seatModelIsExactly,
} from "./provider-seat-model";

describe("seat model strings", () => {
  const rawId = "deepseek/deepseek-v4.1-flash";
  const presetId = "command-code2";
  const encoded = "command-code2/deepseek%2Fdeepseek-v4.1-flash";

  it("generates, parses and regenerates the same string for a slash-bearing id", () => {
    const generated = providerSeatModelString(presetId, rawId);
    expect(generated).toBe(encoded);

    const parsed = parseProviderSeatModelString(generated);
    expect(parsed).toEqual({ providerId: presetId, modelId: rawId });

    // 回填: writing the parsed pair back out reproduces the same seat string, so
    // what the user selected and what the seat runs are the same thing.
    expect(providerSeatModelString(parsed!.providerId, parsed!.modelId)).toBe(
      generated,
    );
  });

  it("leaves an id without a slash readable, and encodes only what it must", () => {
    expect(providerSeatModelString("command-code2", "claude-sonnet-5")).toBe(
      "command-code2/claude-sonnet-5",
    );
    expect(providerSeatModelString("command-code2", "a b/c")).toBe(
      "command-code2/a%20b%2Fc",
    );
    expect(providerSeatModelString("", rawId)).toBe("");
    expect(providerSeatModelString(presetId, "  ")).toBe("");
  });

  it("refuses a value that cannot be a seat pair", () => {
    for (const bad of ["", "no-slash", "/model", "provider/", "   "]) {
      expect(parseProviderSeatModelString(bad)).toBeNull();
    }
  });

  // The regression this rule exists for: rendering `%2F` invites a hand-edit
  // that drops the prefix. A user searching for the model they picked types
  // `deepseek/deepseek-v4.1-flash` (DENE-684).
  it("shows provider and decoded model id, never the escape", () => {
    const catalogId = "command-code2/deepseek%2Fdeepseek-v4.1-flash";
    expect(providerSeatModelDisplay(catalogId)).toBe(`command-code2 · ${rawId}`);
    expect(providerSeatModelDisplay(catalogId)).not.toContain("%2F");
    // A runtime whose ids carry no provider prefix is untouched by the rule.
    expect(providerSeatModelDisplay("claude-fable-5")).toBe("claude-fable-5");
  });
});

describe("seatModelIsExactly", () => {
  const encoded = "command-code2/deepseek%2Fdeepseek-v4.1-flash";

  it("recognises the decoded model id a user types", () => {
    expect(seatModelIsExactly(encoded, "deepseek/deepseek-v4.1-flash")).toBe(true);
    expect(seatModelIsExactly(encoded, " deepseek/deepseek-v4.1-flash ")).toBe(true);
  });

  it("recognises the stored string and the displayed pair", () => {
    expect(seatModelIsExactly(encoded, encoded)).toBe(true);
    expect(
      seatModelIsExactly(encoded, "command-code2 · deepseek/deepseek-v4.1-flash"),
    ).toBe(true);
  });

  it("does not match a different model or an empty search", () => {
    expect(seatModelIsExactly(encoded, "deepseek/deepseek-v4.1")).toBe(false);
    expect(seatModelIsExactly(encoded, "  ")).toBe(false);
    expect(seatModelIsExactly("claude-opus-5", "claude-opus-5")).toBe(true);
    expect(seatModelIsExactly("claude-opus-5", "claude-opus")).toBe(false);
  });
});
