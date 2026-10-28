import { cleanup, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { taskLiveKeys } from "@multica/core/agents";
import { configStore } from "@multica/core/config";
import { OFFICE_LIVE_STATUS_FLAG } from "@multica/core/feature-flags";
import type { AgentTask, TaskLive } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";

const mockState = vi.hoisted(() => ({ listTaskLive: vi.fn() }));

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return { ...actual, api: { ...actual.api, listTaskLive: mockState.listTaskLive } };
});
vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useCurrentWorkspace: () => ({ id: "ws-1" }),
}));
vi.mock("./actor-avatar", () => ({ ActorAvatar: () => <span data-testid="actor-avatar" /> }));
vi.mock("./task-transcript", () => ({ TranscriptButton: () => null }));
vi.mock("../issues/components/terminate-task-confirm-dialog", () => ({ TerminateTaskConfirmDialog: () => null }));
vi.mock("@multica/core/workspace/hooks", () => ({ useActorName: () => ({ getActorName: () => "Lambda" }) }));

import { TaskLiveDetails, TaskLiveView } from "./task-live-line";
import { ActiveTaskRow } from "../issues/components/execution-log-section";

function makeLive(overrides: Partial<TaskLive> = {}): TaskLive {
  return {
    task_id: "task-1",
    issue_id: "issue-1",
    agent_id: "agent-1",
    tool: "Bash",
    command: "go test ./internal/... -count=1",
    last_output: "ok pkg/a",
    tools_in_flight: 1,
    idle_seconds: 90,
    last_activity_at: "2026-10-27T17:28:30Z",
    reported_at: "2026-10-27T17:30:00Z",
    ...overrides,
  };
}

function makeTask(overrides: Partial<AgentTask> = {}): AgentTask {
  return {
    id: "task-1",
    agent_id: "agent-1",
    runtime_id: "runtime-1",
    issue_id: "issue-1",
    status: "running",
    priority: 0,
    dispatched_at: null,
    started_at: "2026-10-27T17:00:00Z",
    completed_at: null,
    result: null,
    error: null,
    created_at: "2026-10-27T17:00:00Z",
    trigger_summary: "Started from comment",
    ...overrides,
  };
}

