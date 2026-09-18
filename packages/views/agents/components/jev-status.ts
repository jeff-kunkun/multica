import type { Agent, AgentRuntime, JevStatusSnapshot } from "@multica/core/types";
import { deriveRuntimeHealth } from "@multica/core/runtimes";

/**
 * The skill that turns the JEV judgement layer on for an agent. Kept in one
 * place because "is this seat on JEV?" is asked by the resolver and by anyone
 * filtering the agent list.
 */
export const JEV_SKILL_NAME = "jev";

/**
 * Resolved JEV presentation state. The precedence is fixed and deliberate:
 *
 *   off (no jev skill) > unknown (no runtime / runtime offline / no snapshot /
 *   unparseable snapshot) > the snapshot's own status.
 *
 * `unknown` folds in the runtime's liveness on purpose: a daemon that went
 * offline leaves its last `active` snapshot in the database, and that stale
 * value must stop claiming availability the moment the machine is gone. The
 * snapshot itself cannot carry that fact.
 */
export type JevState =
  | { kind: "off" }
  | { kind: "unknown" }
  | {
      kind: "active";
      model?: string;
      lastScene?: string;
      lastOutcome?: string;
      /** Unix seconds of the tail decision, absent when the log is empty. */
      lastDecisionAt?: number;
    }
  | {
      kind: "fallback";
      reason?: string;
      manual: boolean;
      /** Unix seconds; the cooldown label is derived client-side from this. */
      disabledUntil?: number;
    };

/** True when the seat is bound to the jev skill and it is not disabled. */
export function agentHasJevSkill(agent: Pick<Agent, "skills">): boolean {
  return (agent.skills ?? []).some(
    (skill) => skill.name === JEV_SKILL_NAME && skill.enabled !== false,
  );
}

export function resolveJevState(
  agent: Pick<Agent, "skills">,
  runtime: AgentRuntime | null | undefined,
  nowMs = Date.now(),
): JevState {
  if (!agentHasJevSkill(agent)) {
    return { kind: "off" };
  }
  if (!runtime || deriveRuntimeHealth(runtime, nowMs) !== "online") {
    return { kind: "unknown" };
  }
  const snapshot: JevStatusSnapshot | null | undefined = runtime.jev;
  if (!snapshot || snapshot.status === "unknown") {
    return { kind: "unknown" };
  }
  if (snapshot.status === "fallback") {
    return {
      kind: "fallback",
      reason: nonEmpty(snapshot.reason),
      manual: snapshot.manual === true,
      disabledUntil:
        snapshot.disabled_until != null && snapshot.disabled_until > 0
          ? snapshot.disabled_until
          : undefined,
    };
  }
  return {
    kind: "active",
    model: nonEmpty(snapshot.model),
    lastScene: nonEmpty(snapshot.last_scene),
    lastOutcome: nonEmpty(snapshot.last_outcome),
    lastDecisionAt:
      snapshot.last_decision_at != null && snapshot.last_decision_at > 0
        ? snapshot.last_decision_at
        : undefined,
  };
}

function nonEmpty(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  return trimmed ? trimmed : undefined;
}
