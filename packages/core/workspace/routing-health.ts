import { z } from "zod";

import { parseWithFallback } from "../api/schema";
import type { RoutingState } from "./routing-settings";

/**
 * The server's read-only report on whether routing is actually working
 * (DENE-633).
 *
 * Why this exists at all: the routing design keeps failures OFF tickets on
 * purpose — a broken model must not leave a trail of identical comments. That
 * makes the settings section the only place a person can find out, so without
 * this endpoint a revoked key is visible nowhere but the server log.
 *
 * This is the client half of `routingHealthResponse` in
 * `server/internal/handler/routing.go`.
 */
export interface RoutingHealth {
  /** The four-way state, server-authoritative. */
  state: RoutingState;
  /** True only when routing is actually doing its work right now. */
  usable: boolean;
  /** Why it is not working, in words. Empty unless `state` is ineffective. */
  reason: string;
  /** Remaining cooldown in seconds; 0 when not cooling down. */
  retry_after_seconds: number;
  /** Unix seconds of the last successful model call; 0 for never. */
  last_success_at: number;
  /** Unix seconds of the last failure; 0 for never. */
  last_failure_at: number;
  model: string;
  threshold: number;
}

/**
 * Deliberately lenient about `state`: an installed desktop build talking to a
 * newer backend must not blank the section because the server grew a fifth
 * state name. `normalizeRoutingHealth` maps anything unrecognised onto the
 * safe reading below.
 */
export const RoutingHealthSchema = z.object({
  state: z.string(),
  usable: z.boolean().optional(),
  reason: z.string().optional(),
  retry_after_seconds: z.number().optional(),
  last_success_at: z.number().optional(),
  last_failure_at: z.number().optional(),
  model: z.string().optional(),
  threshold: z.number().optional(),
});

/**
 * What the UI shows when the server said nothing usable.
 *
 * `state: "off"` with `usable: false` is the honest fallback: a client that
 * cannot read the health report does not know routing is working, and the one
 * reading it must never invent is a green "enabled" chip.
 */
export const UNKNOWN_ROUTING_HEALTH: RoutingHealth = {
  state: "off",
  usable: false,
  reason: "",
  retry_after_seconds: 0,
  last_success_at: 0,
  last_failure_at: 0,
  model: "",
  threshold: 0,
};

const KNOWN_STATES: readonly RoutingState[] = [
  "off",
  "incomplete",
  "enabled",
  "ineffective",
];

/** Narrow a server-supplied state, defaulting an unknown one to `off`. */
export function normalizeRoutingState(value: string): RoutingState {
  return (KNOWN_STATES as readonly string[]).includes(value)
    ? (value as RoutingState)
    : "off";
}

/** Parse a health response, falling back rather than throwing. */
export function parseRoutingHealth(raw: unknown): RoutingHealth {
  const parsed = parseWithFallback(
    raw,
    RoutingHealthSchema,
    UNKNOWN_ROUTING_HEALTH,
    { endpoint: "GET /api/workspaces/{id}/routing/health" },
  );
  return {
    state: normalizeRoutingState(parsed.state ?? ""),
    // `=== true`, not truthy: a server that starts sending the field as a
    // string must not read as "routing is fine".
    usable: parsed.usable === true,
    reason: typeof parsed.reason === "string" ? parsed.reason : "",
    retry_after_seconds: toCount(parsed.retry_after_seconds),
    last_success_at: toCount(parsed.last_success_at),
    last_failure_at: toCount(parsed.last_failure_at),
    model: typeof parsed.model === "string" ? parsed.model : "",
    threshold: typeof parsed.threshold === "number" ? parsed.threshold : 0,
  };
}

function toCount(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? value
    : 0;
}
