// @vitest-environment jsdom

import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";
import { ApiError } from "@multica/core/api";

const roleRef = vi.hoisted(() => ({ current: "owner" as string }));
const botsRef = vi.hoisted(() => ({ current: [] as unknown[] }));
const mockCreate = vi.hoisted(() => vi.fn());
const mockStop = vi.hoisted(() => vi.fn());
const listEnabledRef = vi.hoisted(() => ({ current: undefined as boolean | undefined }));
const searchRef = vi.hoisted(() => ({ current: "" }));
const wakeRef = vi.hoisted(() => ({ current: { data: undefined as unknown, error: null as unknown } }));
const logRef = vi.hoisted(() => ({
  pages: [] as unknown[],
  hasNextPage: false,
  fetchNextPage: vi.fn(),
  pendingSeen: [] as boolean[],
}));
const mockUpdateWake = vi.hoisted(() => vi.fn());

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey?: unknown[]; enabled?: boolean }) => {
    if (opts.queryKey?.[0] === "wake") {
      return { ...wakeRef.current, isLoading: false, refetch: vi.fn() };
    }
    if (opts.queryKey?.[0] !== "bots") return { data: undefined, isLoading: false };
    listEnabledRef.current = opts.enabled;
    return { data: botsRef.current, isLoading: false, isError: false };
  },
  useInfiniteQuery: (opts: { pending: boolean }) => {
    logRef.pendingSeen.push(opts.pending);
    return {
      data: { pages: logRef.pages },
      isLoading: false,
      isError: false,
      hasNextPage: logRef.hasNextPage,
      isFetchingNextPage: false,
      fetchNextPage: logRef.fetchNextPage,
      refetch: vi.fn(),
    };
  },
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  queryOptions: <T,>(opts: T) => opts,
}));
vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    pathname: "/acme/settings",
    searchParams: new URLSearchParams(searchRef.current),
  }),
  AppLink: ({ href, children, ...rest }: { href: string; children: React.ReactNode }) => (
    <a href={href} {...rest}>{children}</a>
  ),
}));
vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/acme/issues/${id}`,
    chatSession: (id: string) => `/acme/chat/${id}`,
  }),
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
  BOT_WAKE_EVENT_TYPES: ["mention", "started", "done", "blocked", "needs_kun", "task", "chat"],
  botWakeOptions: () => ({ queryKey: ["wake"] }),
  botWakeEventsOptions: (_ws: string, _bot: string, pending: boolean) => ({ pending }),
  useUpdateBotWake: () => mutation(mockUpdateWake),
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
    searchRef.current = "";
    wakeRef.current = { data: undefined, error: null };
    logRef.pages = [];
    logRef.hasNextPage = false;
    logRef.pendingSeen = [];
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

  describe("wake settings (DENE-1793)", () => {
    const wake = (over: Record<string, unknown> = {}) => ({
      webhook_configured: true,
      events: ["mention", "task"],
      hourly_limit: 6,
      quiet_hours: { enabled: true, start: "23:00", end: "08:00", timezone: "Asia/Shanghai" },
      ...over,
    });
    const openWake = () => {
      botsRef.current = [bot()];
      searchRef.current = "tab=bots&integration=b1";
      renderTab();
    };

    it("links each bot to its wake settings", () => {
      botsRef.current = [bot()];
      renderTab();
      expect(screen.getByRole("link", { name: "Wake settings" })).toHaveAttribute(
        "href",
        "/acme/settings?tab=bots&integration=b1",
      );
    });

    it("never shows the saved endpoint and sends URL and key together", async () => {
      wakeRef.current = { data: wake(), error: null };
      mockUpdateWake.mockResolvedValue(wake());
      openWake();
      expect(screen.getByTestId("bot-webhook-status")).toHaveTextContent("Configured");
      const url = screen.getByLabelText("URL");
      const key = screen.getByLabelText("Key");
      expect(url).toHaveValue("");
      expect(key).toHaveValue("");
      const save = screen.getByRole("button", { name: "Replace endpoint" });
      expect(save).toBeDisabled();
      await userEvent.type(url, "https://bot.example.com/hook");
      expect(save).toBeDisabled();
      await userEvent.type(key, "Bearer s3cret");
      await userEvent.click(save);
      await waitFor(() =>
        expect(mockUpdateWake).toHaveBeenCalledWith({
          webhook_url: "https://bot.example.com/hook",
          webhook_key: "Bearer s3cret",
        }),
      );
      await waitFor(() => expect(key).toHaveValue(""));
    });

    it("disconnects only after confirming, with two empty strings", async () => {
      wakeRef.current = { data: wake(), error: null };
      mockUpdateWake.mockResolvedValue(wake({ webhook_configured: false }));
      openWake();
      await userEvent.click(screen.getByRole("button", { name: "Disconnect" }));
      expect(mockUpdateWake).not.toHaveBeenCalled();
      const dialog = await screen.findByRole("alertdialog");
      await userEvent.click(within(dialog).getByRole("button", { name: "Disconnect" }));
      await waitFor(() =>
        expect(mockUpdateWake).toHaveBeenCalledWith({ webhook_url: "", webhook_key: "" }),
      );
    });

    it("saves rules without touching the endpoint", async () => {
      wakeRef.current = { data: wake(), error: null };
      mockUpdateWake.mockResolvedValue(wake());
      openWake();
      const save = screen.getByRole("button", { name: "Save rules" });
      expect(save).toBeDisabled();
      await userEvent.click(screen.getByRole("checkbox", { name: /^Done/ }));
      const limit = screen.getByLabelText("Max pushes per hour");
      await userEvent.clear(limit);
      await userEvent.type(limit, "121");
      expect(screen.getByText("Enter a whole number from 0 to 120.")).toBeInTheDocument();
      expect(save).toBeDisabled();
      await userEvent.clear(limit);
      await userEvent.type(limit, "0");
      await userEvent.click(save);
      await waitFor(() =>
        expect(mockUpdateWake).toHaveBeenCalledWith({
          events: ["mention", "done", "task"],
          hourly_limit: 0,
          quiet_hours: { enabled: true, start: "23:00", end: "08:00", timezone: "Asia/Shanghai" },
        }),
      );
    });

    it("explains a forbidden load instead of showing a form", () => {
      wakeRef.current = { data: undefined, error: new ApiError("forbidden", 403, "Forbidden") };
      openWake();
      expect(
        screen.getByText("Only workspace admins can view and change wake settings."),
      ).toBeInTheDocument();
      expect(screen.queryByLabelText("URL")).not.toBeInTheDocument();
    });

    it("keeps paging past an empty page and never calls received 'done'", async () => {
      wakeRef.current = { data: wake(), error: null };
      logRef.pages = [
        {
          events: [
            {
              event_id: "e1",
              sequence: 9,
              type: "done",
              issue_id: "i1",
              chat_session_id: null,
              status: "received",
              reason: "",
              attempts: 1,
              created_at: "2026-10-10T01:00:00Z",
            },
            {
              event_id: "e2",
              sequence: 8,
              type: "mention",
              issue_id: null,
              chat_session_id: "c1",
              status: "failed",
              reason: "endpoint returned 500; retries exhausted",
              attempts: 3,
              created_at: "2026-10-10T00:00:00Z",
            },
          ],
          next_cursor: "8",
        },
        { events: [], next_cursor: "5" },
      ];
      logRef.hasNextPage = true;
      openWake();
      expect(screen.getByText(/only means the endpoint answered 200/)).toBeInTheDocument();
      expect(screen.getByText("Received")).toBeInTheDocument();
      expect(screen.getByText("endpoint returned 500; retries exhausted")).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "Open chat" })).toHaveAttribute("href", "/acme/chat/c1");
      await userEvent.click(screen.getByRole("button", { name: "Load more" }));
      expect(logRef.fetchNextPage).toHaveBeenCalled();
      await userEvent.click(screen.getByRole("tab", { name: "Pending" }));
      expect(logRef.pendingSeen).toContain(true);
    });
  });
});
