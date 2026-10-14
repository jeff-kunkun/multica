// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { McpConfigTab } from "./mcp-config-tab";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const mockAddServer = vi.hoisted(() => vi.fn());
const mockSetEnabled = vi.hoisted(() => vi.fn());
const mockRemoveServer = vi.hoisted(() => vi.fn());

const workspaceMcp = vi.hoisted(() => ({
  // Servers ASSIGNED to this agent, and the workspace library to pick from.
  assigned: [] as Array<Record<string, unknown>>,
  library: [] as Array<Record<string, unknown>>,
}));

// The workspace section reads the agent's assignments plus the library.
// Stubbing the query options keeps this a pure render test — the real ones
// would hit fetch.
vi.mock("@multica/core/workspace/queries", () => ({
  agentMcpServersOptions: (agentId: string) => ({
    queryKey: ["agents", agentId, "mcp-servers"],
    queryFn: () => Promise.resolve(workspaceMcp.assigned),
    enabled: agentId !== "",
  }),
  workspaceMcpServersOptions: (wsId: string) => ({
    queryKey: ["workspaces", wsId, "mcp-servers"],
    queryFn: () => Promise.resolve(workspaceMcp.library),
    enabled: wsId !== "",
  }),
}));

vi.mock("@multica/core/workspace/mutations", () => ({
  useAddAgentMcpServer: () => ({ mutateAsync: mockAddServer, isPending: false }),
  useSetAgentMcpServerEnabled: () => ({ mutateAsync: mockSetEnabled, isPending: false }),
  useRemoveAgentMcpServer: () => ({ mutateAsync: mockRemoveServer, isPending: false }),
}));

const wsServer = (over: Record<string, unknown>) => ({
  id: "srv-1",
  workspace_id: "ws-1",
  name: "shared-linear",
  transport: "http",
  created_at: "2026-08-14T00:00:00Z",
  updated_at: "2026-08-14T00:00:00Z",
  ...over,
});

vi.mock("sonner", () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  owner_id: "user-1",
  created_at: "2026-05-28T00:00:00Z",
  updated_at: "2026-05-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function TestShell({ children }: { children: React.ReactNode }) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nProvider>
  );
}

function renderTab(
  overrides: Partial<Agent> = {},
  onSave = vi.fn().mockResolvedValue(undefined),
) {
  const result = render(
    <TestShell>
      <McpConfigTab
        agent={{ ...baseAgent, ...overrides }}
        onSave={onSave}
      />
    </TestShell>,
  );
  return { ...result, onSave };
}

