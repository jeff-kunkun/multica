// @vitest-environment jsdom

import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

const roleRef = vi.hoisted(() => ({ current: "owner" as string }));
const botsRef = vi.hoisted(() => ({ current: [] as unknown[] }));
const mockCreate = vi.hoisted(() => vi.fn());
const mockStop = vi.hoisted(() => vi.fn());
const listEnabledRef = vi.hoisted(() => ({ current: undefined as boolean | undefined }));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey?: unknown[]; enabled?: boolean }) => {
    if (opts.queryKey?.[0] !== "bots") return { data: undefined, isLoading: false };
    listEnabledRef.current = opts.enabled;
    return { data: botsRef.current, isLoading: false, isError: false };
  },
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  queryOptions: <T,>(opts: T) => opts,
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: roleRef.current }),
}));
const mutation = (fn: (arg: unknown) => unknown) => ({
  isPending: false,
  mutate: (arg: unknown, cb?: { onSuccess?: (r: unknown) => void; onError?: (e: unknown) => void }) =>
    Promise.resolve(fn(arg)).then(cb?.onSuccess, cb?.onError),
  mutateAsync: (arg: unknown) => Promise.resolve(fn(arg)),
});
vi.mock("@multica/core/bots", () => ({
  externalBotListOptions: (_ws: string, enabled: boolean) => ({ queryKey: ["bots"], enabled }),
  useCreateExternalBot: () => mutation(mockCreate),
  useRotateExternalBotKey: () => mutation(vi.fn()),
  useStopExternalBot: () => mutation(mockStop),
  useDeleteExternalBot: () => mutation(vi.fn()),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { BotsTab } from "./bots-tab";

const renderTab = () =>
  render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, settings: enSettings } }}>
      <BotsTab />
    </I18nProvider>,
  );

const bot = (over: Record<string, unknown> = {}) => ({
  id: "b1",
  name: "Test Bot",
  type: "grok",
  status: "active",
  token_prefix: "mul_abc",
  expires_at: null,
  last_used_at: null,
  created_at: "2026-10-10T00:00:00Z",
  stopped_at: null,
  ...over,
});

describe("BotsTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    roleRef.current = "owner";
    botsRef.current = [];
  });

  it("adds a bot and shows the setup text once", async () => {
    mockCreate.mockResolvedValue({
      bot: bot(),
      token: "mul_secret",
      setup_text: "multica login --token mul_secret",
    });
    renderTab();
    await userEvent.click(screen.getByRole("button", { name: "Add bot" }));
    await userEvent.type(screen.getByLabelText("Name"), "Test Bot");
    await userEvent.click(screen.getByRole("button", { name: "Create key" }));
    await waitFor(() =>
      expect(mockCreate).toHaveBeenCalledWith({ name: "Test Bot", type: "grok", expires_in_days: undefined }),
    );
    expect(await screen.findByTestId("bot-setup-text")).toHaveTextContent("multica login --token mul_secret");
  });

  it("stops a bot only after confirming", async () => {
    botsRef.current = [bot()];
    mockStop.mockResolvedValue(bot({ status: "stopped" }));
    renderTab();
    expect(screen.getByText(/Never used/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Actions for Test Bot" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Stop" }));
    expect(mockStop).not.toHaveBeenCalled();
    await userEvent.click(await screen.findByRole("button", { name: "Stop" }));
    await waitFor(() => expect(mockStop).toHaveBeenCalledWith("b1"));
  });

  it("does not load the list for a non-admin", () => {
    roleRef.current = "member";
    renderTab();
    expect(listEnabledRef.current).toBe(false);
  });
});
