"use client";

import type { Agent, AgentRuntime } from "@multica/core/types";
import { useT, useTimeAgo } from "../../i18n";
import { compactRemaining } from "./agent-quota";
import { resolveJevState } from "./jev-status";

/**
 * One-line JEV (fast judgement layer) indicator shared by the agent hover card
 * and the Overview summary.
 *
 * Resolved state and precedence live in ./jev-status (with its matrix test);
 * this component only picks the copy and the tone. `fallback` is deliberately
 * the loudest state — it is the visible evidence that a seat has dropped back
 * to the model it was meant to bypass.
 */
export function AgentJevIndicator({
  agent,
  runtime,
  now = Date.now(),
  labeled = true,
}: {
  agent: Pick<Agent, "skills">;
  runtime: AgentRuntime | null;
  now?: number;
  labeled?: boolean;
}) {
  const { t } = useT("agents");
  const timeAgo = useTimeAgo();
  const state = resolveJevState(agent, runtime, now);
  const label = t(($) => $.jev.label);

  let chipTone = "bg-muted text-muted-foreground";
  let statusText: string;
  let detail: string | null = null;

  switch (state.kind) {
    case "off":
      statusText = t(($) => $.jev.status_off);
      break;
    case "unknown":
      statusText = t(($) => $.jev.status_unknown);
      break;
    case "active": {
      chipTone = "bg-success/10 text-success";
      statusText = t(($) => $.jev.status_active);
      const decision =
        state.lastOutcome ??
        state.lastScene ??
        null;
      const ago = state.lastDecisionAt
        ? timeAgo(new Date(state.lastDecisionAt * 1000).toISOString())
        : null;
      detail = joinParts([state.model ?? null, decision, ago]);
      break;
    }
    case "fallback": {
      chipTone = "bg-warning/10 text-warning";
      statusText = t(($) => $.jev.status_fallback);
      const reason =
        state.reason ??
        t(($) => (state.manual ? $.jev.reason_manual : $.jev.reason_upstream));
      // Cooldown remaining is derived here from the raw deadline so the daemon
      // never has to send a value that changes on every heartbeat.
      const cooldown =
        state.disabledUntil != null
          ? compactRemaining(state.disabledUntil, now)
          : null;
      detail = joinParts([
        reason,
        cooldown ? t(($) => $.jev.cooldown, { when: cooldown }) : null,
      ]);
      break;
    }
    default: {
      // Server-driven enum: an unrecognised value must not blank the row.
      const exhaustive: never = state;
      void exhaustive;
      statusText = t(($) => $.jev.status_unknown);
      break;
    }
  }

  return (
    <div
      className="flex items-center gap-1.5"
      aria-label={label}
      data-jev-state={state.kind}
    >
      {labeled && (
        <span className="w-12 shrink-0 text-muted-foreground">{label}</span>
      )}
      <span
        className={`shrink-0 rounded-md px-1.5 py-0.5 text-micro font-medium ${chipTone}`}
      >
        {statusText}
      </span>
      {detail && (
        <span
          className="min-w-0 truncate text-micro text-muted-foreground"
          title={detail}
        >
          {detail}
        </span>
      )}
    </div>
  );
}

function joinParts(parts: Array<string | null>): string | null {
  const present = parts.filter((part): part is string => !!part);
  return present.length > 0 ? present.join(" · ") : null;
}
