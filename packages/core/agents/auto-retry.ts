import type { Agent } from "../types";

/**
 * Platform auto-retry is on unless the backend explicitly sent `false`.
 * Older servers omit `auto_retry_enabled`; missing must keep the historical
 * retry behaviour rather than fail closed.
 */
export function isAgentAutoRetryEnabled(
  agent: Pick<Agent, "auto_retry_enabled">,
): boolean {
  return agent.auto_retry_enabled !== false;
}
