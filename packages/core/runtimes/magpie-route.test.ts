// @vitest-environment node
import { describe, expect, it } from "vitest";
import { readRuntimeMagpieRoute } from "./magpie-route";

describe("readRuntimeMagpieRoute", () => {
  it("reads a routed CLI's group and models in order", () => {
    expect(
      readRuntimeMagpieRoute({
        magpie: {
          group: "office-claude",
          models: [
            { id: "claude/claude-opus-5-5", name: "Claude Opus 5.5" },
            { id: "claude/claude-sonnet-5-5" },
          ],
        },
      }),
    ).toEqual({
      group: "office-claude",
      models: [
        { id: "claude/claude-opus-5-5", name: "Claude Opus 5.5" },
        // No display name reported: the model id stands in.
        { id: "claude/claude-sonnet-5-5", name: "claude-sonnet-5-5" },
      ],
    });
  });

  it("reads an unrouted CLI", () => {
    expect(
      readRuntimeMagpieRoute({ magpie: { group: "", models: [] } }),
    ).toEqual({ group: "", models: [] });
  });

  it("shows nothing for an older daemon or a machine without magpie", () => {
    expect(readRuntimeMagpieRoute(undefined)).toBeNull();
    expect(readRuntimeMagpieRoute({})).toBeNull();
    expect(readRuntimeMagpieRoute({ cli_version: "0.7.0" })).toBeNull();
    expect(readRuntimeMagpieRoute({ magpie: null })).toBeNull();
  });

  it("shows nothing for a malformed route", () => {
    for (const magpie of [
      "office-claude",
      42,
      [],
      { group: "office-claude" },
      { group: "office-claude", models: [] },
      { group: "office-claude", models: "claude/claude-opus-5-5" },
      { group: "office-claude", models: [{ name: "no id" }] },
      { group: "office-claude", models: [{ id: "" }] },
      { group: 1, models: [] },
    ]) {
      expect(readRuntimeMagpieRoute({ magpie })).toBeNull();
    }
  });

  it("ignores fields it does not know", () => {
    expect(
      readRuntimeMagpieRoute({
        magpie: {
          group: "fast",
          routing: "order",
          models: [{ id: "a/one", name: "One", account: "x" }],
        },
      }),
    ).toEqual({ group: "fast", models: [{ id: "a/one", name: "One" }] });
  });
});
