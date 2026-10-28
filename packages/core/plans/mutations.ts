import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { planKeys } from "./queries";

// The answer posts a reply under the plan card and changes the gate, so the
// plan list is refetched rather than patched; the reply arrives through the
// ordinary comment realtime event.
export function useApproveIssuePlan(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (version: number) => api.approveIssuePlan(issueId, version),
    onSettled: () => qc.invalidateQueries({ queryKey: planKeys.issue(issueId) }),
  });
}

export function useRequestIssuePlanChanges(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ version, feedback }: { version: number; feedback: string }) =>
      api.requestIssuePlanChanges(issueId, version, feedback),
    onSettled: () => qc.invalidateQueries({ queryKey: planKeys.issue(issueId) }),
  });
}
