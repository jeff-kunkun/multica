import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { AgentsPage } from "./agents-page";

const mocks = vi.hoisted(() => ({
  agents: [] as Agent[],
  runtimes: [] as Partial<AgentRuntime>[],
  presence: new Map<string, unknown>(),
  tasks: [] as { agent_id: string; issue_id: string; status: string }[],
  push: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: readonly unknown[] }) => {
    const key = options.queryKey?.[0];
    if (key === "agents") {
      return {
        data: mocks.agents,
        isLoading: false,
        error: null,
        refetch: vi.fn(),
      };
    }
    if (key === "runtimes") return { data: mocks.runtimes };
    if (key === "tasks") return { data: mocks.tasks };
    if (key === "issue") {
      return {
        data: { identifier: "DENE-7", title: `Issue ${options.queryKey?.[1]}` },
      };
    }
    return { data: [] };
  },
}));

vi.mock("@multica/core/agents", () => ({
  agentTaskSnapshotOptions: () => ({ queryKey: ["tasks"] }),
  isAgentRuntimeBound: (agent: { runtime_id: string }) =>
    agent.runtime_id.length > 0,
  useWorkspaceActivityMap: () => ({ byAgent: new Map() }),
  useWorkspacePresenceMap: () => ({ byAgent: mocks.presence }),
}));

vi.mock("@multica/core/issues", () => ({
  issueDetailOptions: (_wsId: string, id: string) => ({
    queryKey: ["issue", id],
  }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    newAgent: () => "/ws/agents/new",
    agentDetail: (id: string) => `/ws/agents/${id}`,
    issueDetail: (id: string) => `/ws/issues/${id}`,
  }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({ queryKey: ["agents"] }),
}));

vi.mock("@multica/core/runtimes", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/runtimes")>()),
  runtimeListOptions: () => ({ queryKey: ["runtimes"] }),
}));

const BASE_AGENT: Agent = {
  id: "agent-base",
  workspace_id: "workspace-1",
  runtime_id: "runtime-routed",
  name: "Base Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "private",
  invocation_targets: [],
  status: "idle",
  max_concurrent_tasks: 1,
  owner_id: "user-1",
  created_at: "2026-06-01T00:00:00Z",
  updated_at: "2026-06-01T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function makeAgent(over: Partial<Agent>): Agent {
  return { ...BASE_AGENT, ...over };
}

function makeAdapter(): NavigationAdapter {
  return {
    push: mocks.push,
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/ws/agents",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (p) => p,
  };
}

function renderPage() {
  renderWithI18n(
    <NavigationProvider value={makeAdapter()}>
      <AgentsPage />
    </NavigationProvider>,
  );
}

function row(name: string): HTMLElement {
  return screen.getByRole("link", { name }).closest("[data-agent-row]") as HTMLElement;
}

beforeEach(() => {
  mocks.agents = [];
  mocks.runtimes = [
    {
      id: "runtime-routed",
      name: "Claude (studio)",
      provider: "claude",
      metadata: {
        magpie: {
          group: "office-claude",
          models: [
            { id: "claude/claude-opus-5-5", name: "Opus 5.5" },
            { id: "claude/claude-sonnet-5-5", name: "Sonnet 5.5" },
          ],
        },
      },
    },
    {
      id: "runtime-unrouted",
      name: "Codex (studio)",
      provider: "codex",
      metadata: { magpie: { group: "", models: [] } },
    },
    {
      id: "runtime-plain",
      name: "Gemini (studio)",
      provider: "gemini",
      metadata: {},
    },
  ];
  mocks.presence = new Map();
  mocks.tasks = [];
  mocks.push.mockReset();
});

describe("AgentsPage", () => {
  it("shows each agent's magpie route, or says it is not routed", () => {
    mocks.agents = [
      makeAgent({ id: "a-routed", name: "Routed" }),
      makeAgent({ id: "a-unrouted", name: "Unrouted", runtime_id: "runtime-unrouted" }),
      makeAgent({ id: "a-plain", name: "Plain", runtime_id: "runtime-plain" }),
    ];
    renderPage();

    const routed = row("Routed");
    expect(within(routed).getByText("office-claude")).toBeInTheDocument();
    expect(within(routed).getByText("Opus 5.5")).toBeInTheDocument();
    expect(within(routed).getByText("Sonnet 5.5")).toBeInTheDocument();

    const unrouted = row("Unrouted");
    expect(within(unrouted).getByText("Not on magpie")).toBeInTheDocument();
    expect(within(unrouted).getByText("CLI's own models")).toBeInTheDocument();

    // No magpie on that machine: no route line at all.
    const plain = row("Plain");
    expect(within(plain).queryByText("Not on magpie")).not.toBeInTheDocument();
    expect(within(plain).queryByText("CLI's own models")).not.toBeInTheDocument();
  });

  // Deleting archives an agent; the office never shows archived ones.
  it("hides deleted agents, with nothing to switch or edit", () => {
    mocks.agents = [
      makeAgent({ id: "a-old", name: "Old", archived_at: "2026-07-01T00:00:00Z" }),
      makeAgent({ id: "a-new", name: "New" }),
    ];
    renderPage();

    const rows = document.querySelectorAll("[data-agent-row]");
    expect([...rows].map((el) => el.textContent?.slice(0, 3))).toEqual(["New"]);
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Edit" })).not.toBeInTheDocument();
  });

  it("opens the agent when the row is clicked", () => {
    mocks.agents = [makeAgent({ id: "a-one", name: "One" })];
    renderPage();

    fireEvent.click(within(row("One")).getByText("No activity (30d)", { exact: false }));
    expect(mocks.push).toHaveBeenCalledWith("/ws/agents/a-one");
  });

  it("shows the issue a working agent is running, linked", () => {
    mocks.agents = [
      makeAgent({ id: "a-busy", name: "Busy" }),
      makeAgent({ id: "a-idle", name: "Idle" }),
    ];
    mocks.tasks = [
      { agent_id: "a-busy", issue_id: "issue-1", status: "running" },
      { agent_id: "a-idle", issue_id: "issue-2", status: "completed" },
    ];
    renderPage();

    const link = within(row("Busy")).getByRole("link", { name: /Issue issue-1/ });
    expect(link).toHaveAttribute("href", "/ws/issues/issue-1");
    expect(within(row("Busy")).getByText("Working on")).toBeInTheDocument();
    expect(within(row("Idle")).queryByText("Working on")).not.toBeInTheDocument();
  });

  it("shows the empty state when there are no agents", () => {
    renderPage();
    expect(screen.getByText("No agents yet")).toBeInTheDocument();
  });
});
