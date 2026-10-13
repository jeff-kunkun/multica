import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { RuntimeCapabilitiesResult } from "../types";

export const runtimeCapabilitiesKeys = {
  all: () => ["runtimes", "capabilities"] as const,
  forRuntime: (runtimeId: string) =>
    [...runtimeCapabilitiesKeys.all(), runtimeId] as const,
};

const POLL_INTERVAL_MS = 500;
const POLL_TIMEOUT_MS = 30_000;

export async function resolveRuntimeCapabilities(
  runtimeId: string,
): Promise<RuntimeCapabilitiesResult> {
  const initial = await api.initiateRuntimeCapabilities(runtimeId);
  const start = Date.now();
  let current = initial;

  while (current.status === "pending" || current.status === "running") {
    if (Date.now() - start > POLL_TIMEOUT_MS) {
      throw new Error("runtime capability discovery timed out");
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
    current = await api.getRuntimeCapabilitiesResult(runtimeId, initial.id);
  }

  if (current.status === "failed" || current.status === "timeout") {
    throw new Error(current.error || "runtime capability discovery failed");
  }

  return {
    mcpServers: current.mcp_servers ?? [],
    mcpSupported: current.mcp_supported === true,
  };
}

// The daemon reports a redacted MCP inventory for the runtime; Agent detail
// surfaces show it next to the agent's own MCP config.
export function runtimeCapabilitiesOptions(runtimeId: string | null | undefined) {
  return queryOptions({
    queryKey: runtimeId
      ? runtimeCapabilitiesKeys.forRuntime(runtimeId)
      : runtimeCapabilitiesKeys.all(),
    queryFn: () => resolveRuntimeCapabilities(runtimeId as string),
    enabled: Boolean(runtimeId),
    staleTime: 30_000,
    retry: false,
  });
}
