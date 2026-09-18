// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Agent, AgentRuntime } from "@multica/core/types";
import enAgents from "../../locales/en/agents.json";
import enCommon from "../../locales/en/common.json";
import { AgentJevIndicator } from "./agent-jev-indicator";

// Happy-path renders for the three user-visible states. The full
// off/unknown/active/fallback boundary matrix lives beside the resolver in
// jev-status.test.ts — do not re-run it through a DOM mount.

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };
const NOW = Date.UTC(2026, 8, 18, 12);

function makeAgent(withJevSkill: boolean): Pick<Agent, "skills"> {
  return {
    skills: withJevSkill
      ? [{ id: "s-1", name: "jev", description: "", enabled: true }]
      : [],
  } as Pick<Agent, "skills">;
}

function makeRuntime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: "d-1",
    name: "Codex (host)",
    runtime_mode: "local",
    provider: "codex",
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: "u-1",
    visibility: "private",
    last_seen_at: new Date(NOW).toISOString(),
    created_at: new Date(NOW).toISOString(),
    updated_at: new Date(NOW).toISOString(),
    ...overrides,
  };
}

function renderIndicator(ui: React.ReactNode) {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {ui}
    </I18nProvider>,
  );
}

describe("AgentJevIndicator", () => {
  it("renders the active state with the model and the last decision", () => {
    // The relative-time label reads the real clock (useTimeAgo), so the
    // fixture is anchored to it; only the cooldown/health paths use `now`.
    const decisionAt = Math.floor(Date.now() / 1000) - 5 * 60;
    const runtime = makeRuntime({
      jev: {
        status: "active",
        model: "jev-1.13.0",
        last_outcome: "@1",
        last_decision_at: decisionAt,
        observed_at: NOW / 1000,
      },
    });
    renderIndicator(
      <AgentJevIndicator agent={makeAgent(true)} runtime={runtime} now={NOW} />,
    );

    expect(screen.getByText(enAgents.jev.label)).toBeInTheDocument();
    expect(screen.getByText(enAgents.jev.status_active)).toBeInTheDocument();
    expect(
      screen.getByText(
        `jev-1.13.0 · @1 · ${enCommon.time.minutes_ago.replace("{{count}}", "5")}`,
      ),
    ).toBeInTheDocument();
  });

  it("renders the fallback state with its reason and cooldown", () => {
    const runtime = makeRuntime({
      jev: {
        status: "fallback",
        reason: "额度见底",
        manual: true,
        disabled_until: NOW / 1000 + 42 * 60,
        observed_at: NOW / 1000,
      },
    });
    renderIndicator(
      <AgentJevIndicator agent={makeAgent(true)} runtime={runtime} now={NOW} />,
    );

    expect(screen.getByText(enAgents.jev.status_fallback)).toBeInTheDocument();
    expect(
      screen.getByText(
        `额度见底 · ${enAgents.jev.cooldown.replace("{{when}}", "42m")}`,
      ),
    ).toBeInTheDocument();
  });

  it("renders the off state for a seat without the jev skill", () => {
    renderIndicator(
      <AgentJevIndicator
        agent={makeAgent(false)}
        runtime={makeRuntime({ jev: { status: "active", observed_at: NOW / 1000 } })}
        now={NOW}
      />,
    );

    expect(screen.getByText(enAgents.jev.status_off)).toBeInTheDocument();
    expect(screen.queryByText(enAgents.jev.status_active)).not.toBeInTheDocument();
  });
});
