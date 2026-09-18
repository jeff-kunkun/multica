// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Agent, AgentRuntime, JevStatusSnapshot } from "@multica/core/types";
import {
  agentHasJevSkill,
  resolveJevState,
} from "./jev-status";

// Canonical matrix for JEV state resolution and the cooldown/elapsed labels.
// The component suite keeps only the three happy-path renders and points here
// for the boundary cases.

const NOW = new Date("2026-09-18T12:00:00Z").getTime();

function runtime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: "daemon-1",
    name: "codex",
    runtime_mode: "local",
    provider: "codex",
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: "user-1",
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-09-01T00:00:00Z",
    updated_at: "2026-09-01T00:00:00Z",
    ...overrides,
  };
}

function agent(skills: Array<{ name: string; enabled?: boolean }>): Pick<Agent, "skills"> {
  return {
    skills: skills.map((skill, index) => ({
      id: `skill-${index}`,
      name: skill.name,
      description: "",
      ...(skill.enabled === undefined ? {} : { enabled: skill.enabled }),
    })),
  } as Pick<Agent, "skills">;
}

function snapshot(overrides: Partial<JevStatusSnapshot> = {}): JevStatusSnapshot {
  return { status: "active", observed_at: 1_800_000_000, ...overrides };
}

describe("agentHasJevSkill", () => {
  it("matches the jev skill by name", () => {
    expect(agentHasJevSkill(agent([{ name: "jev" }]))).toBe(true);
    expect(agentHasJevSkill(agent([{ name: "other" }]))).toBe(false);
    expect(agentHasJevSkill({ skills: [] })).toBe(false);
  });

  it("treats an explicitly disabled skill as not bound", () => {
    expect(agentHasJevSkill(agent([{ name: "jev", enabled: false }]))).toBe(false);
    // Missing `enabled` means enabled: older payloads omit the field.
    expect(agentHasJevSkill(agent([{ name: "jev", enabled: undefined }]))).toBe(true);
    expect(agentHasJevSkill(agent([{ name: "jev", enabled: true }]))).toBe(true);
  });
});

describe("resolveJevState", () => {
  it("is off without the skill, whatever the runtime says", () => {
    expect(resolveJevState(agent([]), runtime({ jev: snapshot() }), NOW)).toEqual({
      kind: "off",
    });
    expect(resolveJevState(agent([{ name: "jev", enabled: false }]), null, NOW)).toEqual({
      kind: "off",
    });
  });

  it("is unknown when the runtime is missing or offline", () => {
    expect(resolveJevState(agent([{ name: "jev" }]), null, NOW)).toEqual({ kind: "unknown" });
    expect(
      resolveJevState(
        agent([{ name: "jev" }]),
        runtime({ status: "offline", jev: snapshot() }),
        NOW,
      ),
    ).toEqual({ kind: "unknown" });
  });

  it("is unknown when the snapshot is missing or unparseable", () => {
    expect(resolveJevState(agent([{ name: "jev" }]), runtime({ jev: null }), NOW)).toEqual({
      kind: "unknown",
    });
    expect(resolveJevState(agent([{ name: "jev" }]), runtime({}), NOW)).toEqual({
      kind: "unknown",
    });
    // `parseWithFallback` degrades an unknown enum value to the "unknown"
    // status rather than dropping the field.
    expect(
      resolveJevState(
        agent([{ name: "jev" }]),
        runtime({ jev: snapshot({ status: "unknown" }) }),
        NOW,
      ),
    ).toEqual({ kind: "unknown" });
  });

  it("reports active with the snapshot detail, trimming blanks", () => {
    const state = resolveJevState(
      agent([{ name: "jev" }]),
      runtime({
        jev: snapshot({
          status: "active",
          model: "jev-1.13.0",
          last_scene: "pr-risk",
          last_outcome: "@1",
          last_decision_at: 1_800_000_000,
        }),
      }),
      NOW,
    );
    expect(state).toEqual({
      kind: "active",
      model: "jev-1.13.0",
      lastScene: "pr-risk",
      lastOutcome: "@1",
      lastDecisionAt: 1_800_000_000,
    });

    expect(
      resolveJevState(
        agent([{ name: "jev" }]),
        runtime({ jev: snapshot({ model: "  ", last_outcome: "" }) }),
        NOW,
      ),
    ).toEqual({
      kind: "active",
      model: undefined,
      lastScene: undefined,
      lastOutcome: undefined,
      lastDecisionAt: undefined,
    });
  });

  it("reports fallback with the reason and the raw cooldown deadline", () => {
    const until = Math.floor(NOW / 1000) + 42 * 60;
    expect(
      resolveJevState(
        agent([{ name: "jev" }]),
        runtime({
          jev: snapshot({
            status: "fallback",
            reason: "额度见底",
            manual: true,
            disabled_until: until,
          }),
        }),
        NOW,
      ),
    ).toEqual({ kind: "fallback", reason: "额度见底", manual: true, disabledUntil: until });
  });

  it("drops a non-positive or absent cooldown deadline", () => {
    expect(
      resolveJevState(
        agent([{ name: "jev" }]),
        runtime({ jev: snapshot({ status: "fallback", reason: "已手动停用", manual: true }) }),
        NOW,
      ),
    ).toEqual({ kind: "fallback", reason: "已手动停用", manual: true, disabledUntil: undefined });
    expect(
      resolveJevState(
        agent([{ name: "jev" }]),
        runtime({ jev: snapshot({ status: "fallback", disabled_until: 0 }) }),
        NOW,
      ),
    ).toEqual({ kind: "fallback", reason: undefined, manual: false, disabledUntil: undefined });
  });

  it("lets off win over an offline runtime with a live snapshot", () => {
    expect(
      resolveJevState(agent([]), runtime({ status: "offline", jev: snapshot() }), NOW),
    ).toEqual({ kind: "off" });
  });
});
