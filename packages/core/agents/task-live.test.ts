import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import type { TaskLive } from "../types";
import { applyTaskLiveUpdate, removeTaskLive, taskLiveKeys } from "./task-live";

function live(taskId: string, command = "make test"): TaskLive {
  return {
    task_id: taskId,
    issue_id: "issue-1",
    agent_id: "agent-1",
    command,
    tools_in_flight: 1,
    idle_seconds: 0,
    last_activity_at: "2026-10-27T17:30:00Z",
    reported_at: "2026-10-27T17:30:00Z",
  };
}

describe("task live cache", () => {
  it("upserts a pushed view into the cached list", () => {
    const qc = new QueryClient();
    qc.setQueryData(taskLiveKeys.list("ws-1"), [live("a"), live("b")]);
    applyTaskLiveUpdate(qc, "ws-1", live("b", "make build"));
    applyTaskLiveUpdate(qc, "ws-1", live("c"));
    const cached = qc.getQueryData<TaskLive[]>(taskLiveKeys.list("ws-1"));
    expect(cached?.map((entry) => `${entry.task_id}:${entry.command}`)).toEqual(["c:make test", "b:make build", "a:make test"]);
  });

  it("starts no cache when nobody shows live status", () => {
    const qc = new QueryClient();
    applyTaskLiveUpdate(qc, "ws-1", live("a"));
    expect(qc.getQueryData(taskLiveKeys.list("ws-1"))).toBeUndefined();
  });

  it("drops a finished run", () => {
    const qc = new QueryClient();
    const list = [live("a"), live("b")];
    qc.setQueryData(taskLiveKeys.list("ws-1"), list);
    removeTaskLive(qc, "ws-1", "missing");
    expect(qc.getQueryData(taskLiveKeys.list("ws-1"))).toBe(list);
    removeTaskLive(qc, "ws-1", "a");
    expect(qc.getQueryData<TaskLive[]>(taskLiveKeys.list("ws-1"))?.map((entry) => entry.task_id)).toEqual(["b"]);
  });
});
