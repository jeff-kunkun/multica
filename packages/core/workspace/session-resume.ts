import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { workspaceKeys } from "./queries";

/**
 * The workspace's session context limit (DENE-2756): when an agent session
 * ended its last run past this many tokens of context, after the CLI's own
 * compaction, the next follow-up on the issue starts a new session with a
 * short handoff. 0, the default, means off.
 */
export function workspaceSessionResumeOptions(wsId: string) {
  return queryOptions({
    queryKey: [...workspaceKeys.all(wsId), "session-resume"] as const,
    queryFn: () => api.getWorkspaceSessionResume(wsId),
    enabled: !!wsId,
  });
}

export function useUpdateWorkspaceSessionResume(wsId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (maxContextTokens: number) => api.updateWorkspaceSessionResume(wsId, maxContextTokens),
    onSuccess: (state) => client.setQueryData(workspaceSessionResumeOptions(wsId).queryKey, state),
  });
}
