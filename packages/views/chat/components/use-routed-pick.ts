"use client";

import { useCurrentWorkspace } from "@multica/core/paths";
import {
  parseRoutingSettings,
  routingIsActive,
  routingState,
} from "@multica/core/workspace/routing-settings";

/**
 * Whether this workspace's router can pick a chat's agent (DENE-1834). The
 * server decides again on create — this only governs whether the chat entry
 * offers 「由路由选派」. Reads the workspace list already in the Query cache.
 */
export function useRoutingPicksAgents(): boolean {
  const workspace = useCurrentWorkspace();
  if (!workspace) return false;
  return routingIsActive(routingState(parseRoutingSettings(workspace.settings)));
}
