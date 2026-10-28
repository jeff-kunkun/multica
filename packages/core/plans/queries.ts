import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const planKeys = {
  all: () => ["issue-plans"] as const,
  issue: (issueId: string) => ["issue-plans", issueId] as const,
};

export const issuePlansOptions = (issueId: string) =>
  queryOptions({
    queryKey: planKeys.issue(issueId),
    queryFn: () => api.listIssuePlans(issueId),
    enabled: !!issueId,
  });
