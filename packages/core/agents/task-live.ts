"use client";

import { queryOptions, useQuery, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { TaskLive } from "../types";

// Live status of the workspace's running issue runs (DENE-2667 PR 4). One read per
// workspace; task_live:updated pushes patch the cached list in place, so the
// 15-second reports never trigger a refetch. The stale time is the
// reconnect / missed-push safety net.
export const taskLiveKeys = {
  all: (wsId: string) => ["workspaces", wsId, "task-live"] as const,
  list: (wsId: string) => [...taskLiveKeys.all(wsId), "list"] as const,
};

export function taskLiveOptions(wsId: string) {
  return queryOptions({
    queryKey: taskLiveKeys.list(wsId),
    queryFn: () => api.listTaskLive(),
    staleTime: 30 * 1000,
    gcTime: 5 * 60 * 1000,
    // A server without live status answers 404; do not retry it.
    retry: false,
  });
}

/** The live view of one run, or undefined when there is none or `enabled` is false. */
export function useTaskLive(wsId: string | undefined, taskId: string, enabled: boolean): TaskLive | undefined {
  const { data } = useQuery({ ...taskLiveOptions(wsId ?? ""), enabled: enabled && !!wsId });
  return data?.find((live) => live.task_id === taskId);
}

/** Upserts a pushed live view. Without a cached list (nobody is showing live status) it does nothing. */
export function applyTaskLiveUpdate(qc: QueryClient, wsId: string, live: TaskLive) {
  if (!live?.task_id) return;
  qc.setQueryData<TaskLive[]>(taskLiveKeys.list(wsId), (old) => {
    if (!old) return old;
    return [live, ...old.filter((entry) => entry.task_id !== live.task_id)];
  });
}

/** Drops a finished run's live view. */
export function removeTaskLive(qc: QueryClient, wsId: string, taskId: string) {
  if (!taskId) return;
  qc.setQueryData<TaskLive[]>(taskLiveKeys.list(wsId), (old) =>
    old?.some((entry) => entry.task_id === taskId) ? old.filter((entry) => entry.task_id !== taskId) : old,
  );
}
