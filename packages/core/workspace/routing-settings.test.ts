// @vitest-environment node
//
// Canonical layer for the routing settings contract. The four-state matrix and
// every malformed-payload case live here; the settings section's own suite
// keeps the happy path and the wiring and does not re-run this matrix through
// a DOM mount.
import { describe, expect, it } from "vitest";
import {
  DEFAULT_CONFIDENCE_THRESHOLD,
  parseRoutingSettings,
  normalizeThreshold,
  routingIsActive,
  routingState,
  withRoutingSettings,
} from "./routing-settings";

describe("parseRoutingSettings", () => {
  it("reads a complete block", () => {
    expect(
      parseRoutingSettings({
        routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.85 },
      }),
    ).toEqual({ enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.85 });
  });

  // Every one of these must read as switched off: a payload the client cannot
  // interpret must never render as enabled.
  it.each([
    ["missing settings", undefined],
    ["null settings", null],
    ["empty settings", {}],
    ["null block", { routing: null }],
    ["block is a string", { routing: "on" }],
    ["block is an array", { routing: [] }],
    ["unrelated settings only", { theme: "dark" }],
  ])("falls back to off for %s", (_label, settings) => {
    const parsed = parseRoutingSettings(
      settings as Record<string, unknown> | null | undefined,
    );
    expect(parsed.enabled).toBe(false);
    expect(routingState(parsed)).toBe("off");
  });

  it("treats a truthy non-boolean enabled as off", () => {
    // Explicit === true, not truthiness: a server field that drifted to a
    // string must not silently switch routing on.
    expect(parseRoutingSettings({ routing: { enabled: "yes" } }).enabled).toBe(false);
    expect(parseRoutingSettings({ routing: { enabled: 1 } }).enabled).toBe(false);
  });

  it("ignores a non-string model", () => {
    expect(parseRoutingSettings({ routing: { enabled: true, model: 42 } }).model).toBe("");
  });
});

describe("normalizeThreshold", () => {
  it.each([0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, "0.8", null, undefined])(
    "falls back to the default for %s",
    (value) => {
      expect(normalizeThreshold(value)).toBe(DEFAULT_CONFIDENCE_THRESHOLD);
    },
  );

  it("keeps a value inside the range", () => {
    expect(normalizeThreshold(0.5)).toBe(0.5);
    expect(normalizeThreshold(1)).toBe(1);
  });
});

describe("routingState", () => {
  it("is off while the switch is off, whatever else is set", () => {
    expect(
      routingState({ enabled: false, model: "m", confidence_threshold: 0.7 }),
    ).toBe("off");
  });

  it("is incomplete when the switch is on with no model", () => {
    // The state that exists because the product must not look enabled when it
    // is doing nothing.
    expect(
      routingState({ enabled: true, model: "", confidence_threshold: 0.7 }),
    ).toBe("incomplete");
    expect(
      routingState({ enabled: true, model: "   ", confidence_threshold: 0.7 }),
    ).toBe("incomplete");
  });

  it("is enabled when the switch is on and a model is chosen", () => {
    expect(
      routingState({ enabled: true, model: "m", confidence_threshold: 0.7 }),
    ).toBe("enabled");
  });

  it("is ineffective only when the server says ineffective in so many words", () => {
    const configured = { enabled: true, model: "m", confidence_threshold: 0.7 };
    expect(routingState(configured, { state: "ineffective" })).toBe("ineffective");
    expect(routingState(configured, { state: "enabled" })).toBe("enabled");
    expect(routingState(configured, null)).toBe("enabled");
  });

  // The cases that used to be misread as a broken model. Each of these is a
  // health report whose `usable` is false for a reason that has nothing to do
  // with the model: two describe settings this client can already see are
  // newer, and one is the fallback for a response it could not read at all.
  it.each([
    ["health still describing the switched-off workspace", "off"],
    ["health still describing the workspace before a model was typed", "incomplete"],
    ["the fallback used when the response cannot be read", "off"],
  ])("does not report a fault for %s", (_label, state) => {
    expect(
      routingState({ enabled: true, model: "m", confidence_threshold: 0.7 }, { state }),
    ).toBe("enabled");
  });

  it("ignores a state name this client does not know", () => {
    // Forward compatibility: a newer server growing a fifth state must not
    // light the red chip on a guess.
    expect(
      routingState(
        { enabled: true, model: "m", confidence_threshold: 0.7 },
        { state: "degraded" },
      ),
    ).toBe("enabled");
  });

  it("treats only enabled as active", () => {
    expect(routingIsActive("enabled")).toBe(true);
    for (const state of ["off", "incomplete", "ineffective"] as const) {
      expect(routingIsActive(state)).toBe(false);
    }
  });
});

describe("withRoutingSettings", () => {
  it("carries the rest of the settings column through untouched", () => {
    expect(
      withRoutingSettings(
        { theme: "dark", other: { a: 1 } },
        { enabled: true, model: " m ", confidence_threshold: 0.9 },
      ),
    ).toEqual({
      theme: "dark",
      other: { a: 1 },
      routing: { enabled: true, model: "m", confidence_threshold: 0.9 },
    });
  });

  it("normalizes an out-of-range threshold before it is stored", () => {
    const out = withRoutingSettings(null, {
      enabled: true,
      model: "m",
      confidence_threshold: 9,
    });
    expect((out.routing as { confidence_threshold: number }).confidence_threshold).toBe(
      DEFAULT_CONFIDENCE_THRESHOLD,
    );
  });

  it("round-trips through parse", () => {
    const next = { enabled: true, model: "m", confidence_threshold: 0.42 };
    expect(parseRoutingSettings(withRoutingSettings({}, next))).toEqual(next);
  });
});
