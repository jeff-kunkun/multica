import { beforeEach, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { renderWithI18n } from "../../test/i18n";
import { AgentPermissionsTab } from "./agent-permissions-tab";

const fixture = vi.hoisted(() => ({
  role: "admin",
  settings: { retained_preference: true } as Record<string, unknown>,
}));

vi.mock("@multica/core/api", () => ({ api: { updateWorkspace: vi.fn() } }));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws", settings: fixture.settings }),
}));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: unknown) => unknown) => selector({ user: { id: "u" } }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({
    queryKey: ["members", "ws"],
    queryFn: async () => [{ user_id: "u", role: fixture.role }],
  }),
  workspaceKeys: { list: () => ["workspaces"] },
}));
vi.mock("@multica/core/workspace/agent-spawn", () => ({
  agentSpawnOptions: () => ({ queryKey: ["spawn", "ws"], queryFn: async () => null }),
  useUpdateAgentSpawn: () => ({ mutate: vi.fn() }),
}));

const update = vi.mocked(api.updateWorkspace);

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <AgentPermissionsTab />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  fixture.role = "admin";
  fixture.settings = { retained_preference: true };
  update.mockReset().mockResolvedValue({ id: "ws" } as Awaited<ReturnType<typeof api.updateWorkspace>>);
});

it("saves the bot limit while preserving other workspace settings", async () => {
  mount();
  const input = screen.getByRole("spinbutton", { name: "Bot wake limit" });
  await waitFor(() => expect(input).toBeEnabled());
  expect(input).toHaveValue(4);
  expect(input).toHaveAttribute("min", "1");
  expect(input).toHaveAttribute("max", "100");
  const row = input.closest('[data-settings-anchor="bot-wake-chain"]') as HTMLElement;
  expect(within(row).queryByRole("switch")).toBeNull();

  fireEvent.change(input, { target: { value: "8" } });
  fireEvent.blur(input);
  await waitFor(() =>
    expect(update).toHaveBeenCalledWith("ws", {
      settings: {
        retained_preference: true,
        agent_chain_budget: 30,
        agent_task_timeout_minutes: 0,
        bot_wake_chain_limit: 8,
      },
    }),
  );
});

it("restores an empty or zero draft without saving an unlimited limit", async () => {
  fixture.settings.bot_wake_chain_limit = 7;
  mount();
  const input = screen.getByRole("spinbutton", { name: "Bot wake limit" });
  await waitFor(() => expect(input).toBeEnabled());
  for (const value of ["", "0", "1.5"]) {
    fireEvent.change(input, { target: { value } });
    fireEvent.blur(input);
    expect(input).toHaveValue(7);
  }
  expect(update).not.toHaveBeenCalled();
});

it("shows the saved limit read-only to workspace members", async () => {
  fixture.role = "member";
  fixture.settings.bot_wake_chain_limit = 12;
  mount();
  const input = screen.getByRole("spinbutton", { name: "Bot wake limit" });
  expect(input).toBeDisabled();
  expect(input).toHaveValue(12);
  expect(screen.getByText(/Only admins can change this/)).toBeVisible();
  expect(update).not.toHaveBeenCalled();
});
