// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import type { AgentRuntime, PlanLimitsSnapshot } from "@multica/core/types";
import enRuntimes from "../../locales/en/runtimes.json";
import {
  displayPlanLimits,
  PlanLimitsCell,
  planLimitWindowShortLabel,
} from "./plan-limits";

const NOW = Date.UTC(2026, 7, 21, 12);

const SNAPSHOT: PlanLimitsSnapshot = {
  provider: "codex",
  status: "available",
  observed_at: NOW / 1000,
  windows: [
    {
      name: "primary",
      used_percent: 42,
      window_minutes: 300,
      resets_at: NOW / 1000 + 60,
    },
  ],
};

describe("displayPlanLimits", () => {
  it("drops a percentage after its provider reset boundary", () => {
    expect(displayPlanLimits(SNAPSHOT, NOW)).not.toBeNull();
    expect(displayPlanLimits(SNAPSHOT, NOW + 61_000)).toBeNull();
  });

  it("expires reset-less exhausted observations after one day", () => {
    const exhausted: PlanLimitsSnapshot = {
      provider: "claude",
      status: "exhausted",
      observed_at: NOW / 1000,
    };
    expect(displayPlanLimits(exhausted, NOW)).not.toBeNull();
    expect(displayPlanLimits(exhausted, NOW + 24 * 60 * 60 * 1000 + 1)).toBeNull();
  });

  it("keeps a window-less 429 snapshot available until it expires", () => {
    const exhausted: PlanLimitsSnapshot = {
      provider: "grok",
      status: "exhausted",
      observed_at: NOW / 1000,
    };
    const display = displayPlanLimits(exhausted, NOW);
    expect(display).not.toBeNull();
    expect(display?.windows).toEqual([]);
  });

  it("treats a missing snapshot as unavailable", () => {
    expect(displayPlanLimits(undefined, NOW)).toBeNull();
  });

  it("expires stale percentages even when the provider reset is later", () => {
    const weekly: PlanLimitsSnapshot = {
      ...SNAPSHOT,
      windows: [{
        name: "secondary",
        used_percent: 18,
        window_minutes: 10_080,
        resets_at: NOW / 1000 + 7 * 24 * 60 * 60,
      }],
    };
    expect(displayPlanLimits(weekly, NOW + 24 * 60 * 60 * 1000 + 1)).toBeNull();
  });

  it("uses provider window durations for compact labels", () => {
    expect(planLimitWindowShortLabel(SNAPSHOT.windows![0]!)).toBe("5h");
  });

  it("labels Gemini model buckets and Grok credits by window name", () => {
    expect(planLimitWindowShortLabel({ name: "gemini_pro", used_percent: 12 })).toBe("Pro");
    expect(planLimitWindowShortLabel({ name: "gemini_flash", used_percent: 40 })).toBe("Flash");
    expect(planLimitWindowShortLabel({ name: "gemini_flash_lite", used_percent: 5 })).toBe("Lite");
    expect(planLimitWindowShortLabel({ name: "credits", used_percent: 18 })).toBe("Credits");
  });

  it("labels coding-plan windows as 5h/7d and balances as currency", () => {
    expect(planLimitWindowShortLabel({ name: "five_hour", used_percent: 12 })).toBe("5h");
    expect(planLimitWindowShortLabel({ name: "seven_day", used_percent: 40 })).toBe("7d");
    expect(planLimitWindowShortLabel({ name: "balance_cny", remaining: 110 })).toBe("¥");
    expect(planLimitWindowShortLabel({ name: "balance_usd", remaining: 12.5 })).toBe("$");
  });
});

describe("PlanLimitsCell", () => {
  it("renders the current Codex window percentage", () => {
    const runtime = {
      plan_limits: SNAPSHOT,
    } as AgentRuntime;

    render(
      <I18nProvider locale="en" resources={{ en: { runtimes: enRuntimes } }}>
        <PlanLimitsCell runtime={runtime} now={NOW} />
      </I18nProvider>,
    );

    expect(screen.getByText("5h 42%")).toBeInTheDocument();
  });

  it("renders a DeepSeek remaining balance", () => {
    const runtime = {
      plan_limits: {
        provider: "dsh",
        status: "available",
        observed_at: NOW / 1000,
        windows: [{ name: "balance_cny", remaining: 110 }],
      },
    } as AgentRuntime;

    render(
      <I18nProvider locale="en" resources={{ en: { runtimes: enRuntimes } }}>
        <PlanLimitsCell runtime={runtime} now={NOW} />
      </I18nProvider>,
    );

    expect(screen.getByText("¥110")).toBeInTheDocument();
  });
});
