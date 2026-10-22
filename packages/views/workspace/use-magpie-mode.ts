import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { readRuntimeMagpieGroupRoute, runtimeListOptions } from "@multica/core/runtimes";
import { isWorkspaceMagpieEnabled } from "@multica/core/workspace/magpie";
import { agentListOptions } from "@multica/core/workspace/queries";

/**
 * Whether the current workspace has magpie mode on. Read from the workspace
 * payload already in cache, so pages switch without an extra request; a
 * workspace that has not loaded yet counts as off (the old behaviour).
 */
export function useWorkspaceMagpieEnabled(): boolean {
  return isWorkspaceMagpieEnabled(useCurrentWorkspace()?.settings);
}

/**
 * Whether an agent is a magpie agent: the workspace switch is on and the
 * agent's runtime routes its CLI through a magpie group. Every other agent is
 * native and gets the official pages. Reads as native until the agent and
 * runtime lists load.
 */
export function useIsMagpieAgent(agentId: string): boolean {
  const wsId = useWorkspaceId();
  const enabled = useWorkspaceMagpieEnabled();
  const { data: agents } = useQuery({ ...agentListOptions(wsId), enabled });
  const { data: runtimes } = useQuery({ ...runtimeListOptions(wsId), enabled });
  if (!enabled) return false;
  const agent = agents?.find((a) => a.id === agentId);
  const runtime = agent ? runtimes?.find((r) => r.id === agent.runtime_id) : undefined;
  return readRuntimeMagpieGroupRoute(runtime?.metadata) !== null;
}
