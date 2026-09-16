import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { projectKeys } from "./queries";
import type { ProjectMember } from "../types";

export const projectMemberKeys = {
  list: (wsId: string, projectId: string) =>
    [...projectKeys.detail(wsId, projectId), "members"] as const,
};

export function projectMembersOptions(wsId: string, projectId: string) {
  return queryOptions({
    queryKey: projectMemberKeys.list(wsId, projectId),
    queryFn: () => api.listProjectMembers(projectId),
  });
}

export function useAddProjectMember(wsId: string, projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (memberId: string) =>
      api.addProjectMember(projectId, { member_id: memberId }),
    onSuccess: (created) => {
      qc.setQueryData<ProjectMember[]>(
        projectMemberKeys.list(wsId, projectId),
        (old) => {
          if (!old) return old;
          if (old.some((m) => m.member_id === created.member_id)) return old;
          return [...old, created];
        },
      );
    },
    onSettled: () => {
      qc.invalidateQueries({
        queryKey: projectMemberKeys.list(wsId, projectId),
      });
    },
  });
}

export function useRemoveProjectMember(wsId: string, projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (memberId: string) => api.removeProjectMember(projectId, memberId),
    onMutate: async (memberId) => {
      await qc.cancelQueries({
        queryKey: projectMemberKeys.list(wsId, projectId),
      });
      const prev = qc.getQueryData<ProjectMember[]>(
        projectMemberKeys.list(wsId, projectId),
      );
      qc.setQueryData<ProjectMember[]>(
        projectMemberKeys.list(wsId, projectId),
        (old) => old?.filter((m) => m.member_id !== memberId),
      );
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) {
        qc.setQueryData(projectMemberKeys.list(wsId, projectId), ctx.prev);
      }
    },
    onSettled: () => {
      qc.invalidateQueries({
        queryKey: projectMemberKeys.list(wsId, projectId),
      });
    },
  });
}
