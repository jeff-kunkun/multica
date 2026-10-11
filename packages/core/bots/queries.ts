import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { CreateExternalBotRequest } from "../types";

export const botKeys = {
  all: (wsId: string) => ["external-bots", wsId] as const,
  list: (wsId: string) => [...botKeys.all(wsId), "list"] as const,
};

export const externalBotListOptions = (wsId: string, enabled = true) =>
  queryOptions({
    queryKey: botKeys.list(wsId),
    queryFn: () => api.listExternalBots(wsId),
    enabled: !!wsId && enabled,
  });

// Every mutation waits for the server, then refetches the list: a key that
// was just issued or cut off must never be shown from a guess.
export function useCreateExternalBot(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateExternalBotRequest) => api.createExternalBot(wsId, data),
    onSettled: () => qc.invalidateQueries({ queryKey: botKeys.list(wsId) }),
  });
}

export function useRotateExternalBotKey(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (botId: string) => api.rotateExternalBotKey(wsId, botId),
    onSettled: () => qc.invalidateQueries({ queryKey: botKeys.list(wsId) }),
  });
}

export function useStopExternalBot(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (botId: string) => api.stopExternalBot(wsId, botId),
    onSettled: () => qc.invalidateQueries({ queryKey: botKeys.list(wsId) }),
  });
}

export function useDeleteExternalBot(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (botId: string) => api.deleteExternalBot(wsId, botId),
    onSettled: () => qc.invalidateQueries({ queryKey: botKeys.list(wsId) }),
  });
}
