import { queryOptions, useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { api } from "../api";
import { agentTaskSnapshotOptions } from "../agents/queries";
import { buildInboxBoard, type InboxBoard } from "./board";

export const homeKeys = {
  all: (wsId: string) => ["workspaces", wsId, "home"] as const,
  parking: (wsId: string) => [...homeKeys.all(wsId), "parking"] as const,
  summons: (wsId: string) => [...homeKeys.all(wsId), "summons"] as const,
  doneToday: (wsId: string, dayStart: string) =>
    [...homeKeys.all(wsId), "done-today", dayStart] as const,
  issuesByIds: (wsId: string, ids: readonly string[]) =>
    [...homeKeys.all(wsId), "issues", ids.join(",")] as const,
};

// Server verdicts change at a run's end and on replies; WS events invalidate
// `homeKeys.all` right away (use-realtime-sync). The stale time is only the
// reconnect / missed-event safety net.
const HOME_STALE_TIME = 30 * 1000;

export function parkingRecordsOptions(wsId: string) {
  return queryOptions({
    queryKey: homeKeys.parking(wsId),
    queryFn: () => api.listIssueParkingRecords({ limit: 500 }),
    select: (data) => data.records ?? [],
    staleTime: HOME_STALE_TIME,
    refetchOnWindowFocus: true,
  });
}

export function waitingSummonsOptions(wsId: string) {
  return queryOptions({
    queryKey: homeKeys.summons(wsId),
    queryFn: () => api.listWaitingSummons(),
    staleTime: HOME_STALE_TIME,
    refetchOnWindowFocus: true,
  });
}

/** Local midnight of `now`, and of the next day, as ISO strings. */
export function localDayWindow(now: Date): { start: string; end: string } {
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const end = new Date(start);
  end.setDate(end.getDate() + 1);
  return { start: start.toISOString(), end: end.toISOString() };
}

export function doneTodayOptions(wsId: string, day: { start: string; end: string }) {
  return queryOptions({
    queryKey: homeKeys.doneToday(wsId, day.start),
    queryFn: () =>
      api.listIssues({
        status: "done",
        date_field: "updated_at",
        date_start: day.start,
        date_end: day.end,
        sort_by: "updated_at",
        sort_direction: "desc",
        limit: 100,
      }),
    select: (data) => data.issues,
    staleTime: HOME_STALE_TIME,
    refetchOnWindowFocus: true,
  });
}

export function issuesByIdsOptions(wsId: string, ids: readonly string[]) {
  return queryOptions({
    queryKey: homeKeys.issuesByIds(wsId, ids),
    queryFn: () => api.listIssues({ ids: [...ids], limit: ids.length }),
    select: (data) => data.issues,
    staleTime: HOME_STALE_TIME,
  });
}

export interface InboxBoardResult {
  board: InboxBoard;
  isLoading: boolean;
  isError: boolean;
}

const EMPTY: never[] = [];

/**
 * The four lanes of the inbox, assembled from the summon list, the parking
 * records, the workspace task snapshot and today's finished issues.
 */
export function useInboxBoard(
  wsId: string,
  userId: string | null,
  now: Date = new Date(),
): InboxBoardResult {
  const dayStart = localDayWindow(now).start;
  const day = useMemo(() => localDayWindow(new Date(dayStart)), [dayStart]);

  const summons = useQuery(waitingSummonsOptions(wsId));
  const parking = useQuery(parkingRecordsOptions(wsId));
  const tasks = useQuery(agentTaskSnapshotOptions(wsId));
  const done = useQuery(doneTodayOptions(wsId, day));

  const runningIds = useMemo(() => {
    const ids = new Set<string>();
    for (const t of tasks.data ?? EMPTY) {
      if (t.issue_id && (t.status === "running" || t.status === "dispatched")) ids.add(t.issue_id);
    }
    return [...ids].sort();
  }, [tasks.data]);
  const runningIssues = useQuery({
    ...issuesByIdsOptions(wsId, runningIds),
    enabled: runningIds.length > 0,
  });

  const board = useMemo(
    () =>
      buildInboxBoard({
        userId,
        summons: summons.data ?? EMPTY,
        parking: parking.data ?? EMPTY,
        tasks: tasks.data ?? EMPTY,
        runningIssues: runningIds.length > 0 ? (runningIssues.data ?? EMPTY) : EMPTY,
        doneIssues: done.data ?? EMPTY,
      }),
    [userId, summons.data, parking.data, tasks.data, runningIds.length, runningIssues.data, done.data],
  );

  return {
    board,
    isLoading: summons.isLoading || parking.isLoading || tasks.isLoading || done.isLoading,
    isError: summons.isError && parking.isError,
  };
}