describe("McpConfigTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    workspaceMcp.assigned = [];
    workspaceMcp.library = [];
    mockAddServer.mockResolvedValue([]);
    mockSetEnabled.mockResolvedValue([]);
    mockRemoveServer.mockResolvedValue([]);
  });

  it("renders redacted managed MCP without exposing add or edit controls", () => {
    renderTab({ mcp_config: null, mcp_config_redacted: true });

    expect(screen.getByText(/hidden from your view/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /add mcp/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("projects historical aggregate config into individually managed rows", () => {
    renderTab({
      mcp_config: {
        version: 1,
        mcpServers: {
          fetch: { command: "uvx", args: ["mcp-server-fetch"] },
          docs: { type: "http", url: "https://example.test/mcp", enabled: false },
        },
      },
    });

    expect(screen.getByText("fetch")).toBeInTheDocument();
    expect(screen.getByText("docs")).toBeInTheDocument();
    expect(
      within(screen.getByText("docs").closest("li")!).getByText("Off for agent"),
    ).toBeVisible();
    expect(screen.getByLabelText("STDIO")).toBeInTheDocument();
    expect(screen.getByLabelText("Streamable HTTP")).toBeInTheDocument();
    expect(screen.getByText("STDIO")).toBeVisible();
    expect(screen.getByText("Streamable HTTP")).toBeVisible();
    expect(screen.getByRole("heading", { name: /managed by multica/i })).toBeInTheDocument();
    expect(screen.queryByLabelText(/MCP config JSON editor/i)).not.toBeInTheDocument();
  });

  it("renames a managed server inline without changing its configuration", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab({
      mcp_config: {
        version: 1,
        mcpServers: {
          fetch: { command: "uvx", args: ["mcp-server-fetch"] },
        },
      },
    });

    await user.click(
      screen.getByRole("button", { name: /rename server fetch/i }),
    );
    const input = screen.getByRole("textbox", { name: "Rename server" });
    await user.clear(input);
    await user.type(input, "fetch-v2");
    await user.click(screen.getByRole("button", { name: "Save name" }));

    expect(onSave).toHaveBeenCalledWith({
      mcp_config: {
        version: 1,
        mcpServers: {
          "fetch-v2": { command: "uvx", args: ["mcp-server-fetch"] },
        },
      },
    });
  });

  it("preserves a refreshed server config when an inline rename is submitted", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(undefined);
    const initialConfig = {
      version: 1,
      mcpServers: {
        fetch: { command: "uvx", args: ["old"] },
      },
    };
    const view = renderTab({ mcp_config: initialConfig }, onSave);

    await user.click(
      screen.getByRole("button", { name: /rename server fetch/i }),
    );
    const input = screen.getByRole("textbox", { name: "Rename server" });
    await user.clear(input);
    await user.type(input, "fetch-v2");

    view.rerender(
      <TestShell>
        <McpConfigTab
          agent={{
            ...baseAgent,
            mcp_config: {
              version: 1,
              mcpServers: {
                fetch: { command: "uvx", args: ["new"] },
              },
            },
          }}
          onSave={onSave}
        />
      </TestShell>,
    );
    await user.click(screen.getByRole("button", { name: "Save name" }));

    expect(onSave).toHaveBeenCalledWith({
      mcp_config: {
        version: 1,
        mcpServers: {
          "fetch-v2": { command: "uvx", args: ["new"] },
        },
      },
    });
  });

  it("rejects a managed rename when refreshed data removed the original server", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(undefined);
    const otherContainer = { fetch: { command: "other-mcp-server" } };
    const view = renderTab({
      mcp_config: {
        mcpServers: { fetch: { command: "uvx" } },
        mcp: otherContainer,
      },
    }, onSave);

    await user.click(screen.getByRole("button", { name: /rename server fetch/i }));
    const input = screen.getByRole("textbox", { name: "Rename server" });
    await user.clear(input);
    await user.type(input, "fetch-v2");

    // Removing the original entry reveals the same name in another supported
    // container. The draft must not rename this different server or recreate
    // the deleted entry from its stale snapshot.
    view.rerender(
      <TestShell>
        <McpConfigTab
          agent={{ ...baseAgent, mcp_config: { mcp: otherContainer } }}
          onSave={onSave}
        />
      </TestShell>,
    );
    await user.click(screen.getByRole("button", { name: "Save name" }));

    expect(screen.getByRole("alert")).toHaveTextContent("Could not rename the MCP server");
    expect(onSave).not.toHaveBeenCalled();
    expect(input).toHaveValue("fetch-v2");
    expect(input).not.toBeDisabled();
  });

  it("validates every managed rename branch and cancels a no-op", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab({
      mcp_config: {
        version: 1,
        mcpServers: {
          fetch: { command: "uvx" },
          docs: { url: "https://example.test/mcp" },
        },
      },
    });

    await user.click(
      screen.getByRole("button", { name: /rename server fetch/i }),
    );
    const input = screen.getByRole("textbox", { name: "Rename server" });

    await user.clear(input);
    await user.click(screen.getByRole("button", { name: "Save name" }));
    expect(screen.getByText("Enter a server name.")).toBeInTheDocument();

    await user.type(input, "bad name");
    await user.click(screen.getByRole("button", { name: "Save name" }));
    expect(
      screen.getByText("Use only letters, numbers, hyphens, and underscores."),
    ).toBeInTheDocument();

    await user.clear(input);
    await user.type(input, "docs");
    await user.click(screen.getByRole("button", { name: "Save name" }));
    expect(
      screen.getByText("A server with this name already exists."),
    ).toBeInTheDocument();
    expect(onSave).not.toHaveBeenCalled();

    await user.clear(input);
    await user.type(input, "fetch");
    await user.click(screen.getByRole("button", { name: "Save name" }));
    expect(screen.queryByRole("textbox", { name: "Rename server" })).toBeNull();
    expect(onSave).not.toHaveBeenCalled();
  });

  it.each([
    { reason: "an API error", error: new Error("Rename failed"), message: "Rename failed" },
    { reason: "an empty error", error: new Error(""), message: "Could not rename the MCP server" },
    { reason: "a non-Error rejection", error: null, message: "Could not rename the MCP server" },
  ])("keeps a managed server rename open after $reason", async ({ error, message }) => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockRejectedValueOnce(error);
    renderTab(
      {
        mcp_config: {
          version: 1,
          mcpServers: {
            fetch: { command: "uvx", args: ["mcp-server-fetch"] },
          },
        },
      },
      onSave,
    );

    await user.click(
      screen.getByRole("button", { name: /rename server fetch/i }),
    );
    const input = screen.getByRole("textbox", { name: "Rename server" });
    await user.clear(input);
    await user.type(input, "fetch-v2");
    await user.click(screen.getByRole("button", { name: "Save name" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(input).toHaveValue("fetch-v2");
    expect(input).not.toBeDisabled();
  });

  it("blocks competing managed actions while an inline rename is pending", async () => {
    const user = userEvent.setup();
    let finishRename!: () => void;
    const onSave = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finishRename = resolve;
        }),
    );
    renderTab(
      {
        mcp_config: {
          version: 1,
          mcpServers: {
            fetch: { command: "uvx" },
            docs: { url: "https://example.test/mcp" },
          },
        },
      },
      onSave,
    );

    await user.click(
      screen.getByRole("button", { name: /rename server fetch/i }),
    );
    const input = screen.getByRole("textbox", { name: "Rename server" });
    await user.clear(input);
    await user.type(input, "fetch-v2");
    await user.click(screen.getByRole("button", { name: "Save name" }));

    await waitFor(() => expect(onSave).toHaveBeenCalledOnce());
    expect(input).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save name" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel rename" })).toBeDisabled();
    expect(screen.getByRole("button", { name: /Add MCP/i })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: /rename server docs/i }),
    ).toBeDisabled();

    finishRename();
    await waitFor(() =>
      expect(screen.queryByRole("textbox", { name: "Rename server" })).toBeNull(),
    );
  });

  it("adds one stdio server through the form and preserves historical top-level data", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab({ mcp_config: { version: 1 } });

    await user.click(screen.getByRole("button", { name: /add mcp/i }));
    await user.type(screen.getByLabelText("Server name"), "fetch");
    await user.type(screen.getByLabelText("Command"), "uvx");
    await user.click(screen.getByRole("button", { name: "Add" }));

    expect(onSave).toHaveBeenCalledWith({
      mcp_config: {
        version: 1,
        mcpServers: { fetch: { command: "uvx" } },
      },
    });
  });

  it("opens a new agent MCP dialog without displaying validation errors", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(screen.getByRole("button", { name: /add mcp/i }));

    expect(screen.getByLabelText("Server name")).not.toHaveAttribute("aria-invalid");
    expect(screen.getByLabelText("Command")).not.toHaveAttribute("aria-invalid");
    expect(screen.queryByText("Enter a server name.")).toBeNull();
    expect(screen.queryByText(/Enter the command used/)).toBeNull();
  });

  it("adds one HTTP server through JSON mode", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab();

    await user.click(screen.getByRole("button", { name: /add mcp/i }));
    await user.type(screen.getByLabelText("Server name"), "weather");
    await user.click(screen.getByRole("tab", { name: "JSON" }));
    fireEvent.change(screen.getByLabelText(/MCP server JSON configuration/i), {
      target: {
        value: JSON.stringify({
          type: "http",
          url: "https://example.invalid/mcp",
        }),
      },
    });
    await user.click(screen.getByRole("button", { name: "Add" }));

    expect(onSave).toHaveBeenCalledWith({
      mcp_config: {
        mcpServers: {
          weather: {
            type: "http",
            url: "https://example.invalid/mcp",
          },
        },
      },
    });
  });

  it("edits one historical server without replacing its siblings", async () => {
    const user = userEvent.setup();
    const existing = {
      version: 1,
      mcpServers: {
        fetch: {
          command: "uvx",
          timeout: 30,
          tools: { include: ["fetch_url"] },
        },
        docs: { url: "https://example.test/mcp" },
      },
    };
    const { onSave } = renderTab({ mcp_config: existing });

    await user.click(
      screen.getByRole("button", { name: /edit configuration fetch/i }),
    );
    expect(screen.queryByLabelText("Server name")).not.toBeInTheDocument();
    const command = screen.getByLabelText("Command");
    await user.clear(command);
    await user.type(command, "npx");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith({
      mcp_config: {
        version: 1,
        mcpServers: {
          fetch: {
            timeout: 30,
            tools: { include: ["fetch_url"] },
            command: "npx",
          },
          docs: { url: "https://example.test/mcp" },
        },
      },
    });
  });

  it("round-trips empty stdio arguments and environment values", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab({
      mcp_config: {
        mcpServers: {
          fetch: {
            command: "uvx",
            args: [""],
            env: { EMPTY_VALUE: "" },
          },
        },
      },
    });

    await user.click(
      screen.getByRole("button", { name: /edit configuration fetch/i }),
    );
    expect(screen.getByLabelText("Startup arguments 1")).toHaveValue("");
    expect(
      screen.getByLabelText("Environment variables: Value 1"),
    ).toHaveValue("");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith({
      mcp_config: {
        mcpServers: {
          fetch: {
            command: "uvx",
            args: [""],
            env: { EMPTY_VALUE: "" },
          },
        },
      },
    });
  });

  it("preserves leading, middle, and trailing empty stdio arguments", async () => {
    const user = userEvent.setup();
    const args = ["", "2222", "", "333", ""];
    const { onSave } = renderTab({
      mcp_config: {
        mcpServers: {
          fetch: { command: "uvx", args },
        },
      },
    });

    await user.click(
      screen.getByRole("button", { name: /edit configuration fetch/i }),
    );
    for (const [index, value] of args.entries()) {
      expect(screen.getByLabelText(`Startup arguments ${index + 1}`)).toHaveValue(
        value,
      );
    }
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith({
      mcp_config: {
        mcpServers: {
          fetch: { command: "uvx", args },
        },
      },
    });
  });

  it("saves leading, middle, and trailing empty rows added in the form", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab({
      mcp_config: {
        mcpServers: {
          fetch: { command: "uvx" },
        },
      },
    });

    await user.click(
      screen.getByRole("button", { name: /edit configuration fetch/i }),
    );
    const addArgument = screen.getByRole("button", { name: "Add argument" });
    for (let index = 0; index < 5; index += 1) {
      await user.click(addArgument);
    }
    await user.type(screen.getByLabelText("Startup arguments 2"), "2222");
    await user.type(screen.getByLabelText("Startup arguments 4"), "333");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith({
      mcp_config: {
        mcpServers: {
          fetch: { command: "uvx", args: ["", "2222", "", "333", ""] },
        },
      },
    });
  });

  it("round-trips empty HTTP header values", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab({
      mcp_config: {
        mcpServers: {
          docs: {
            type: "http",
            url: "https://example.test/mcp",
            headers: { "X-Empty": "" },
          },
        },
      },
    });

    await user.click(
      screen.getByRole("button", { name: /edit configuration docs/i }),
    );
    expect(screen.getByLabelText("HTTP headers: Value 1")).toHaveValue("");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith({
      mcp_config: {
        mcpServers: {
          docs: {
            type: "http",
            url: "https://example.test/mcp",
            headers: { "X-Empty": "" },
          },
        },
      },
    });
  });

  it("deletes the last managed server only after confirmation", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab({
      mcp_config: { mcpServers: { fetch: { command: "uvx" } } },
    });

    await user.click(
      screen.getByRole("button", { name: /delete mcp server fetch/i }),
    );
    expect(screen.getByText(/runtime servers are not affected/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /delete server/i }));

    expect(onSave).toHaveBeenCalledWith({ mcp_config: null });
  });

  it("blocks invalid single-server JSON", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab();

    await user.click(screen.getByRole("button", { name: /add mcp/i }));
    await user.type(screen.getByLabelText("Server name"), "broken");
    await user.click(screen.getByRole("tab", { name: "JSON" }));
    fireEvent.change(screen.getByLabelText(/MCP server JSON configuration/i), {
      target: { value: "{not json" },
    });

    expect(screen.queryByText(/invalid json/i)).toBeNull();
    const addButton = screen.getByRole("button", { name: "Add" });
    expect(addButton).toBeEnabled();

    await user.click(addButton);

    expect(screen.getByText(/invalid json/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/MCP server JSON configuration/i)).toHaveFocus();
    expect(onSave).not.toHaveBeenCalled();
  });

});

