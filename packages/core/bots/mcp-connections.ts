import { useCallback } from "react";
import {
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
export type { McpConnection, McpConnectionGrant } from "../api/mcp-connections";

const connectionKey = (wsId: string) => ["mcp-connections", wsId] as const;
export const mcpConnectionsOptions = (wsId: string, enabled = true) =>
  queryOptions({
    queryKey: connectionKey(wsId),
    queryFn: () => api.listMcpConnections(wsId),
    enabled: !!wsId && enabled,
  });

// Authorization codes must not enter either the query or mutation cache.
export function useCreateMcpConnection(wsId: string) {
  const qc = useQueryClient();
  return useCallback(
    async (data: { name: string; botId: string }) => {
      const grant = await api.createMcpConnection(wsId, data);
      void qc.invalidateQueries({ queryKey: connectionKey(wsId) });
      return grant;
    },
    [wsId, qc],
  );
}

export function useRevokeMcpConnection(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.revokeMcpConnection(wsId, id),
    onSuccess: () => qc.invalidateQueries({ queryKey: connectionKey(wsId) }),
  });
}

export type { OfficeBotRoute } from "../api/mcp-connections";
const routeKey = (wsId: string) => ["office-bot-routes", wsId] as const;
export const officeBotRoutesOptions = (wsId: string, enabled = true) =>
  queryOptions({
    queryKey: routeKey(wsId),
    queryFn: () => api.listOfficeBotRoutes(wsId),
    enabled: !!wsId && enabled,
  });
export function useSaveOfficeBotRoute(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: {
      projectId: string | null;
      botId: string;
      channelRef: string;
    }) => api.saveOfficeBotRoute(wsId, data),
    onSuccess: () => qc.invalidateQueries({ queryKey: routeKey(wsId) }),
  });
}
export function useDeleteOfficeBotRoute(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (projectId: string | null) =>
      api.deleteOfficeBotRoute(wsId, projectId),
    onSuccess: () => qc.invalidateQueries({ queryKey: routeKey(wsId) }),
  });
}
