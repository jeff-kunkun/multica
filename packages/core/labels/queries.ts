import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { LabelResourceType } from "../types";

export const labelKeys = {
  all: (wsId: string) => ["labels", wsId] as const,
  list: (wsId: string, resourceType: LabelResourceType = "issue") =>
    [...labelKeys.all(wsId), "list", resourceType] as const,
  detail: (wsId: string, id: string) =>
    [...labelKeys.all(wsId), "detail", id] as const,
  byIssue: (wsId: string, issueId: string) =>
    [...labelKeys.all(wsId), "issue", issueId] as const,
};

export function labelListOptions(wsId: string, resourceType: LabelResourceType = "issue") {
  return queryOptions({
    queryKey: labelKeys.list(wsId, resourceType),
    queryFn: () => api.listLabels(resourceType),
    select: (data) => data.labels,
  });
}

export function issueLabelsOptions(wsId: string, issueId: string) {
  return queryOptions({
    queryKey: labelKeys.byIssue(wsId, issueId),
    queryFn: () => api.listLabelsForIssue(issueId),
    select: (data) => data.labels,
    enabled: Boolean(issueId),
  });
}
