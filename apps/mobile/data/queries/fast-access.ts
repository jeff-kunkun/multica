import { infiniteQueryOptions } from "@tanstack/react-query";
import { api } from "@/data/api";

export const fastAccessKeys = {
  snapshot: (userId: string, wsId: string | null, allVisible: boolean, since: string) =>
    ["fast-access", userId, wsId, allVisible, since] as const,
};

export function fastAccessOptions(userId: string, wsId: string | null, allVisible: boolean, since: string) {
  return infiniteQueryOptions({
    queryKey: fastAccessKeys.snapshot(userId, wsId, allVisible, since),
    enabled: !!userId && !!wsId,
    initialPageParam: null as string | null,
    queryFn: ({ pageParam, signal }) => api.readFastAccess(pageParam
      ? { cursor: pageParam, limit: 100, max_bytes: 50000 }
      : { scope: allVisible ? { mode: "all_visible" } : { mode: "workspace_ids", workspace_ids: [wsId!] },
        since, include: ["active_runs", "chats"], limit: 100, max_bytes: 50000 }, { signal }),
    getNextPageParam: (page) => page.complete ? undefined : page.nextCursor,
    retry: false,
    staleTime: Infinity,
    gcTime: 0,
  });
}
