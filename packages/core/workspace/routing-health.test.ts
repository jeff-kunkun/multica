// @vitest-environment node
import { describe, expect, it } from "vitest";

import {
  normalizeRoutingState,
  parseRoutingHealth,
  UNKNOWN_ROUTING_HEALTH,
} from "./routing-health";

describe("parseRoutingHealth", () => {
  it("reads a well-formed report", () => {
    expect(
      parseRoutingHealth({
        state: "ineffective",
        usable: false,
        reason: "the routing model rejected our credentials (401)",
        retry_after_seconds: 240,
        last_success_at: 1_700_000_000,
        last_failure_at: 1_700_000_300,
        model: "gpt-5.6-luna",
        threshold: 0.8,
      }),
    ).toEqual({
      state: "ineffective",
      usable: false,
      reason: "the routing model rejected our credentials (401)",
      retry_after_seconds: 240,
      last_success_at: 1_700_000_000,
      last_failure_at: 1_700_000_300,
      model: "gpt-5.6-luna",
      threshold: 0.8,
    });
  });

  // The malformed-response matrix. Every one of these must degrade to "we do
  // not know", never to a green chip: this endpoint is the only place a broken
  // routing model is visible, so a client that guesses "enabled" from a reply
  // it could not read would hide exactly the failure it exists to show.
  it.each([
    ["null", null],
    ["undefined", undefined],
    ["a string", "enabled"],
    ["an array", [{ state: "enabled" }]],
    ["a missing state", { usable: true }],
    ["a numeric state", { state: 1, usable: true }],
  ])("falls back on %s", (_label, raw) => {
    expect(parseRoutingHealth(raw)).toEqual(UNKNOWN_ROUTING_HEALTH);
  });

  it("does not read a truthy non-boolean as usable", () => {
    // A backend that starts sending "true" as a string must not light the
    // green chip. `=== true`, per the API compatibility rules.
    const health = parseRoutingHealth({ state: "enabled", usable: "true" });
    expect(health.usable).toBe(false);
  });

  it("treats a state this build does not know as off rather than enabled", () => {
    // An installed desktop build against a newer backend. Unknown is not a
    // licence to claim routing is working.
    const health = parseRoutingHealth({ state: "degraded", usable: true });
    expect(health.state).toBe("off");
  });

  it("drops nonsense timestamps instead of rendering them", () => {
    const health = parseRoutingHealth({
      state: "enabled",
      usable: true,
      last_success_at: -5,
      retry_after_seconds: Number.NaN,
    });
    expect(health.last_success_at).toBe(0);
    expect(health.retry_after_seconds).toBe(0);
  });
});

describe("normalizeRoutingState", () => {
  it("passes the four known states through", () => {
    for (const s of ["off", "incomplete", "enabled", "ineffective"] as const) {
      expect(normalizeRoutingState(s)).toBe(s);
    }
  });
});
