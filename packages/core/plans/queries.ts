import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const planKeys = {
  all: (wsId: string) => ["plans", wsId] as const,
  issue: (wsId: string, issueId: string) => ["plans", wsId, "issue", issueId] as const,
  approvers: (wsId: string) => ["plans", wsId, "approvers"] as const,
};

/** Plan versions on one issue plus whether the caller may approve. */
export function issuePlansOptions(wsId: string, issueId: string) {
  return queryOptions({
    queryKey: planKeys.issue(wsId, issueId),
    queryFn: () => api.listIssuePlans(issueId),
    staleTime: 30_000,
  });
}

export function planApproverListOptions(wsId: string, enabled = true) {
  return queryOptions({
    queryKey: planKeys.approvers(wsId),
    queryFn: () => api.listPlanApprovers(),
    enabled,
  });
}
