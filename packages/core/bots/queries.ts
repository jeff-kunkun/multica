import {
  infiniteQueryOptions,
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
import type {
  BotMentionSource,
  BotWakeEventType,
  CreateExternalBotRequest,
  UpdateBotWakeRequest,
} from "../types";

/** Server order; also the order the settings page lists them in. */
export const BOT_WAKE_EVENT_TYPES: readonly BotWakeEventType[] = [
  "mention",
  "started",
  "done",
  "blocked",
  "needs_kun",
  "task",
  "chat",
];

/** Server limit for one acknowledgement call. */
export const BOT_WAKE_ACK_MAX = 100;

export const botKeys = {
  all: (wsId: string) => ["external-bots", wsId] as const,
  list: (wsId: string) => [...botKeys.all(wsId), "list"] as const,
  wake: (wsId: string, botId: string) => [...botKeys.all(wsId), "wake", botId] as const,
  wakeEvents: (wsId: string, botId: string, pending: boolean) =>
    [...botKeys.wake(wsId, botId), pending ? "pending" : "events"] as const,
  mentions: (wsId: string, source: BotMentionSource) =>
    [
      ...botKeys.all(wsId),
      "mentions",
      source.issue_id ? `issue:${source.issue_id}` : source.chat_session_id ? `chat:${source.chat_session_id}` : "",
    ] as const,
};

export const externalBotListOptions = (wsId: string, enabled = true) =>
  queryOptions({
    queryKey: botKeys.list(wsId),
    queryFn: () => api.listExternalBots(wsId),
    enabled: !!wsId && enabled,
  });

export const botWakeOptions = (wsId: string, botId: string) =>
  queryOptions({
    queryKey: botKeys.wake(wsId, botId),
    queryFn: () => api.getBotWake(wsId, botId),
    enabled: !!wsId && !!botId,
    refetchInterval: 5_000,
  });

const WAKE_EVENT_PAGE = 20;

export const botWakeEventsOptions = (wsId: string, botId: string, pending: boolean) =>
  infiniteQueryOptions({
    queryKey: botKeys.wakeEvents(wsId, botId, pending),
    queryFn: ({ pageParam }) =>
      api.listBotWakeEvents(wsId, botId, {
        pending,
        limit: WAKE_EVENT_PAGE,
        before: pageParam || undefined,
      }),
    initialPageParam: "",
    // An empty filtered page can still carry a cursor; only "" means the end.
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled: !!wsId && !!botId,
  });

// @ completion: identity only. Kept warm so the synchronous picker can read
// it from cache on the first keystroke.
export const botMentionDirectoryOptions = (wsId: string, source: BotMentionSource = {}) =>
  queryOptions({
    queryKey: botKeys.mentions(wsId, source),
    queryFn: () => api.listBotMentionDirectory(wsId, source),
    enabled: !!wsId,
    staleTime: 60_000,
    retry: false,
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

// Setup text without a key, fetched on demand for the row's "copy setup text"
// action (DENE-1804). Nothing to invalidate: it mints nothing.
export function useExternalBotSetupText(wsId: string) {
  return useMutation({
    mutationFn: (botId?: string) => api.getExternalBotSetupText(wsId, botId),
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
    onSettled: () => qc.invalidateQueries({ queryKey: botKeys.all(wsId) }),
  });
}

export function useDeleteExternalBot(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (botId: string) => api.deleteExternalBot(wsId, botId),
    onSettled: () => qc.invalidateQueries({ queryKey: botKeys.all(wsId) }),
  });
}

// The page shows only what the server answered; no optimistic wake state.
export function useUpdateBotWake(wsId: string, botId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: UpdateBotWakeRequest) => api.updateBotWake(wsId, botId, data),
    onSuccess: (saved) => qc.setQueryData(botKeys.wake(wsId, botId), saved),
    onSettled: () => qc.invalidateQueries({ queryKey: botKeys.wake(wsId, botId) }),
  });
}

// No optimistic removal: rows leave the pending list only after the server
// accepted the batch. Both lists refetch from the first page, so an older
// event that failed late shows up again; no sequence high-water mark is kept.
export function useAckBotWakePending(wsId: string, botId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (eventIds: string[]) => api.ackBotWakePending(wsId, botId, eventIds),
    onSuccess: () =>
      Promise.all([
        qc.resetQueries({ queryKey: botKeys.wakeEvents(wsId, botId, true) }),
        qc.invalidateQueries({ queryKey: botKeys.wakeEvents(wsId, botId, false) }),
      ]),
  });
}
