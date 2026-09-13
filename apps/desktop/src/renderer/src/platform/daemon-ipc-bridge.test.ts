// @vitest-environment node
import { describe, expect, it } from "vitest";

import type { AgentRuntime, PlanLimitsSnapshot } from "@multica/core/types";
import { applyLocalDaemonStatus } from "./daemon-ipc-bridge";

function makeRuntime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "rt-codex",
    workspace_id: "ws-1",
    daemon_id: "daemon-1",
    name: "codex",
    runtime_mode: "local",
    provider: "codex",
    launch_header: "",
    status: "offline",
    device_info: "",
    metadata: {},
    owner_id: null,
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

const CODEX_WINDOWS: PlanLimitsSnapshot = {
  provider: "codex",
  status: "available",
  observed_at: 1_800_000_000,
  windows: [
    { name: "primary", used_percent: 12, window_minutes: 300, resets_at: 1_800_001_000 },
    { name: "secondary", used_percent: 4, window_minutes: 10_080, resets_at: 1_800_002_000 },
  ],
};

describe("applyLocalDaemonStatus", () => {
  it("overlays live 5h/7d windows onto a built-in runtime of the same provider", () => {
    const rt = makeRuntime({ plan_limits: null });
    const got = applyLocalDaemonStatus(rt, {
      state: "running",
      daemonId: "daemon-1",
      planLimits: { codex: CODEX_WINDOWS },
    });
    expect(got.status).toBe("online");
    expect(got.plan_limits).toEqual(CODEX_WINDOWS);
  });

  it("does not overlay quota onto a custom-profile runtime", () => {
    const rt = makeRuntime({ profile_id: "profile-1", plan_limits: null });
    const got = applyLocalDaemonStatus(rt, {
      state: "running",
      daemonId: "daemon-1",
      planLimits: { codex: CODEX_WINDOWS },
    });
    expect(got.plan_limits).toBeNull();
  });

  it("leaves a different provider's snapshot untouched", () => {
    const rt = makeRuntime({ provider: "grok", plan_limits: null });
    const got = applyLocalDaemonStatus(rt, {
      state: "running",
      daemonId: "daemon-1",
      planLimits: { codex: CODEX_WINDOWS },
    });
    expect(got.plan_limits).toBeNull();
  });
});
