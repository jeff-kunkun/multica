import { infiniteQueryOptions } from "@tanstack/react-query";
import { api } from "../api";

export function fastAccessOptions(userId: string, wsId: string, allVisible: boolean, since: string) {
  return infiniteQueryOptions({
    queryKey: ["fast-access", userId, wsId, allVisible, since],
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam, client, queryKey }) => {
      const initial = { scope: allVisible ? { mode: "all_visible" as const } : { mode: "workspace_ids" as const, workspace_ids: [wsId] },
        include: ["active_runs", "chats"] as ("active_runs" | "chats")[], limit: 100, max_bytes: 50000 };
      // The visible activity window is a current-state snapshot. A complete
      // durable checkpoint belongs to incremental consumers, not this dialog.
      try {
        return await api.readFastAccess(pageParam ? { cursor: pageParam, limit: 100, max_bytes: 50000 } : initial);
      } catch (error) {
        // Never retain summaries after failed access revalidation or expiry.
        client.setQueryData(queryKey, { pages: [], pageParams: [] });
        throw error;
      }
    },
    getNextPageParam: (page) => page.complete ? undefined : page.nextCursor,
    retry: false,
    staleTime: 0,
    refetchInterval: 30_000,
    refetchOnWindowFocus: "always",
    gcTime: 0,
  });
}
