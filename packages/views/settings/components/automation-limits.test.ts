// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  DEFAULT_AUTOMATION_LIMITS,
  automationLimitsEqual,
  parseAutomationLimits,
  parseBotWakeLimitInput,
  parseLimitInput,
} from "./automation-limits";

describe("parseAutomationLimits", () => {
  it("falls back to the server defaults when nothing was saved", () => {
    expect(parseAutomationLimits(undefined)).toEqual(DEFAULT_AUTOMATION_LIMITS);
    expect(parseAutomationLimits({})).toEqual(DEFAULT_AUTOMATION_LIMITS);
  });

  it("keeps saved values, including zero as unlimited", () => {
    expect(
      parseAutomationLimits({
        agent_chain_budget: 0,
        agent_task_timeout_minutes: 90,
      }),
    ).toEqual({
      agent_chain_budget: 0,
      agent_task_timeout_minutes: 90,
      bot_wake_chain_limit: 4,
    });
  });

  it.each([["12"], [-1], [1.5], [null], [10_000_000], [Number.NaN]])(
    "ignores a malformed value %j",
    (value) => {
      expect(
        parseAutomationLimits({
          agent_chain_budget: value,
          agent_task_timeout_minutes: value,
        }),
      ).toEqual(DEFAULT_AUTOMATION_LIMITS);
    },
  );

  it.each([1, 4, 100])("keeps a saved bot wake limit of %i", (value) => {
    expect(parseAutomationLimits({ bot_wake_chain_limit: value }).bot_wake_chain_limit).toBe(value);
  });

  it.each([0, -1, 101, 1.5, "4", null, Number.NaN])(
    "rejects a bot wake limit of %j instead of enabling unlimited wakes",
    (value) => {
      expect(parseAutomationLimits({ bot_wake_chain_limit: value }).bot_wake_chain_limit).toBe(4);
    },
  );
});

describe("parseLimitInput", () => {
  it("accepts a positive integer", () => {
    expect(parseLimitInput("45", 30)).toBe(45);
  });

  it.each([[""], ["0"], ["-3"], ["abc"]])(
    "never turns %j into unlimited",
    (text) => {
      expect(parseLimitInput(text, 30)).toBe(30);
    },
  );

  it("caps at what the server accepts", () => {
    expect(parseLimitInput("123456789", 30)).toBe(999_999);
  });
});

describe("parseBotWakeLimitInput", () => {
  it.each(["", " ", "0", "-1", "1.5", "4oops", "Infinity"])(
    "keeps the previous count for %j",
    (text) => {
      expect(parseBotWakeLimitInput(text, 4)).toBe(4);
    },
  );

  it("allows the minimum and maximum without an unlimited value", () => {
    expect(parseBotWakeLimitInput("1", 4)).toBe(1);
    expect(parseBotWakeLimitInput("100", 4)).toBe(100);
    expect(parseBotWakeLimitInput("101", 4)).toBe(100);
  });
});

describe("automationLimitsEqual", () => {
  it("saves a change that affects only the bot wake limit", () => {
    expect(automationLimitsEqual(DEFAULT_AUTOMATION_LIMITS, { ...DEFAULT_AUTOMATION_LIMITS })).toBe(true);
    expect(
      automationLimitsEqual(DEFAULT_AUTOMATION_LIMITS, {
        ...DEFAULT_AUTOMATION_LIMITS,
        bot_wake_chain_limit: 5,
      }),
    ).toBe(false);
  });
});
