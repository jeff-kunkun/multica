import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { AgentsPage } from "./agents-page";

const mocks = vi.hoisted(() => ({
  agents: [] as Agent[],
  runtimes: [] as Partial<AgentRuntime>[],
  presence: new Map<string, unknown>(),
  archiveAgent: vi.fn(),
  restoreAgent: vi.fn(),
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
    return { data: [] };
  },
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@multica/core/agents", () => ({
  isAgentRuntimeBound: (agent: { runtime_id: string }) =>
    agent.runtime_id.length > 0,
  useWorkspaceActivityMap: () => ({ byAgent: new Map() }),
  useWorkspacePresenceMap: () => ({ byAgent: mocks.presence }),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    archiveAgent: mocks.archiveAgent,
    restoreAgent: mocks.restoreAgent,
  },
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: unknown) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    newAgent: () => "/ws/agents/new",
    agentDetail: (id: string) => `/ws/agents/${id}`,
  }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({ queryKey: ["agents"] }),
  memberListOptions: () => ({ queryKey: ["members"] }),
  workspaceKeys: { agents: (wsId: string) => ["agents", wsId] },
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
  mocks.archiveAgent.mockReset().mockResolvedValue(undefined);
  mocks.restoreAgent.mockReset().mockResolvedValue(undefined);
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

  it("lists archived agents last and counts only active ones", () => {
    mocks.agents = [
      makeAgent({ id: "a-old", name: "Old", archived_at: "2026-07-01T00:00:00Z" }),
      makeAgent({ id: "a-new", name: "New" }),
    ];
    renderPage();

    const rows = document.querySelectorAll("[data-agent-row]");
    expect([...rows].map((el) => el.textContent?.slice(0, 3))).toEqual(["New", "Old"]);
    expect(within(row("Old")).getByRole("switch")).not.toBeChecked();
    expect(within(row("Old")).queryByText("office-claude")).not.toBeInTheDocument();
  });

  it("opens the agent when the row is clicked, but not when the switch is", () => {
    mocks.agents = [makeAgent({ id: "a-one", name: "One" })];
    renderPage();

    fireEvent.click(within(row("One")).getByRole("switch"));
    expect(mocks.push).not.toHaveBeenCalled();

    fireEvent.click(within(row("One")).getByText("No activity (30d)", { exact: false }));
    expect(mocks.push).toHaveBeenCalledWith("/ws/agents/a-one");
  });

  it("archives an idle agent straight away", async () => {
    mocks.agents = [makeAgent({ id: "a-idle", name: "Idle" })];
    renderPage();

    fireEvent.click(within(row("Idle")).getByRole("switch"));
    await waitFor(() => expect(mocks.archiveAgent).toHaveBeenCalledWith("a-idle"));
  });

  it("asks before archiving an agent with active tasks", async () => {
    mocks.agents = [makeAgent({ id: "a-busy", name: "Busy" })];
    mocks.presence = new Map([
      ["a-busy", { availability: "online", runningCount: 1, queuedCount: 1 }],
    ]);
    renderPage();

    fireEvent.click(within(row("Busy")).getByRole("switch"));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/2 active tasks will be cancelled/)).toBeInTheDocument();
    expect(mocks.archiveAgent).not.toHaveBeenCalled();

    fireEvent.click(within(dialog).getByRole("button", { name: "Archive" }));
    await waitFor(() => expect(mocks.archiveAgent).toHaveBeenCalledWith("a-busy"));
    expect(mocks.push).not.toHaveBeenCalled();
  });

  it("restores an archived agent", async () => {
    mocks.agents = [
      makeAgent({ id: "a-back", name: "Back", archived_at: "2026-07-01T00:00:00Z" }),
    ];
    renderPage();

    fireEvent.click(within(row("Back")).getByRole("switch"));
    await waitFor(() => expect(mocks.restoreAgent).toHaveBeenCalledWith("a-back"));
  });

  it("does not let a non-owner flip someone else's agent", () => {
    mocks.agents = [makeAgent({ id: "a-theirs", name: "Theirs", owner_id: "user-2" })];
    renderPage();

    expect(within(row("Theirs")).getByRole("switch")).toHaveAttribute("aria-disabled", "true");
  });

  it("shows the empty state when there are no agents", () => {
    renderPage();
    expect(screen.getByText("No agents yet")).toBeInTheDocument();
  });
});
