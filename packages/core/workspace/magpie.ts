import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { workspaceKeys } from "./queries";

/**
 * The workspace's magpie mode switch. Off (the default) keeps the old
 * behaviour: editable agents page, archive only, routing groups pick the
 * model. On adds magpie group selection, the read-only agents mirror and
 * real delete with backup.
 */
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
