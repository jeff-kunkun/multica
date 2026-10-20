import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import type {
  AgentAccessPass,
  CreateAgentAccessPassRequest,
} from "../types";
import { workspaceKeys } from "../workspace/queries";

/**
 * Agent borrowing (kun fork, DENE-808): timed access passes.
 *
 * Passes are server data — a pass expires on the server's clock — so they
 * live in TanStack Query under workspace-scoped keys, never in a Zustand store.
 */
export const agentAccessKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-access"] as const,
  passes: (wsId: string, agentId: string) =>
    [...agentAccessKeys.all(wsId), "passes", agentId] as const,
};

export function agentAccessPassesOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: agentAccessKeys.passes(wsId, agentId),
    queryFn: () => api.listAgentAccessPasses(agentId),
  });
}

export function useAgentAccessPasses(agentId: string, enabled = true) {
  const wsId = useWorkspaceId();
  return useQuery({ ...agentAccessPassesOptions(wsId, agentId), enabled: enabled && !!agentId });
}

export function useCreateAgentAccessPass(agentId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation<AgentAccessPass, Error, CreateAgentAccessPassRequest>({
    mutationFn: (body) => api.createAgentAccessPass(agentId, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: agentAccessKeys.passes(wsId, agentId) });
      void qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    },
  });
}

export function useRevokeAgentAccessPass(agentId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation<AgentAccessPass, Error, string>({
    mutationFn: (passId) => api.revokeAgentAccessPass(agentId, passId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: agentAccessKeys.passes(wsId, agentId) });
      void qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    },
  });
}

/** Preset pass lengths offered in the settings page. */
export type AgentAccessPassPreset = "2h" | "today" | "custom";

/**
 * Turn a preset into the request body the server accepts. `today` means the
 * end of the caller's local day; `custom` needs an explicit expiry.
 */
export function agentAccessPassExpiry(
  preset: AgentAccessPassPreset,
  customExpiresAt?: Date,
  now: Date = new Date(),
): { expires_at?: string; duration_minutes?: number } | null {
  switch (preset) {
    case "2h":
      return { duration_minutes: 120 };
    case "today": {
      const end = new Date(now);
      end.setHours(23, 59, 59, 999);
      if (end.getTime() - now.getTime() < 60_000) {
        // Less than a minute of "today" left — roll to end of tomorrow so the
        // grant is never dead on arrival.
        end.setDate(end.getDate() + 1);
      }
      return { expires_at: end.toISOString() };
    }
    case "custom":
      if (!customExpiresAt || Number.isNaN(customExpiresAt.getTime())) return null;
      if (customExpiresAt.getTime() <= now.getTime()) return null;
      return { expires_at: customExpiresAt.toISOString() };
  }
}
