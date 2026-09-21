// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import type { AgentRuntime } from "@multica/core/types";
import { collectProviderQuotas } from "./provider-status-bar";

const NOW = 1_800_000_000_000;

function runtime(
  provider: string,
  observedAt: number,
  plan_limits: AgentRuntime["plan_limits"],
): AgentRuntime {
  return { id: `${provider}-${observedAt}`, provider, plan_limits } as AgentRuntime;
}

describe("collectProviderQuotas", () => {
  it("keeps only providers with a current real snapshot", () => {
    const result = collectProviderQuotas(
      [
        runtime("grok", NOW / 1000 - 60, {
          provider: "grok",
          status: "available",
          observed_at: NOW / 1000 - 60,
          windows: [{ name: "credits", used_percent: 25 }],
        }),
        runtime("codex", NOW / 1000 - 60, null),
      ],
      NOW,
    );

    expect(result.map((item) => item.provider)).toEqual(["grok"]);
  });

  it("uses the newest snapshot when a provider has multiple runtimes", () => {
    const result = collectProviderQuotas(
      [
        runtime("grok", NOW / 1000 - 120, {
          provider: "grok",
          status: "available",
          observed_at: NOW / 1000 - 120,
          windows: [{ name: "credits", used_percent: 80 }],
        }),
        runtime("grok", NOW / 1000 - 30, {
          provider: "grok",
          status: "available",
          observed_at: NOW / 1000 - 30,
          windows: [{ name: "credits", used_percent: 20 }],
        }),
      ],
      NOW,
    );

    expect(result[0]?.snapshot.observed_at).toBe(NOW / 1000 - 30);
    expect(result[0]?.windows[0]?.used_percent).toBe(20);
  });
});
