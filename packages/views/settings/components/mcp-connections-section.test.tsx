// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import { McpConnectionsSection } from "./mcp-connections-section";
import enSettings from "../../locales/en/settings.json";
import type { ExternalBot } from "@multica/core/types";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  revoke: vi.fn(),
  copy: vi.fn(),
  data: [] as unknown[],
}));
vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: mocks.data, isLoading: false, isError: false }),
}));
vi.mock("@multica/core/bots", () => ({
  mcpConnectionsOptions: () => ({}),
  useCreateMcpConnection: () => mocks.create,
  useRevokeMcpConnection: () => ({ mutate: mocks.revoke, isPending: false }),
}));
vi.mock("@multica/ui/lib/clipboard", () => ({ copyText: mocks.copy }));
const bot = { id: "b", name: "Project Bot", status: "active" } as ExternalBot;
function mount(canManage = true) {
  return render(
    <I18nProvider locale="en" resources={{ en: { settings: enSettings } }}>
      <McpConnectionsSection wsId="ws" canManage={canManage} bots={[bot]} />
    </I18nProvider>,
  );
}
beforeEach(() => {
  cleanup();
  vi.clearAllMocks();
  mocks.data = [];
});
describe("MCP connection controls", () => {
  it("explains missing permission and disables new authorization", () => {
    mount(false);
    expect(screen.getByText("Requires owner or admin access.")).toBeTruthy();
    expect(
      screen
        .getByRole("button", { name: "Connect app" })
        .hasAttribute("disabled"),
    ).toBe(true);
  });
  it("shows a one-time code only until the dialog closes", async () => {
    mocks.create.mockResolvedValue({
      id: "c",
      code: "one-time-code",
      endpoint: "https://test.example/mcp",
      exchangeUrl: "https://test.example/exchange",
      expiresAt: new Date(Date.now() + 300000).toISOString(),
    });
    mount();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Connect app" }));
    await user.type(screen.getByLabelText("Connection name"), "Demo app");
    await user.click(screen.getByRole("combobox"));
    await user.click(
      await screen.findByRole("option", { name: "Project Bot" }),
    );
    await user.click(
      screen.getByRole("button", { name: "Get authorization code" }),
    );
    expect(await screen.findByText("one-time-code")).toBeTruthy();
    expect(mocks.create).toHaveBeenCalledWith({ name: "Demo app", botId: "b" });
    await user.click(screen.getAllByRole("button", { name: "Close" })[0]!);
    await waitFor(() => expect(screen.queryByText("one-time-code")).toBeNull());
  });
  it("requires confirmation and waits for revoke success before closing", async () => {
    mocks.data = [
      { id: "c", name: "Test app", revoked: false, expiresAt: "2027-01-01" },
    ];
    mount();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Revoke" }));
    expect(mocks.revoke).not.toHaveBeenCalled();
    const buttons = screen.getAllByRole("button", { name: "Revoke" });
    await user.click(buttons[buttons.length - 1]!);
    expect(mocks.revoke).toHaveBeenCalledWith("c", expect.any(Object));
    expect(screen.getByRole("alertdialog")).toBeTruthy();
    mocks.revoke.mock.calls[0]![1].onSuccess();
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
  });
});
