import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import type { UpdateAgentRequest } from "../types";
import { workspaceKeys } from "../workspace/queries";

/**
 * `GET /api/agents/{id}/env` is audited server-side, so its answer is cached
 * for the session instead of being refetched on every mount and focus.
 */
export const AGENT_ENV_STALE_TIME_MS = 5 * 60_000;

/** Query key for one agent's `custom_env` map. Workspace-scoped like every other. */
export function agentEnvQueryKey(wsId: string | null, agentId: string) {
  return ["agent-env", wsId ?? "", agentId] as const;
}

/**
 * The write one account switch performs, as the accounts model describes it:
 * either the agy `--gemini_dir` binding inside `custom_args`, or exactly one
 * environment variable.
 *
 * Deliberately structural rather than an import of the view layer's
 * `AccountSwitchPlan` — core cannot depend on `packages/views`, and the two
 * writable lever shapes are the whole contract.
 */
export type AgentAccountSwitch =
  | { kind: "custom_args"; custom_args: string[]; runtime_config?: Record<string, unknown> }
  | { kind: "env"; key: string; value: string };

/**
 * Apply one account switch to an agent.
 *
 * NOT optimistic, on purpose. Which account is "in effect" is derived from the
 * agent's binding AND the daemon's account report, so a locally patched cache
 * would claim a switch the runtime has not been told about yet; the round-trip
 * here is short and the surfaces that call it stay on screen either way.
 *
 * Env levers cannot ride on `PUT /api/agents/{id}` — `custom_env` is rejected
 * with 400 there — so they re-read the map and change ONLY the target key, and
 * an unrelated variable the user set is written back as-is (values the server
 * masked as `"****"` are preserved by its own guard).
 */
export function useSwitchAgentAccount(agentId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation<void, Error, AgentAccountSwitch>({
    mutationFn: async (change) => {
      if (change.kind === "env") {
        const current = await api.getAgentEnv(agentId);
        const saved = await api.updateAgentEnv(agentId, {
          custom_env: { ...(current.custom_env ?? {}), [change.key]: change.value },
        });
        qc.setQueryData(agentEnvQueryKey(wsId, agentId), saved);
        return;
      }
      // The agy binding and the slot list are both agent fields, so they leave
      // in ONE request: the backend rotates over `runtime_config.agy_slots` and
      // launches with `custom_args`, and committing only half of that pair
      // would leave the agent bound to a directory it may not rotate to.
      const updates: UpdateAgentRequest = { custom_args: change.custom_args };
      if (change.runtime_config !== undefined) {
        updates.runtime_config = change.runtime_config;
      }
      await api.updateAgent(agentId, updates);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    },
  });
}
