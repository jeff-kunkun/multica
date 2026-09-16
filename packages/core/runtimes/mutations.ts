import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { runtimeKeys } from "./queries";
import { workspaceKeys } from "../workspace/queries";
import { agentTaskSnapshotKeys } from "../agents/queries";
import type { TransferRuntimeBinding } from "../api/config-transfer";

export function useDeleteRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (runtimeId: string) => api.deleteRuntime(runtimeId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}

// Confirmed-delete counterpart to useDeleteRuntime. The dialog routes here when
// the strict DELETE refused with `runtime_has_active_agents` (or when the
// caller already knows the runtime has active agents and wants to skip the
// pre-flight refusal). Mutation fn returns the server-reported counts so
// the caller can render a richer success toast.
//
// Invalidates runtimes (the list / detail), workspace agents (they are unbound,
// so their runtime column and readiness change) and the agent presence snapshot
// (the delete also cancels queued/running tasks). Without the agent-side
// invalidation the Agents page would keep showing them as runnable.
export function useUnbindAgentsAndDeleteRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      runtimeId,
      expectedActiveAgentIds,
    }: {
      runtimeId: string;
      expectedActiveAgentIds: string[];
    }) => api.unbindAgentsAndDeleteRuntime(runtimeId, expectedActiveAgentIds),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      qc.invalidateQueries({ queryKey: agentTaskSnapshotKeys.all(wsId) });
    },
  });
}

// useUpdateRuntime patches editable fields on a runtime (visibility, custom
// name). Invalidates the runtime list so the picker disabled-state and
// display names recompute.
export function useUpdateRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      runtimeId,
      patch,
    }: {
      runtimeId: string;
      patch: {
        visibility?: "private" | "public";
        // Empty string clears the custom name; omit to leave unchanged.
        custom_name?: string;
        apply_to_machine?: boolean;
      };
    }) => api.updateRuntime(runtimeId, patch),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}

// useBindTransferRuntimes applies the runtimes a human picked on the workspace
// migration card after a cross-instance import (DENE-364). The server decides
// per binding, so the mutation resolves with the per-row report instead of
// throwing on a partial failure; the caller renders which rows landed.
//
// Invalidates the workspace agents (their runtime column and readiness change)
// and the runtimes list (a newly used runtime shows an active agent).
export function useBindTransferRuntimes(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (bindings: TransferRuntimeBinding[]) =>
      api.bindTransferRuntimes(wsId, bindings),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}
