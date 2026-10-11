import { describe, expect, it } from "vitest";
import { BotWakeDeliveryStatsSchema } from "./schemas";

describe("wake delivery statistics compatibility", () => {
  it("preserves saved rules and validates a real backlog count", () => {
    expect(BotWakeDeliveryStatsSchema.parse({ hourly_limit: 1, limited_count: 2 }))
      .toEqual({ hourly_limit: 1, limited_count: 2 });
  });
  it.each([undefined, null, "2", -1, 1.5, Number.POSITIVE_INFINITY])(
    "defaults a missing or malformed count (%s) without dropping rules",
    (limited_count) => {
      expect(BotWakeDeliveryStatsSchema.parse({ hourly_limit: 10, limited_count }))
        .toEqual({ hourly_limit: 10, limited_count: 0 });
    },
  );
});
