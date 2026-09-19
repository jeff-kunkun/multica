/**
 * The routing configuration stored in `workspace.settings.routing` (DENE-633).
 *
 * This file is the client half of a cross-surface contract: the field names
 * and types here must match `server/internal/routing/settings.go` exactly.
 * Renaming one breaks every workspace that has already been configured, so
 * they are named once, here and there, and nowhere else.
 */

/** The key the routing block lives under inside `workspace.settings`. */
export const ROUTING_SETTINGS_KEY = "routing";

/**
 * The confidence floor applied when settings carry no explicit one. Mirrors
 * DefaultConfidenceThreshold on the server; a client that guessed a different
 * default would show a threshold the server does not apply.
 */
export const DEFAULT_CONFIDENCE_THRESHOLD = 0.7;

export interface RoutingSettings {
  enabled: boolean;
  /**
   * Model identifier for the routing judge. Empty while `enabled` is true is
   * the "incomplete" state — somebody flipped the switch and stopped.
   */
  model: string;
  /** Confidence floor in (0, 1]. */
  confidence_threshold: number;
}

/**
 * What the section shows. Derived, never stored — a stored copy would drift
 * from the three fields above.
 *
 * - `off`          switch off (the default). Completely the pre-routing product.
 * - `incomplete`   switch on, no model chosen. ALSO completely the pre-routing
 *                  product — which is exactly why it has to be shown: somebody
 *                  who flipped the switch will otherwise believe it is working.
 * - `enabled`      switch on and a model chosen.
 * - `ineffective`  configured, but the model is rejected, unreachable, deleted,
 *                  or cooling down after repeated failures. Also the pre-routing
 *                  product, and the reason is shown HERE and never on a ticket.
 */
export type RoutingState = "off" | "incomplete" | "enabled" | "ineffective";

export const DEFAULT_ROUTING_SETTINGS: RoutingSettings = {
  enabled: false,
  model: "",
  confidence_threshold: DEFAULT_CONFIDENCE_THRESHOLD,
};

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/**
 * Read the routing block out of a workspace's settings.
 *
 * Every unreadable shape — missing block, null, wrong type, a string where a
 * number belongs — yields the defaults, which is the switched-off state. A
 * settings payload this client cannot interpret must never render as enabled.
 */
export function parseRoutingSettings(
  settings: Record<string, unknown> | null | undefined,
): RoutingSettings {
  const block = settings?.[ROUTING_SETTINGS_KEY];
  if (!isRecord(block)) return { ...DEFAULT_ROUTING_SETTINGS };
  return {
    enabled: block.enabled === true,
    model: typeof block.model === "string" ? block.model : "",
    confidence_threshold: normalizeThreshold(block.confidence_threshold),
  };
}

/**
 * Clamp a stored or typed threshold to something the server will honour.
 * Anything outside (0, 1] falls back to the default rather than to a value
 * that would accept every verdict.
 */
export function normalizeThreshold(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return DEFAULT_CONFIDENCE_THRESHOLD;
  }
  if (value <= 0 || value > 1) return DEFAULT_CONFIDENCE_THRESHOLD;
  return value;
}

/**
 * Classify the stored fields.
 *
 * `ineffective` is never derived here: it depends on live model health, which
 * only the server knows. Pass the server's health report to get it.
 */
export function routingState(
  settings: RoutingSettings,
  health?: { usable: boolean } | null,
): RoutingState {
  if (!settings.enabled) return "off";
  if (settings.model.trim() === "") return "incomplete";
  if (health && health.usable === false) return "ineffective";
  return "enabled";
}

/** Only `enabled` routes issues. The other three are the pre-routing product. */
export function routingIsActive(state: RoutingState): boolean {
  return state === "enabled";
}

/**
 * Merge a routing block back into a full settings object for the update call.
 * The rest of `settings` is carried through untouched: this section shares the
 * column with everything else the workspace stores.
 */
export function withRoutingSettings(
  settings: Record<string, unknown> | null | undefined,
  next: RoutingSettings,
): Record<string, unknown> {
  return {
    ...(settings ?? {}),
    [ROUTING_SETTINGS_KEY]: {
      enabled: next.enabled,
      model: next.model.trim(),
      confidence_threshold: normalizeThreshold(next.confidence_threshold),
    },
  };
}