// The workspace layer (GH #6062) reaches an agent only when someone assigns
// it, so the tab has to show what is assigned, let an owner assign more, and
// never imply an unassigned library entry is in play.
describe("McpConfigTab workspace servers", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    workspaceMcp.assigned = [];
    workspaceMcp.library = [];
    mockAddServer.mockResolvedValue([]);
    mockSetEnabled.mockResolvedValue([]);
    mockRemoveServer.mockResolvedValue([]);
  });

  it("lists the workspace servers assigned to this agent", async () => {
    workspaceMcp.assigned = [wsServer({ enabled: true })];
    renderTab({ mcp_config: null });

    const row = (await screen.findByText("shared-linear")).closest("li")!;
    expect(within(row).getByText("Streamable HTTP")).toBeVisible();
    expect(screen.getByText(/From the workspace library/)).toBeInTheDocument();
  });

  // The defining property of this model: a library entry nobody assigned must
  // not appear as if the agent had it.
  it("does not show unassigned library servers as the agent's", async () => {
    workspaceMcp.library = [wsServer({ name: "not-assigned" })];
    renderTab({ mcp_config: null });

    expect(
      await screen.findByText(/No workspace MCP servers assigned/),
    ).toBeInTheDocument();
    expect(screen.queryByText("not-assigned")).toBeNull();
  });

  it("assigns a library server through the picker", async () => {
    const user = userEvent.setup();
    workspaceMcp.library = [wsServer({ id: "srv-9", name: "pickable" })];
    renderTab({ mcp_config: null });

    await user.click(await screen.findByRole("button", { name: /Add from workspace/ }));
    const option = await screen.findByRole("menuitem", { name: /pickable/ });
    expect(within(option).getByText("Streamable HTTP")).toBeVisible();
    await user.click(option);

    await waitFor(() => expect(mockAddServer).toHaveBeenCalledWith("srv-9"));
  });

  it("toggles an assignment off without dropping it", async () => {
    const user = userEvent.setup();
    workspaceMcp.assigned = [wsServer({ enabled: true })];
    renderTab({ mcp_config: null });

    await user.click(
      await screen.findByRole("switch", { name: /Enable shared-linear/i }),
    );

    await waitFor(() =>
      expect(mockSetEnabled).toHaveBeenCalledWith({ serverId: "srv-1", enabled: false }),
    );
    expect(mockRemoveServer).not.toHaveBeenCalled();
  });

  it("removes an assignment", async () => {
    const user = userEvent.setup();
    workspaceMcp.assigned = [wsServer({ enabled: true })];
    renderTab({ mcp_config: null });

    await user.click(
      await screen.findByRole("button", { name: /Remove shared-linear/i }),
    );

    await waitFor(() => expect(mockRemoveServer).toHaveBeenCalledWith("srv-1"));
  });

  it("marks an assigned server the agent overrides by name", async () => {
    workspaceMcp.assigned = [wsServer({ name: "linear", enabled: true })];
    renderTab({
      mcp_config: { mcpServers: { linear: { url: "https://agent.example" } } },
    });

    expect(await screen.findByText("Overridden")).toBeInTheDocument();
  });

  // A member without edit rights can still read what the agent runs with —
  // the inventory carries no credential material — but every write affordance
  // is hidden rather than left to 403 on click.
  it("hides the assignment controls from a viewer who cannot edit", async () => {
    workspaceMcp.assigned = [wsServer({ enabled: true })];
    workspaceMcp.library = [wsServer({ id: "srv-9", name: "pickable" })];
    render(
      <TestShell>
        <McpConfigTab
          agent={{
            ...baseAgent,
            mcp_config: {
              version: 1,
              mcpServers: { fetch: { command: "uvx" } },
            },
          }}
          canEdit={false}
          onSave={vi.fn()}
        />
      </TestShell>,
    );

    expect(await screen.findByText("shared-linear")).toBeInTheDocument();
    expect(screen.getByText("fetch")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Add MCP/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /Rename server fetch/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /Edit configuration fetch/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /Delete MCP server fetch/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /Add from workspace/ })).toBeNull();
    expect(screen.queryByRole("switch")).toBeNull();
    expect(screen.queryByRole("button", { name: /Remove shared-linear/i })).toBeNull();
  });

  it("points at workspace Settings when the library is empty", async () => {
    renderTab({ mcp_config: null });

    expect(
      await screen.findByText(/no MCP servers to assign yet/i),
    ).toBeInTheDocument();
  });
});

