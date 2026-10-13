import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { projectKeys } from "./queries";

// The workspace members a project is shared with. Only the workspace owner
// can change the list; everyone who can see the project can read it.
export const projectMemberKeys = {
  list: (wsId: string, projectId: string) =>
    [...projectKeys.detail(wsId, projectId), "members"] as const,
};

export function projectMembersOptions(wsId: string, projectId: string) {
  return queryOptions({
    queryKey: projectMemberKeys.list(wsId, projectId),
    queryFn: () => api.listProjectMembers(projectId),
    select: (data) => data.members,
  });
}

function useInvalidateProjectMembers(wsId: string, projectId: string) {
  const qc = useQueryClient();
  return () => {
    qc.invalidateQueries({ queryKey: projectMemberKeys.list(wsId, projectId) });
    // Sharing changes who can see the project, so the list may change too.
    qc.invalidateQueries({ queryKey: projectKeys.list(wsId) });
  };
}

export function useAddProjectMember(wsId: string, projectId: string) {
  const invalidate = useInvalidateProjectMembers(wsId, projectId);
  return useMutation({
    mutationFn: (userId: string) => api.addProjectMember(projectId, userId),
    onSettled: invalidate,
  });
}

export function useRemoveProjectMember(wsId: string, projectId: string) {
  const invalidate = useInvalidateProjectMembers(wsId, projectId);
  return useMutation({
    mutationFn: (userId: string) => api.removeProjectMember(projectId, userId),
    onSettled: invalidate,
  });
}
