// @vitest-environment node
import { describe, it, expect } from "vitest";
import { effectiveTaskStatus, pickStageKeys } from "./task-status-pill";

describe("pickStageKeys", () => {
  it("returns retrying while a deferred chat retry waits for backoff", () => {
    expect(pickStageKeys("deferred", [], "offline")).toEqual({ stageKey: "retrying" });
  });

  it("keeps deferred authoritative over task messages from the earlier attempt", () => {
    expect(
      effectiveTaskStatus("deferred", [
        {
          task_id: "task-1",
          issue_id: "",
          seq: 1,
          type: "thinking",
          created_at: "2026-07-01T00:00:00Z",
        },
      ]),
    ).toBe("deferred");
  });

  it("returns queued when status is queued and agent is online", () => {
    expect(pickStageKeys("queued", [], "online")).toEqual({ stageKey: "queued" });
  });

  it("returns offline when status is queued and agent is offline", () => {
    expect(pickStageKeys("queued", [], "offline")).toEqual({
      stageKey: "offline",
      static: true,
    });
  });

  it("reads an older daemon's local-directory hold as queued", () => {
    expect(pickStageKeys("waiting_local_directory", [], "online")).toEqual({
      stageKey: "queued",
    });
  });

  it("returns thinking for running with no messages", () => {
    expect(pickStageKeys("running", [], "online")).toEqual({ stageKey: "thinking" });
  });
});
