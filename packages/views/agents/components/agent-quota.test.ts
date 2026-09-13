// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { DashboardUsageByAgent, PlanLimitsSnapshot } from "@multica/core/types";
import {
  classifyAgentQuota,
  compactRemaining,
  nearestResetAt,
  quotaWindowPercents,
  sumAgentUsage30d,
} from "./agent-quota";

const NOW = Date.UTC(2026, 8, 13, 12);

const CODEX: PlanLimitsSnapshot = {
  provider: "codex",
  status: "available",
  observed_at: NOW / 1000,
  windows: [
    {
      name: "primary",
      used_percent: 25,
      window_minutes: 300,
      resets_at: NOW / 1000 + 2 * 60 * 60,
    },
    {
      name: "secondary",
      used_percent: 3,
      window_minutes: 10_080,
      resets_at: NOW / 1000 + 6 * 24 * 60 * 60,
    },
  ],
};

describe("classifyAgentQuota", () => {
  it("treats Codex rolling windows as subscription quota", () => {
    expect(classifyAgentQuota(CODEX, NOW)).toBe("windows");
  });

  it("keeps a stale subscription snapshot off the metered cost path", () => {
    expect(classifyAgentQuota(CODEX, NOW + 25 * 60 * 60 * 1000)).toBe("windows");
  });

  it("treats a window-less 429 snapshot as exhausted", () => {
    const grok: PlanLimitsSnapshot = {
      provider: "grok",
      status: "exhausted",
      observed_at: NOW / 1000,
    };
    expect(classifyAgentQuota(grok, NOW)).toBe("exhausted");
  });

  it("falls back to metered usage when no snapshot exists", () => {
    expect(classifyAgentQuota(undefined, NOW)).toBe("metered");
    expect(classifyAgentQuota(null, NOW)).toBe("metered");
  });
});

describe("compactRemaining / nearestResetAt", () => {
  it("picks the soonest reset and formats a compact countdown", () => {
    expect(nearestResetAt(CODEX.windows!)).toBe(NOW / 1000 + 2 * 60 * 60);
    expect(compactRemaining(NOW / 1000 + 2 * 60 * 60, NOW)).toBe("2h");
    expect(compactRemaining(NOW / 1000 + 15 * 60, NOW)).toBe("15m");
    expect(compactRemaining(NOW / 1000 + 3 * 24 * 60 * 60, NOW)).toBe("3d");
  });

  it("returns null once the reset has passed", () => {
    expect(compactRemaining(NOW / 1000 - 1, NOW)).toBeNull();
  });
});

describe("quotaWindowPercents", () => {
  it("keeps provider window short labels for the capsule", () => {
    const percents = quotaWindowPercents(CODEX.windows!);
    expect(percents.map((w) => `${w.shortLabel} ${w.used_percent}`)).toEqual([
      "5h 25",
      "7d 3",
    ]);
  });

  it("summarizes Gemini Pro/Flash and Grok credits for the capsule", () => {
    const gemini: PlanLimitsSnapshot = {
      provider: "gemini",
      status: "available",
      observed_at: NOW / 1000,
      windows: [
        { name: "gemini_pro", used_percent: 45 },
        { name: "gemini_flash", used_percent: 10 },
      ],
    };
    expect(
      quotaWindowPercents(gemini.windows!).map((w) => `${w.shortLabel} ${w.used_percent}`),
    ).toEqual(["Pro 45", "Flash 10"]);

    const grok: PlanLimitsSnapshot = {
      provider: "grok",
      status: "available",
      observed_at: NOW / 1000,
      windows: [{ name: "credits", used_percent: 37, window_minutes: 10_080 }],
    };
    expect(quotaWindowPercents(grok.windows!).map((w) => w.shortLabel)).toEqual(["credits"]);
  });
});

describe("sumAgentUsage30d", () => {
  it("folds this agent's token rows and ignores others", () => {
    const rows: DashboardUsageByAgent[] = [
      {
        agent_id: "agent-1",
        provider: "grok",
        model: "grok-4",
        input_tokens: 1_000,
        output_tokens: 500,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        cost_usd_ticks: 2 * 10_000_000_000,
        task_count: 1,
      },
      {
        agent_id: "agent-2",
        provider: "grok",
        model: "grok-4",
        input_tokens: 9_000,
        output_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        cost_usd_ticks: 50 * 10_000_000_000,
        task_count: 1,
      },
    ];
    expect(sumAgentUsage30d(rows, "agent-1")).toEqual({
      tokens: 1_500,
      cost: 2,
    });
  });
});