describe("McpConfigTab effective set", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    workspaceMcp.assigned = [];
    workspaceMcp.library = [];
  });

  // Same transport hazard as before, reached through the SAVED config: the
  // form emits `type: "http"`, so neither entry may be editable through it.
  // `websocket` is the one that catches using mcpTransport() for this.
  it.each([
    ["sse", { type: "sse", url: "https://sse.example" }],
    ["websocket", { type: "websocket", url: "wss://example.test" }],
  ])("edits an agent's %s server through JSON, not the form", async (_label, entry) => {
    const user = userEvent.setup();
    renderTab({ mcp_config: { mcpServers: { streamy: entry } } });

    await user.click(
      screen.getByRole("button", { name: /edit configuration streamy/i }),
    );

    expect(screen.getByRole("tab", { name: "JSON" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByRole("tab", { name: "Visual editor" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  // The common shapes must stay on the form — this guard is about entries the
  // form would CHANGE, not a general retreat to the JSON editor.
  it.each([
    ["stdio", { command: "uvx" }],
    ["typed http", { type: "http", url: "https://mcp.example" }],
    ["untyped url", { url: "https://mcp.example" }],
  ])("keeps editing a %s server on the form", async (_label, entry) => {
    const user = userEvent.setup();
    renderTab({ mcp_config: { mcpServers: { normal: entry } } });

    await user.click(
      screen.getByRole("button", { name: /edit configuration normal/i }),
    );

    expect(screen.getByRole("tab", { name: "Visual editor" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });
});
