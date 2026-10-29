import { beforeEach, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { renderWithI18n } from "../../test/i18n";
import { WakeupsTab } from "./wakeups-tab";

vi.mock("@multica/core/api", () => ({
  api: {
    listWorkspaceSystemWakeups: vi.fn(),
    updateWorkspaceSystemWakeup: vi.fn(),
    getWorkspaceSessionResume: vi.fn(),
    updateWorkspaceSessionResume: vi.fn(),
  },
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (s: unknown) => unknown) => selector({ user: { id: "u" } }),
}));
let role = "admin";
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members", "ws"], queryFn: async () => [{ user_id: "u", role }] }),
  workspaceKeys: { all: (wsId: string) => ["workspaces", wsId] },
}));

const list = vi.mocked(api.listWorkspaceSystemWakeups);
const update = vi.mocked(api.updateWorkspaceSystemWakeup);
const getCap = vi.mocked(api.getWorkspaceSessionResume);
const updateCap = vi.mocked(api.updateWorkspaceSessionResume);

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <WakeupsTab />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  role = "admin";
  list.mockReset().mockResolvedValue([
    { rule: "child_done", enabled: true, instruction: "", builtin_instruction: "Advance the next stage.", customized: 2 },
  ]);
  update.mockReset().mockResolvedValue(undefined);
  getCap.mockReset().mockResolvedValue({ max_context_tokens: 0 });
  updateCap.mockReset().mockImplementation(async (_ws, max_context_tokens) => ({ max_context_tokens }));
});

it("turns the sub-issue rule's default off and saves a workspace instruction", async () => {
  mount();
  const toggle = await screen.findByRole("switch", { name: "On by default" });
  expect(screen.getByText("2 open issues have their own setting and ignore this default.")).toBeVisible();
  fireEvent.click(toggle);
  await waitFor(() => expect(update).toHaveBeenCalledWith("child_done", { enabled: false }));

  const input = screen.getByRole("textbox", { name: "Default instruction" });
  const save = within(input.closest("form")!).getByRole("button", { name: "Save" });
  expect(screen.getByText("Advance the next stage.")).toBeVisible();
  expect(save).toBeDisabled();
  fireEvent.change(input, { target: { value: "  Wrap up in the parent.  " } });
  fireEvent.click(save);
  await waitFor(() => expect(update).toHaveBeenCalledWith("child_done", { instruction: "Wrap up in the parent." }));
});

it("shows the defaults read-only to members who cannot change them", async () => {
  role = "member";
  mount();
  expect(await screen.findByRole("switch", { name: "On by default" })).toHaveAttribute("aria-disabled", "true");
  expect(screen.getByText("Only owners and admins can change these defaults.")).toBeVisible();
  expect(screen.getByRole("textbox", { name: "Default instruction" })).toBeDisabled();
  expect(await screen.findByRole("spinbutton", { name: "Context limit (tokens)" })).toBeDisabled();
  expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
});

it("saves the session context limit, off by default, and refuses one out of range", async () => {
  mount();
  const input = await screen.findByRole("spinbutton", { name: "Context limit (tokens)" });
  expect(input).toHaveValue(0);
  expect(screen.getByText("0 means off, the default. Claude and Codex report their size.")).toBeVisible();
  const form = input.closest("form")!;
  fireEvent.change(input, { target: { value: "2000001" } });
  fireEvent.submit(form);
  expect(await screen.findByText("Enter a whole number from 0 to 2000000.")).toBeVisible();
  expect(updateCap).not.toHaveBeenCalled();
  fireEvent.change(input, { target: { value: "150000" } });
  fireEvent.submit(form);
  await waitFor(() => expect(updateCap).toHaveBeenCalledWith("ws", 150000));
});
