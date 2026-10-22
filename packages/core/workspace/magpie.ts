import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { workspaceKeys } from "./queries";

/**
 * The workspace's magpie mode switch. Off (the default) keeps the official
 * behaviour for every agent. On, agents whose runtime magpie routes through
 * a group get group selection, the magpie detail page and real delete with
 * backup; the rest stay native.
 */
export function isWorkspaceMagpieEnabled(settings: unknown): boolean {
  if (!settings || typeof settings !== "object") return false;
  const magpie = (settings as Record<string, unknown>).magpie;
  if (!magpie || typeof magpie !== "object") return false;
  return (magpie as Record<string, unknown>).enabled === true;
}

export function workspaceMagpieOptions(wsId: string) {
  return queryOptions({
    queryKey: [...workspaceKeys.all(wsId), "magpie"] as const,
    queryFn: () => api.getWorkspaceMagpie(wsId),
    enabled: !!wsId,
  });
}

export function useUpdateWorkspaceMagpie(wsId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (enabled: boolean) => api.updateWorkspaceMagpie(wsId, enabled),
    onSuccess: (state) => client.setQueryData(workspaceMagpieOptions(wsId).queryKey, state),
    onSettled: () => client.invalidateQueries({ queryKey: workspaceKeys.list() }),
  });
}
