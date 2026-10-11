import { infiniteQueryOptions } from "@tanstack/react-query";
import { api } from "../api";

export function fastAccessOptions(userId: string, wsId: string, allVisible: boolean, since: string) {
  return infiniteQueryOptions({
    queryKey: ["fast-access", userId, wsId, allVisible, since],
    initialPageParam: null as string | null,
    queryFn: ({ pageParam }) => api.readFastAccess(pageParam
      ? { cursor: pageParam, limit: 100, max_bytes: 50000 }
      : { scope: allVisible ? { mode: "all_visible" } : { mode: "workspace_ids", workspace_ids: [wsId] },
        since, include: ["active_runs", "chats"], limit: 100, max_bytes: 50000 }),
    getNextPageParam: (page) => page.complete ? undefined : page.nextCursor,
    retry: false,
    staleTime: Infinity,
    gcTime: 0,
  });
}
