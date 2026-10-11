import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api } from "@/data/api";

export const workspaceLinkKeys = {
  all: (wsId: string | null) => ["workspace-links", wsId] as const,
  list: (wsId: string | null) => [...workspaceLinkKeys.all(wsId), "list"] as const,
  lookup: (wsId: string | null, target: string) => [...workspaceLinkKeys.all(wsId), "lookup", target] as const,
  view: (wsId: string | null, id: string, project: string) => [...workspaceLinkKeys.all(wsId), "view", id, project] as const,
  chat: (wsId: string | null, id: string) => [...workspaceLinkKeys.all(wsId), "chat", id] as const,
};
export const workspaceLinksOptions = (wsId: string | null) => queryOptions({
  queryKey: workspaceLinkKeys.list(wsId), queryFn: ({ signal }) => api.listWorkspaceLinks({ signal }), enabled: !!wsId,
});
export const workspaceLinkLookupOptions = (wsId: string | null, target: string, enabled: boolean) => queryOptions({
  queryKey: workspaceLinkKeys.lookup(wsId, target), queryFn: ({ signal }) => api.lookupWorkspaceLink(target, { signal }), enabled: !!wsId && !!target && enabled,
});
export const linkedViewOptions = (wsId: string | null, id: string, project = "") => infiniteQueryOptions({
  queryKey: workspaceLinkKeys.view(wsId, id, project), initialPageParam: "",
  queryFn: ({ signal, pageParam }) => api.getLinkedView(id, project, pageParam, { signal }),
  getNextPageParam: (page) => page.next_cursor || undefined, enabled: !!wsId && !!id,
  refetchInterval: 60_000,
});
export const chatReferenceOptions = (wsId: string | null, id: string) => queryOptions({
  queryKey: workspaceLinkKeys.chat(wsId, id), queryFn: ({ signal }) => api.getChatSession(id, { signal }), enabled: !!wsId && !!id,
});