function renderWithQuery(ui: ReactElement, seed?: TaskLive[]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  if (seed) qc.setQueryData(taskLiveKeys.list("ws-1"), seed);
  return renderWithI18n(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

beforeEach(() => {
  cleanup();
  vi.clearAllMocks();
  mockState.listTaskLive.mockResolvedValue([]);
});

afterEach(() => {
  configStore.getState().setLocalFeatureFlags({});
});

describe("TaskLiveView", () => {
  it("shows the quiet time, the command in flight and the last output line", () => {
    renderWithI18n(<TaskLiveView live={makeLive()} />);
    expect(screen.getByText("Quiet 1m 30s")).toBeInTheDocument();
    expect(screen.getByText("Running: go test ./internal/... -count=1")).toBeInTheDocument();
    expect(screen.getByText("Last: ok pkg/a")).toBeInTheDocument();
  });

  it("says when no command is running", () => {
    renderWithI18n(<TaskLiveView live={makeLive({ tool: undefined, command: undefined, tools_in_flight: 0, last_output: undefined })} />);
    expect(screen.getByText("No command running")).toBeInTheDocument();
    expect(screen.queryByText(/^Last:/)).not.toBeInTheDocument();
  });

  it("localizes", () => {
    renderWithI18n(<TaskLiveView live={makeLive({ idle_seconds: 5 })} />, { locale: "zh-Hans" });
    expect(screen.getByText("已安静 5s")).toBeInTheDocument();
    expect(screen.getByText("正在跑：go test ./internal/... -count=1")).toBeInTheDocument();
  });
});

describe("TaskLiveDetails", () => {
  function row(label: string) {
    return screen.getByText(label, { selector: "dt" }).nextElementSibling;
  }

  it("lists the command in flight, the last output line and the quiet time under labels", () => {
    renderWithI18n(<TaskLiveDetails live={makeLive()} />);
    expect(row("Command")).toHaveTextContent("go test ./internal/... -count=1");
    expect(row("Output")).toHaveTextContent("ok pkg/a");
    expect(row("Quiet")).toHaveTextContent("1m 30s");
  });

  it("falls back to the tool, then says whether anything is running", () => {
    const { unmount } = renderWithI18n(<TaskLiveDetails live={makeLive({ command: undefined, tool: "WebFetch" })} />);
    expect(row("Command")).toHaveTextContent("WebFetch");
    unmount();

    const second = renderWithI18n(<TaskLiveDetails live={makeLive({ command: undefined, tool: undefined })} />);
    expect(row("Command")).toHaveTextContent("A tool is running");
    second.unmount();

    renderWithI18n(<TaskLiveDetails live={makeLive({ command: undefined, tool: undefined, tools_in_flight: 0, last_output: undefined, idle_seconds: 0 })} />);
    expect(row("Command")).toHaveTextContent("No command running");
    expect(row("Output")).toHaveTextContent("No output yet");
    expect(row("Quiet")).toHaveTextContent("under a second");
  });

  it("keeps long lines whole for the hover title", () => {
    const long = `bash -c '${"x".repeat(280)}'`;
    renderWithI18n(<TaskLiveDetails live={makeLive({ command: long })} />);
    expect(screen.getByTitle(long)).toHaveClass("sm:pointer-fine:line-clamp-2");
  });

  it("never clamps on narrow or touch screens, where there is no hover title", () => {
    const command = `bash -c '${"x".repeat(280)}'`;
    const output = "y".repeat(200);
    renderWithI18n(<TaskLiveDetails live={makeLive({ command, last_output: output })} />);
    for (const el of [screen.getByTitle(command), screen.getByTitle(output)]) {
      // Only the wide, mouse-driven variant may clamp; the base style shows all of it.
      expect(el.className.split(" ").filter((c) => c.includes("line-clamp"))).toEqual(["sm:pointer-fine:line-clamp-2"]);
      expect(el.className).not.toMatch(/(^|\s)(truncate|line-clamp-\d)(\s|$)/);
    }
    expect(row("Command")).toHaveTextContent(command);
    expect(row("Output")).toHaveTextContent(output);
  });

  it("localizes", () => {
    renderWithI18n(<TaskLiveDetails live={makeLive({ idle_seconds: 5 })} />, { locale: "zh-Hans" });
    expect(row("当前命令")).toHaveTextContent("go test ./internal/... -count=1");
    expect(row("最后输出")).toHaveTextContent("ok pkg/a");
    expect(row("已安静")).toHaveTextContent("5s");
  });
});

describe("ActiveTaskRow live status", () => {
  it("shows the run's live view only with the office flag, reading nothing without it", () => {
    const { unmount } = renderWithQuery(<ActiveTaskRow task={makeTask()} issueId="issue-1" />, [makeLive()]);
    expect(screen.queryByTestId("task-live-line")).not.toBeInTheDocument();
    expect(mockState.listTaskLive).not.toHaveBeenCalled();
    unmount();

    configStore.getState().setLocalFeatureFlags({ [OFFICE_LIVE_STATUS_FLAG]: true });
    renderWithQuery(<ActiveTaskRow task={makeTask()} issueId="issue-1" />, [makeLive({ task_id: "other" }), makeLive()]);
    expect(screen.getByTestId("task-live-line")).toBeInTheDocument();
    expect(screen.getByText("Running: go test ./internal/... -count=1")).toBeInTheDocument();
  });

  it("lays the live status out as labelled rows for the header card", () => {
    configStore.getState().setLocalFeatureFlags({ [OFFICE_LIVE_STATUS_FLAG]: true });
    renderWithQuery(<ActiveTaskRow task={makeTask()} issueId="issue-1" liveDetails />, [makeLive()]);
    expect(screen.getByTestId("task-live-details")).toHaveTextContent("go test ./internal/... -count=1");
    expect(screen.queryByTestId("task-live-line")).not.toBeInTheDocument();
  });

  it("shows nothing for a run that is not running or has no report", () => {
    configStore.getState().setLocalFeatureFlags({ [OFFICE_LIVE_STATUS_FLAG]: true });
    const { unmount } = renderWithQuery(<ActiveTaskRow task={makeTask({ status: "queued" })} issueId="issue-1" />, [makeLive()]);
    expect(screen.queryByTestId("task-live-line")).not.toBeInTheDocument();
    unmount();
    renderWithQuery(<ActiveTaskRow task={makeTask()} issueId="issue-1" />, []);
    expect(screen.queryByTestId("task-live-line")).not.toBeInTheDocument();
  });
});
