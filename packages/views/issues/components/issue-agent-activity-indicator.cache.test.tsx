import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import { configStore } from "@multica/core/config";
import { OFFICE_HIDE_WAKEUPS_FLAG } from "@multica/core/feature-flags";
import { pausedWakeupsOptions, workspaceWakeupSummariesOptions } from "@multica/core/issues/wakeups";
import type { AgentTask, IssueWakeupSummaryRow, PausedWakeup } from "@multica/core/types";
import en from "../../locales/en/issues.json";

// The other indicator tests stub useQuery. This one keeps React Query real,
// because a disabled query still hands back what another view cached: the
// issue surface polls the same wakeup summaries for every card.

vi.mock("./wakeup-condition-names", () => ({
  useConditionNames: () => ({ status: (key: string) => key, label: () => undefined, property: () => undefined, actor: (_type: string, id: string) => id }),
}));
vi.mock("../../common/use-viewing-timezone", () => ({
  useViewingTimezone: () => "UTC",
}));
vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("../../agents/components/agent-avatar-stack", () => ({
  AgentAvatarStack: ({ agentIds }: { agentIds: string[] }) => (
    <div data-testid="agent-avatar-stack">{agentIds.length}</div>
  ),
}));
vi.mock("../../i18n", () => ({
  useT: () => ({
    t: (selector: (strings: typeof en) => string, params: Record<string, unknown> = {}) =>
      selector(en).replace(/{{(\w+)}}/g, (_, key: string) => String(params[key] ?? "")),
  }),
  useLocale: () => "en",
}));

import { IssueAgentActivityIndicator } from "./issue-agent-activity-indicator";

const running: AgentTask = {
  id: "task-1",
  agent_id: "agent-1",
  runtime_id: "runtime-1",
  issue_id: "issue-1",
  status: "running",
  priority: 0,
  dispatched_at: null,
  started_at: "2026-06-08T08:00:00Z",
  completed_at: null,
  result: null,
  error: null,
  created_at: "2026-06-08T08:00:00Z",
  wakeup_id: "wake-2",
};

const summary = {
  id: "wake-1",
  issue_id: "issue-1",
  agent_id: "agent-1",
  agent_name: "Emacs",
  kind: "event",
  mode: "continuous",
  event_types: ["task.completed"],
  timezone: "UTC",
  next_fire_at: null,
  active_count: 2,
  event_count: 2,
} as unknown as IssueWakeupSummaryRow;

const paused = { issue_id: "issue-1", id: "wake-9", agent_id: "agent-1", paused_reason: "loop" } as unknown as PausedWakeup;

let qc: QueryClient;

// What the issue surface's poll leaves behind, fresh enough that nothing refetches.
function seed(tasks: AgentTask[]) {
  qc.setQueryData(agentTaskSnapshotOptions("ws-1").queryKey, tasks);
  qc.setQueryData(workspaceWakeupSummariesOptions("ws-1").queryKey, [summary]);
  qc.setQueryData(pausedWakeupsOptions("ws-1").queryKey, [paused]);
}

function renderIndicator() {
  return render(
    <QueryClientProvider client={qc}>
      <IssueAgentActivityIndicator issueId="issue-1" hoverCard={false} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
});
afterEach(() => {
  cleanup();
  qc.clear();
  configStore.getState().setLocalFeatureFlags({});
});

describe("IssueAgentActivityIndicator with a shared wakeup cache", () => {
  it("shows the cached wakeup cues by default", () => {
    seed([running]);
    renderIndicator();
    expect(screen.getByLabelText("Triggered by wakeup")).toBeInTheDocument();
    expect(screen.getByText("+2")).toBeInTheDocument();
  });

  it("keeps cached wakeups and paused rules off the card when wakeups are hidden", () => {
    configStore.getState().setLocalFeatureFlags({ [OFFICE_HIDE_WAKEUPS_FLAG]: true });
    seed([running]);
    renderIndicator();
    expect(screen.getByText("Working", { ignore: '[aria-hidden="true"] *' })).toBeInTheDocument();
    expect(screen.queryByLabelText("Triggered by wakeup")).toBeNull();
    expect(screen.queryByText("+2")).toBeNull();
    expect(screen.queryByText("Wakeup paused")).toBeNull();
  });

  it("gives an idle issue no badge from cached wakeups when wakeups are hidden", () => {
    configStore.getState().setLocalFeatureFlags({ [OFFICE_HIDE_WAKEUPS_FLAG]: true });
    seed([]);
    const { container } = renderIndicator();
    expect(container.firstChild).toBeNull();
  });
});
