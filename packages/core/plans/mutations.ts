import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { issueKeys } from "../issues/queries";
import { planKeys } from "./queries";

/** Approving creates child issues and starts runs: refresh plans and issues. */
export function useApprovePlan(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ issueId, version }: { issueId: string; version: number }) =>
      api.approvePlan(issueId, version),
    onSettled: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: planKeys.all(wsId) }),
        queryClient.invalidateQueries({ queryKey: issueKeys.all(wsId) }),
      ]),
  });
}

export function useAddPlanApprover(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) => api.addPlanApprover(userId),
    onSettled: () => queryClient.invalidateQueries({ queryKey: planKeys.all(wsId) }),
  });
}

export function useRemovePlanApprover(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) => api.removePlanApprover(userId),
    onSettled: () => queryClient.invalidateQueries({ queryKey: planKeys.all(wsId) }),
  });
}
